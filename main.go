package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/joho/godotenv"
	_ "github.com/mattn/go-sqlite3"
)

const llmPrompt = `
Analyze the following user input. It's a user request to remind about something in specific time. Your goal is define:
• requested reminder's date and time ("datetime" field, format "2006-01-02 15:04:05"). if the user didn't provide date then use today date. the user allowed to provide relative date or time, for example "today", "tomorrow", "in 10 minutes", "after 1 hour", etc. – in this case calculate datetime based on current datetime: %s. 
• label for reminder ("label" field)
• generate an answer to the user about accepted reminder. language of answer is russian. For example: "Принято. 26 мая в 16:20 напомню тебе, что надо решить задачу".
Output Requirements:
•  Output must be in valid JSON with UTF-8 encoded strings.
•  The JSON structure must be: {"datetime": "2006-01-02 15:04:05", "label": "string", "answer": "string"}
•  If you cannot generate any field, leave it empty in the JSON.
•  Escape double quotes " by prefixing them with a backslash \.
•  Do not escape other characters.
•  Do not add extra formatting, line breaks, reasoning or any additional text outside the JSON structure.
`

var (
	db  *sql.DB
	bot *tgbotapi.BotAPI
)

// LLMOutput represents the JSON structure we expect from the LLM.
type LLMOutput struct {
	Datetime string `json:"datetime"`
	Label    string `json:"label"`
	Answer   string `json:"answer"`
}

func main() {
	// Load environment variables from .env file.
	err := godotenv.Load()
	if err != nil {
		log.Printf("Error loading .env file: %v", err)
	}

	// Open (or create) the SQLite database
	db, err = sql.Open("sqlite3", "reminders.db")
	if err != nil {
		log.Fatalf("Error opening DB: %v", err)
	}
	defer db.Close()

	//db.SetMaxOpenConns(8)

	// Create reminders table if it doesn't exist
	createTableSQL := `
	CREATE TABLE IF NOT EXISTS reminders (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		chat_id INTEGER,
		user_id INTEGER,
		reminder_time DATETIME,
		label TEXT,
		notified INTEGER DEFAULT 0
	);`
	_, err = db.Exec(createTableSQL)
	if err != nil {
		log.Fatalf("Error creating table: %v", err)
	}

	// Retrieve Telegram bot token from environment variables.
	botToken := os.Getenv("TELEGRAM_BOT_TOKEN")
	if botToken == "" {
		log.Fatalf("TELEGRAM_BOT_TOKEN is not set in environment")
	}

	// Initialize Telegram Bot
	bot, err = tgbotapi.NewBotAPI(botToken)
	if err != nil {
		log.Fatalf("Error creating bot: %v", err)
	}
	log.Printf("Authorized on account %s", bot.Self.UserName)

	// Start the background goroutine that checks for due reminders
	go checkReminders()

	// Set up update configuration
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	updates := bot.GetUpdatesChan(u)

	// Process incoming messages
	for update := range updates {
		if update.Message != nil {
			// If the message contains voice/audio, handle it separately.
			if update.Message.Voice != nil {
				handleAudioMessage(update.Message)
			} else {
				handleMessage(update.Message)
			}
		}
	}
}

// handleMessage processes text messages as before.
func handleMessage(msg *tgbotapi.Message) {

	log.Println("handle text message")

	// Call the LLM to parse the message.
	reminderTime, label, answer, err := parseMessageWithLLM(msg.Text)
	if err != nil {
		log.Printf("LLM parse error: %v", err)
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Ошибка распознования запроса. Попробуй сформулировать по-другому.")
		_, err = bot.Send(reply)
		if err != nil {
			log.Printf("error sending answer: %v", err)
		}
		return
	}

	log.Println("handle text message 2")

	// Insert the reminder into the database.
	_, err = db.Exec("INSERT INTO reminders (chat_id, user_id, reminder_time, label) VALUES (?, ?, ?, ?)",
		msg.Chat.ID, msg.From.ID, reminderTime, label)
	if err != nil {
		log.Printf("DB insert error: %v", err)
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Error storing reminder")
		_, err = bot.Send(reply)
		if err != nil {
			log.Printf("error sending answer: %v", err)
		}
		return
	}

	log.Println("handle text message 3")

	// Log the accepted reminder.
	log.Printf("Accepted reminder: '%s' at %s for chat ID %d", label, reminderTime.Format("2006-01-02 15:04:05"), msg.Chat.ID)

	// Acknowledge the reminder creation using the answer provided by the LLM.
	reply := tgbotapi.NewMessage(msg.Chat.ID, answer)
	_, err = bot.Send(reply)
	if err != nil {
		log.Printf("error sending answer: %v", err)
	}
}

// handleAudioMessage processes an audio/voice message, transcribes it with OpenAI Whisper,
// then passes the transcription to the existing parsing logic.
func handleAudioMessage(msg *tgbotapi.Message) {

	log.Println("handle audio message")

	fileID := msg.Voice.FileID
	// Get file information from Telegram.
	fileConfig := tgbotapi.FileConfig{FileID: fileID}
	file, err := bot.GetFile(fileConfig)
	if err != nil {
		log.Printf("Error getting file info: %v", err)
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Не удалось получить аудио файл.")
		_, err = bot.Send(reply)
		if err != nil {
			log.Printf("error sending answer: %v", err)
		}
		return
	}

	log.Println("handle audio message 2")

	// Download the file using the provided file path.
	fileURL := file.Link(bot.Token)
	resp, err := http.Get(fileURL)
	if err != nil {
		log.Printf("Error downloading file: %v", err)
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Не удалось загрузить аудио файл.")
		_, err = bot.Send(reply)
		if err != nil {
			log.Printf("error sending answer: %v", err)
		}
		return
	}
	defer resp.Body.Close()

	log.Println("handle audio message 3")

	// Save the audio file locally (temporary).
	tempFile, err := ioutil.TempFile("", "audio-*.ogg")
	if err != nil {
		log.Printf("Error creating temp file: %v", err)
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Ошибка обработки аудио файла.")
		_, err = bot.Send(reply)
		if err != nil {
			log.Printf("error sending answer: %v", err)
		}
		return
	}
	defer func(name string) {
		err := os.Remove(name)
		if err != nil {
			log.Printf("Error removing temp file: %v", err)
		}
	}(tempFile.Name())

	log.Println("handle audio message 4")

	_, err = io.Copy(tempFile, resp.Body)
	if err != nil {
		log.Printf("Error saving audio file: %v", err)
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Ошибка сохранения аудио файла.")
		_, err = bot.Send(reply)
		if err != nil {
			log.Printf("error sending answer: %v", err)
		}
		return
	}
	err = tempFile.Close()
	if err != nil {
		log.Printf("Error closing temp file: %v", err)
	}

	log.Println("handle audio message 5")

	// Call OpenAI Whisper API to transcribe the audio.
	transcription, err := transcribeAudio(tempFile.Name())
	if err != nil {
		log.Printf("Error transcribing audio: %v", err)
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Ошибка распознавания аудио.")
		_, err = bot.Send(reply)
		if err != nil {
			log.Printf("error sending answer: %v", err)
		}
		return
	}

	log.Println("handle audio message 6")

	// Use the transcription as the user input.
	reminderTime, label, answer, err := parseMessageWithLLM(transcription)
	if err != nil {
		log.Printf("LLM parse error after transcription: %v", err)
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Ошибка распознования запроса из аудио. Попробуй сформулировать по-другому.")
		_, err = bot.Send(reply)
		if err != nil {
			log.Printf("error sending answer: %v", err)
		}
		return
	}

	log.Println("handle audio message 7")

	// Insert the reminder into the database.
	res, err := db.Exec("INSERT INTO reminders (chat_id, user_id, reminder_time, label) VALUES (?, ?, ?, ?)",
		msg.Chat.ID, msg.From.ID, reminderTime, label)

	log.Println("handle audio message 8", res, err)

	if err != nil {
		log.Printf("DB insert error: %v", err)
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Error storing reminder")
		_, err = bot.Send(reply)
		if err != nil {
			log.Printf("error sending answer: %v", err)
		}
		return
	}

	// Log the accepted reminder.
	log.Printf("Accepted reminder (from audio): '%s' at %s for chat ID %d", label, reminderTime.Format("2006-01-02 15:04:05"), msg.Chat.ID)

	// Acknowledge the reminder creation using the answer provided by the LLM.
	reply := tgbotapi.NewMessage(msg.Chat.ID, answer)
	_, err = bot.Send(reply)
	if err != nil {
		log.Printf("error sending answer: %v", err)
	}
}

// transcribeAudio sends the audio file to OpenAI's Whisper API and returns the transcription.
func transcribeAudio(filePath string) (string, error) {
	openaiAPIKey := os.Getenv("OPENAI_API_KEY")
	if openaiAPIKey == "" {
		return "", fmt.Errorf("OPENAI_API_KEY is not set in environment")
	}

	// Open the audio file.
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	// Prepare multipart form data.
	var requestBody bytes.Buffer
	writer := multipart.NewWriter(&requestBody)
	part, err := writer.CreateFormFile("file", filepath.Base(filePath))
	if err != nil {
		return "", err
	}
	_, err = io.Copy(part, file)
	if err != nil {
		return "", err
	}
	// Add additional fields required by the Whisper API.
	// For example, set model to "whisper-1"
	_ = writer.WriteField("model", "whisper-1")
	writer.Close()

	req, err := http.NewRequest("POST", "https://api.openai.com/v1/audio/transcriptions", &requestBody)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+openaiAPIKey)

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	// Define a response struct for the transcription API.
	var whisperResp struct {
		Text string `json:"text"`
	}
	err = json.Unmarshal(body, &whisperResp)
	if err != nil {
		return "", fmt.Errorf("error parsing transcription JSON: %v", err)
	}

	return whisperResp.Text, nil
}

// parseMessageWithLLM calls the OpenAI API to parse the user input.
// It sends a prompt asking to analyze the input and output JSON with three keys:
// "datetime", "label" and "answer". The datetime should be in the format YYYY-MM-DD HH:MM:SS,
// label is a short description, and answer is the confirmation message.
func parseMessageWithLLM(input string) (time.Time, string, string, error) {
	openaiAPIKey := os.Getenv("OPENAI_API_KEY")
	if openaiAPIKey == "" {
		return time.Time{}, "", "", fmt.Errorf("OPENAI_API_KEY is not set in environment")
	}

	// Prepare request payload for the Chat Completion endpoint using model "gpt-4o".
	reqBody, err := json.Marshal(map[string]interface{}{
		"model": "gpt-4o",
		"messages": []map[string]string{
			{"role": "developer", "content": fmt.Sprintf(llmPrompt, time.Now().Format("2006-01-02 15:04:05"))},
			{"role": "user", "content": input},
		},
	})
	if err != nil {
		return time.Time{}, "", "", err
	}

	req, err := http.NewRequest("POST", "https://api.openai.com/v1/chat/completions", bytes.NewBuffer(reqBody))
	if err != nil {
		return time.Time{}, "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+openaiAPIKey)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return time.Time{}, "", "", err
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return time.Time{}, "", "", err
	}

	// Define a struct to parse the OpenAI response.
	type ChatChoice struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	type OpenAIChatResponse struct {
		Choices []ChatChoice `json:"choices"`
	}

	var openaiResp OpenAIChatResponse
	err = json.Unmarshal(body, &openaiResp)
	if err != nil {
		return time.Time{}, "", "", err
	}

	if len(openaiResp.Choices) == 0 {
		return time.Time{}, "", "", fmt.Errorf("no choices returned from OpenAI")
	}

	// The model's output should be a JSON string; clean it up.
	outputText := strings.Trim(strings.TrimSpace(openaiResp.Choices[0].Message.Content), "\r\n```json")
	log.Println(outputText)
	startIdx := strings.Index(outputText, "{")
	endIdx := strings.LastIndex(outputText, "}")
	if startIdx == -1 || endIdx == -1 || startIdx >= endIdx {
		return time.Time{}, "", "", fmt.Errorf("failed to extract JSON from model output")
	}
	jsonStr := outputText[startIdx : endIdx+1]

	var result LLMOutput
	err = json.Unmarshal([]byte(jsonStr), &result)
	if err != nil {
		return time.Time{}, "", "", fmt.Errorf("error parsing JSON: %v", err)
	}

	reminderTime, err := time.Parse("2006-01-02 15:04:05", result.Datetime)
	if err != nil {
		return time.Time{}, "", "", fmt.Errorf("error parsing datetime: %v", err)
	}

	if strings.TrimSpace(result.Label) == "" {
		return time.Time{}, "", "", fmt.Errorf("label is empty")
	}

	if strings.TrimSpace(result.Answer) == "" {
		return time.Time{}, "", "", fmt.Errorf("answer is empty")
	}

	return reminderTime, result.Label, result.Answer, nil
}

// checkReminders periodically checks the database for reminders
// that are due and sends them to the user.
func checkReminders() {

	log.Println("Checking reminders...")

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		now := time.Now()
		rows, err := db.Query("SELECT id, chat_id, label FROM reminders WHERE reminder_time <= ? AND notified = 0", now)
		if err != nil {
			log.Printf("DB query error: %v", err)
			continue
		}

		for rows.Next() {
			var id int64
			var chatID int64
			var label string
			err = rows.Scan(&id, &chatID, &label)
			if err != nil {
				log.Printf("Row scan error: %v", err)
				continue
			}

			log.Printf("handled reminder id=%v, chatId=%v, label=%s", id, chatID, label)

			// Send the reminder message to the user.
			msg := tgbotapi.NewMessage(chatID, label)
			_, err = bot.Send(msg)
			if err != nil {
				log.Printf("Error sending reminder: %v", err)
				continue
			} else {
				log.Printf("Reminder sent: [%v] %s", chatID, label)
			}

			// Mark the reminder as notified to avoid sending it again.
			result, err := db.Exec("UPDATE reminders SET notified = 1 WHERE id = ?", id)
			if err != nil {
				log.Printf("Error updating reminder status: %v", err)
			} else {
				rowsAffected, err := result.RowsAffected()
				log.Printf("Updated reminder: [%v] %s, rows affected=%v (%s)", id, label, rowsAffected, err)
			}
		}
		err = rows.Close()
		if err != nil {
			log.Printf("checkReminders Error closing rows: %v", err)
		}
	}
}
