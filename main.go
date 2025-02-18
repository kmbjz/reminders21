package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/joho/godotenv"
	_ "github.com/mattn/go-sqlite3"
)

// Updated llmPrompt with new instructions and two placeholders:
// 1. Current datetime
// 2. JSON array of user reminders
const llmPrompt = `
Analyze the following user input. The user might be requesting to create, adjust, or delete a reminder.
If the request is to create a reminder, extract:
• requested reminder's date and time ("datetime" field, format "2006-01-02 15:04:05"). If the user didn't provide a date then use today's date. The user is allowed to provide relative date or time, for example "today", "tomorrow", "in 10 minutes", "after 1 hour", etc. – in this case calculate datetime based on current datetime: %s.
• label for reminder ("label" field).
• action should be "create".
• generate an answer to the user about the accepted reminder in Russian.
If the request is to adjust an existing reminder, extract:
• reminder_id of the reminder to adjust (choose the most relevant from the provided user reminders).
• new datetime (optional) and/or new label (optional) if provided.
• action should be "adjust".
• generate a confirmation answer in Russian.
If the request is to delete an existing reminder, extract:
• reminder_id of the reminder to delete (choose the most relevant from the provided user reminders).
• action should be "delete".
• generate a confirmation answer in Russian.
Include the provided user reminders in the output JSON under "user_reminders", which is a JSON array of objects each having "reminder_id", "datetime", and "label". The provided user reminders are: %s
Output Requirements:
• Output must be in valid JSON with UTF-8 encoded strings.
• The JSON structure must be:
{"action": "create|adjust|delete", "datetime": "2006-01-02 15:04:05", "label": "string", "reminder_id": "string", "answer": "string", "user_reminders": [ {"reminder_id": "string", "datetime": "2006-01-02 15:04:05", "label": "string"}, ... ]}
• If you cannot generate any field, leave it empty in the JSON.
• Escape double quotes " by prefixing them with a backslash \.
• Do not escape other characters.
• Do not add extra formatting, line breaks, reasoning or any additional text outside the JSON structure.
`

// LLMOutput represents the expected JSON structure returned by the LLM.
type LLMOutput struct {
	Action        string              `json:"action"`
	Datetime      string              `json:"datetime"`
	Label         string              `json:"label"`
	ReminderID    string              `json:"reminder_id"`
	Answer        string              `json:"answer"`
	UserReminders []map[string]string `json:"user_reminders"`
}

// Types for function calling responses
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ChatMessage struct {
	Role         string        `json:"role"`
	Content      string        `json:"content"`
	FunctionCall *FunctionCall `json:"function_call,omitempty"`
}

type ChatChoice struct {
	Message ChatMessage `json:"message"`
}

type OpenAIChatResponse struct {
	Choices []ChatChoice `json:"choices"`
}

// Bot encapsulates the Telegram bot, database, logger, and a mutex for serializing DB access.
type Bot struct {
	db     *sql.DB
	bot    *tgbotapi.BotAPI
	dbLock sync.Mutex
	logger *log.Logger
}

// NewBot returns an instance of Bot.
func NewBot(db *sql.DB, bot *tgbotapi.BotAPI, logger *log.Logger) *Bot {
	return &Bot{
		db:     db,
		bot:    bot,
		logger: logger,
	}
}

// initDB opens the SQLite database, sets busy timeout/WAL mode, limits connections,
// and creates the reminders table if it doesn't exist.
func initDB(path string, logger *log.Logger) (*sql.DB, error) {
	connStr := fmt.Sprintf("file:%s?_busy_timeout=5000&_journal_mode=WAL", path)
	db, err := sql.Open("sqlite3", connStr)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)

	createTableSQL := `
	CREATE TABLE IF NOT EXISTS reminders (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		chat_id INTEGER,
		user_id INTEGER,
		reminder_time DATETIME,
		label TEXT,
		notified INTEGER DEFAULT 0
	);`
	if _, err = db.Exec(createTableSQL); err != nil {
		return nil, err
	}
	logger.Println("Database initialized.")
	return db, nil
}

// getUserReminders retrieves all active (notified=0) reminders for a given user.
func (b *Bot) getUserReminders(userID int64) ([]map[string]string, error) {
	b.dbLock.Lock()
	defer b.dbLock.Unlock()

	rows, err := b.db.Query("SELECT id, reminder_time, label FROM reminders WHERE user_id = ? AND notified = 0 ORDER BY reminder_time", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var reminders []map[string]string
	for rows.Next() {
		var id int
		var rTime time.Time
		var label string
		if err := rows.Scan(&id, &rTime, &label); err != nil {
			continue
		}
		reminder := map[string]string{
			"reminder_id": fmt.Sprintf("%d", id),
			"datetime":    rTime.Format("2006-01-02 15:04:05"),
			"label":       label,
		}
		reminders = append(reminders, reminder)
	}
	return reminders, nil
}

// parseMessageWithLLM now accepts the userID, gathers that user's active reminders,
// builds an updated prompt (including function definitions for adjust and delete),
// and calls OpenAI. It then processes both plain JSON output and potential function_call.
func (b *Bot) parseMessageWithLLM(input string, userID int64) (LLMOutput, error) {
	var result LLMOutput

	// Get user reminders and marshal to JSON string
	userReminders, err := b.getUserReminders(userID)
	if err != nil {
		return result, err
	}
	remindersJSONBytes, err := json.Marshal(userReminders)
	if err != nil {
		return result, err
	}
	remindersJSON := string(remindersJSONBytes)

	// Build prompt by injecting current time and user reminders into the prompt template
	prompt := fmt.Sprintf(llmPrompt, time.Now().Format("2006-01-02 15:04:05"), remindersJSON)

	// Define function calling definitions for adjust and delete actions
	functions := []map[string]interface{}{
		{
			"name":        "adjust_reminder",
			"description": "Adjust an existing reminder: change label and/or reschedule.",
			"parameters": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"reminder_id": map[string]interface{}{
						"type":        "string",
						"description": "The ID of the reminder to adjust",
					},
					"datetime": map[string]interface{}{
						"type":        "string",
						"description": "New date and time in format '2006-01-02 15:04:05' (optional)",
					},
					"label": map[string]interface{}{
						"type":        "string",
						"description": "New label for the reminder (optional)",
					},
				},
				"required": []string{"reminder_id"},
			},
		},
		{
			"name":        "delete_reminder",
			"description": "Delete an existing reminder.",
			"parameters": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"reminder_id": map[string]interface{}{
						"type":        "string",
						"description": "The ID of the reminder to delete",
					},
				},
				"required": []string{"reminder_id"},
			},
		},
	}

	reqBodyMap := map[string]interface{}{
		"model": "gpt-4o",
		"messages": []map[string]string{
			{"role": "developer", "content": prompt},
			{"role": "user", "content": input},
		},
		"functions":     functions,
		"function_call": "auto",
	}
	reqBody, err := json.Marshal(reqBodyMap)
	if err != nil {
		return result, err
	}

	openaiAPIKey := os.Getenv("OPENAI_API_KEY")
	if openaiAPIKey == "" {
		return result, fmt.Errorf("OPENAI_API_KEY is not set in environment")
	}

	req, err := http.NewRequest("POST", "https://api.openai.com/v1/chat/completions", bytes.NewBuffer(reqBody))
	if err != nil {
		return result, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+openaiAPIKey)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return result, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return result, err
	}

	var openaiResp OpenAIChatResponse
	if err = json.Unmarshal(body, &openaiResp); err != nil {
		return result, err
	}
	if len(openaiResp.Choices) == 0 {
		return result, fmt.Errorf("no choices returned from OpenAI")
	}

	choice := openaiResp.Choices[0].Message
	// If a function_call is returned, use it to set the action and parameters.
	if choice.FunctionCall != nil {
		fc := choice.FunctionCall
		// Set the action based on the function name.
		if fc.Name == "adjust_reminder" {
			result.Action = "adjust"
		} else if fc.Name == "delete_reminder" {
			result.Action = "delete"
		}
		// Parse the function call arguments.
		if err := json.Unmarshal([]byte(fc.Arguments), &result); err != nil {
			return result, fmt.Errorf("error parsing function call arguments: %v", err)
		}
		// Optionally, if answer is empty, provide a default answer.
		if strings.TrimSpace(result.Answer) == "" {
			if result.Action == "adjust" {
				result.Answer = "Напоминание обновлено."
			} else if result.Action == "delete" {
				result.Answer = "Напоминание удалено."
			}
		}
	} else {
		// Otherwise, parse the plain JSON output from the LLM.
		// Clean and extract JSON from the model's output.
		outputText := strings.Trim(strings.TrimSpace(choice.Content), "\r\n```json")
		startIdx := strings.Index(outputText, "{")
		endIdx := strings.LastIndex(outputText, "}")
		if startIdx == -1 || endIdx == -1 || startIdx >= endIdx {
			return result, fmt.Errorf("failed to extract JSON from model output")
		}
		jsonStr := outputText[startIdx : endIdx+1]
		if err = json.Unmarshal([]byte(jsonStr), &result); err != nil {
			return result, fmt.Errorf("error parsing JSON: %v", err)
		}
	}

	// Basic validation – for create, label and datetime must be provided.
	if result.Action == "create" {
		if strings.TrimSpace(result.Label) == "" {
			return result, fmt.Errorf("label is empty")
		}
		if strings.TrimSpace(result.Datetime) == "" {
			return result, fmt.Errorf("datetime is empty")
		}
	} else if result.Action == "adjust" || result.Action == "delete" {
		if strings.TrimSpace(result.ReminderID) == "" {
			return result, fmt.Errorf("reminder_id is empty for action %s", result.Action)
		}
	}

	return result, nil
}

// handleMessage processes incoming text messages that are not commands.
func (b *Bot) handleMessage(msg *tgbotapi.Message) {
	b.logger.Println("Received text message.")

	// Parse the message via LLM (passing the user ID so we can include their reminders).
	output, err := b.parseMessageWithLLM(msg.Text, msg.From.ID)
	if err != nil {
		b.logger.Printf("LLM parse error: %v", err)
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Ошибка распознования запроса. Попробуй сформулировать по-другому.")
		b.bot.Send(reply)
		return
	}

	switch output.Action {
	case "create":
		// Parse the datetime
		reminderTime, err := time.Parse("2006-01-02 15:04:05", output.Datetime)
		if err != nil {
			b.logger.Printf("error parsing datetime: %v", err)
			reply := tgbotapi.NewMessage(msg.Chat.ID, "Неверный формат даты/времени.")
			b.bot.Send(reply)
			return
		}

		// Insert reminder into DB
		b.dbLock.Lock()
		_, err = b.db.Exec(
			"INSERT INTO reminders (chat_id, user_id, reminder_time, label) VALUES (?, ?, ?, ?)",
			msg.Chat.ID, msg.From.ID, reminderTime, output.Label,
		)
		b.dbLock.Unlock()

		if err != nil {
			b.logger.Printf("DB insert error: %v", err)
			reply := tgbotapi.NewMessage(msg.Chat.ID, "Ошибка сохранения напоминания.")
			b.bot.Send(reply)
			return
		}
		b.logger.Printf("Accepted reminder: '%s' at %s for chat ID %d", output.Label, reminderTime.Format("2006-01-02 15:04:05"), msg.Chat.ID)
		reply := tgbotapi.NewMessage(msg.Chat.ID, output.Answer)
		b.bot.Send(reply)
	case "adjust":
		// Adjust an existing reminder.
		// Build the update query based on which fields are provided.
		var (
			query string
			args  []interface{}
		)
		if strings.TrimSpace(output.Datetime) != "" && strings.TrimSpace(output.Label) != "" {
			// Update both fields
			reminderTime, err := time.Parse("2006-01-02 15:04:05", output.Datetime)
			if err != nil {
				b.logger.Printf("error parsing datetime: %v", err)
				reply := tgbotapi.NewMessage(msg.Chat.ID, "Неверный формат даты/времени.")
				b.bot.Send(reply)
				return
			}
			query = "UPDATE reminders SET reminder_time = ?, label = ? WHERE id = ? AND user_id = ?"
			args = []interface{}{reminderTime, output.Label, output.ReminderID, msg.From.ID}
		} else if strings.TrimSpace(output.Datetime) != "" {
			reminderTime, err := time.Parse("2006-01-02 15:04:05", output.Datetime)
			if err != nil {
				b.logger.Printf("error parsing datetime: %v", err)
				reply := tgbotapi.NewMessage(msg.Chat.ID, "Неверный формат даты/времени.")
				b.bot.Send(reply)
				return
			}
			query = "UPDATE reminders SET reminder_time = ? WHERE id = ? AND user_id = ?"
			args = []interface{}{reminderTime, output.ReminderID, msg.From.ID}
		} else if strings.TrimSpace(output.Label) != "" {
			query = "UPDATE reminders SET label = ? WHERE id = ? AND user_id = ?"
			args = []interface{}{output.Label, output.ReminderID, msg.From.ID}
		} else {
			reply := tgbotapi.NewMessage(msg.Chat.ID, "Не указаны новые данные для обновления напоминания.")
			b.bot.Send(reply)
			return
		}

		b.dbLock.Lock()
		res, err := b.db.Exec(query, args...)
		b.dbLock.Unlock()
		if err != nil {
			b.logger.Printf("DB update error: %v", err)
			reply := tgbotapi.NewMessage(msg.Chat.ID, "Ошибка обновления напоминания.")
			b.bot.Send(reply)
			return
		}
		affected, _ := res.RowsAffected()
		if affected == 0 {
			reply := tgbotapi.NewMessage(msg.Chat.ID, "Напоминание не найдено или не принадлежит вам.")
			b.bot.Send(reply)
			return
		}
		b.logger.Printf("Adjusted reminder ID %s for chat ID %d", output.ReminderID, msg.Chat.ID)
		reply := tgbotapi.NewMessage(msg.Chat.ID, output.Answer)
		b.bot.Send(reply)
	case "delete":
		// Delete the specified reminder.
		b.dbLock.Lock()
		res, err := b.db.Exec("DELETE FROM reminders WHERE id = ? AND user_id = ?", output.ReminderID, msg.From.ID)
		b.dbLock.Unlock()
		if err != nil {
			b.logger.Printf("DB delete error: %v", err)
			reply := tgbotapi.NewMessage(msg.Chat.ID, "Ошибка удаления напоминания.")
			b.bot.Send(reply)
			return
		}
		affected, _ := res.RowsAffected()
		if affected == 0 {
			reply := tgbotapi.NewMessage(msg.Chat.ID, "Напоминание не найдено или не принадлежит вам.")
			b.bot.Send(reply)
			return
		}
		b.logger.Printf("Deleted reminder ID %s for chat ID %d", output.ReminderID, msg.Chat.ID)
		reply := tgbotapi.NewMessage(msg.Chat.ID, output.Answer)
		b.bot.Send(reply)
	default:
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Неверное действие в запросе.")
		b.bot.Send(reply)
	}
}

// handleAudioMessage processes incoming voice messages.
func (b *Bot) handleAudioMessage(msg *tgbotapi.Message) {
	b.logger.Println("Received audio message.")

	fileID := msg.Voice.FileID
	fileCfg := tgbotapi.FileConfig{FileID: fileID}
	file, err := b.bot.GetFile(fileCfg)
	if err != nil {
		b.logger.Printf("Error getting file info: %v", err)
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Не удалось получить аудио файл.")
		b.bot.Send(reply)
		return
	}

	fileURL := file.Link(b.bot.Token)
	resp, err := http.Get(fileURL)
	if err != nil {
		b.logger.Printf("Error downloading file: %v", err)
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Не удалось загрузить аудио файл.")
		b.bot.Send(reply)
		return
	}
	defer resp.Body.Close()

	tmpFile, err := os.CreateTemp("", "audio-*.ogg")
	if err != nil {
		b.logger.Printf("Error creating temp file: %v", err)
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Ошибка обработки аудио файла.")
		b.bot.Send(reply)
		return
	}
	defer func() {
		tmpFile.Close()
		os.Remove(tmpFile.Name())
	}()

	if _, err = io.Copy(tmpFile, resp.Body); err != nil {
		b.logger.Printf("Error saving audio file: %v", err)
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Ошибка сохранения аудио файла.")
		b.bot.Send(reply)
		return
	}

	transcription, err := b.transcribeAudio(tmpFile.Name())
	if err != nil {
		b.logger.Printf("Error transcribing audio: %v", err)
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Ошибка распознавания аудио.")
		b.bot.Send(reply)
		return
	}

	output, err := b.parseMessageWithLLM(transcription, msg.From.ID)
	if err != nil {
		b.logger.Printf("LLM parse error after transcription: %v", err)
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Ошибка распознования запроса из аудио. Попробуй сформулировать по-другому.")
		b.bot.Send(reply)
		return
	}

	// Process the action similar to handleMessage.
	switch output.Action {
	case "create":
		reminderTime, err := time.Parse("2006-01-02 15:04:05", output.Datetime)
		if err != nil {
			b.logger.Printf("error parsing datetime: %v", err)
			reply := tgbotapi.NewMessage(msg.Chat.ID, "Неверный формат даты/времени.")
			b.bot.Send(reply)
			return
		}
		b.dbLock.Lock()
		_, err = b.db.Exec(
			"INSERT INTO reminders (chat_id, user_id, reminder_time, label) VALUES (?, ?, ?, ?)",
			msg.Chat.ID, msg.From.ID, reminderTime, output.Label,
		)
		b.dbLock.Unlock()
		if err != nil {
			b.logger.Printf("DB insert error: %v", err)
			reply := tgbotapi.NewMessage(msg.Chat.ID, "Ошибка сохранения напоминания.")
			b.bot.Send(reply)
			return
		}
		b.logger.Printf("Accepted reminder: '%s' at %s for chat ID %d", output.Label, reminderTime.Format("2006-01-02 15:04:05"), msg.Chat.ID)
		reply := tgbotapi.NewMessage(msg.Chat.ID, output.Answer)
		b.bot.Send(reply)
	case "adjust":
		var (
			query string
			args  []interface{}
		)
		if strings.TrimSpace(output.Datetime) != "" && strings.TrimSpace(output.Label) != "" {
			reminderTime, err := time.Parse("2006-01-02 15:04:05", output.Datetime)
			if err != nil {
				b.logger.Printf("error parsing datetime: %v", err)
				reply := tgbotapi.NewMessage(msg.Chat.ID, "Неверный формат даты/времени.")
				b.bot.Send(reply)
				return
			}
			query = "UPDATE reminders SET reminder_time = ?, label = ? WHERE id = ? AND user_id = ?"
			args = []interface{}{reminderTime, output.Label, output.ReminderID, msg.From.ID}
		} else if strings.TrimSpace(output.Datetime) != "" {
			reminderTime, err := time.Parse("2006-01-02 15:04:05", output.Datetime)
			if err != nil {
				b.logger.Printf("error parsing datetime: %v", err)
				reply := tgbotapi.NewMessage(msg.Chat.ID, "Неверный формат даты/времени.")
				b.bot.Send(reply)
				return
			}
			query = "UPDATE reminders SET reminder_time = ? WHERE id = ? AND user_id = ?"
			args = []interface{}{reminderTime, output.ReminderID, msg.From.ID}
		} else if strings.TrimSpace(output.Label) != "" {
			query = "UPDATE reminders SET label = ? WHERE id = ? AND user_id = ?"
			args = []interface{}{output.Label, output.ReminderID, msg.From.ID}
		} else {
			reply := tgbotapi.NewMessage(msg.Chat.ID, "Не указаны новые данные для обновления напоминания.")
			b.bot.Send(reply)
			return
		}
		b.dbLock.Lock()
		res, err := b.db.Exec(query, args...)
		b.dbLock.Unlock()
		if err != nil {
			b.logger.Printf("DB update error: %v", err)
			reply := tgbotapi.NewMessage(msg.Chat.ID, "Ошибка обновления напоминания.")
			b.bot.Send(reply)
			return
		}
		affected, _ := res.RowsAffected()
		if affected == 0 {
			reply := tgbotapi.NewMessage(msg.Chat.ID, "Напоминание не найдено или не принадлежит вам.")
			b.bot.Send(reply)
			return
		}
		b.logger.Printf("Adjusted reminder ID %s for chat ID %d", output.ReminderID, msg.Chat.ID)
		reply := tgbotapi.NewMessage(msg.Chat.ID, output.Answer)
		b.bot.Send(reply)
	case "delete":
		b.dbLock.Lock()
		res, err := b.db.Exec("DELETE FROM reminders WHERE id = ? AND user_id = ?", output.ReminderID, msg.From.ID)
		b.dbLock.Unlock()
		if err != nil {
			b.logger.Printf("DB delete error: %v", err)
			reply := tgbotapi.NewMessage(msg.Chat.ID, "Ошибка удаления напоминания.")
			b.bot.Send(reply)
			return
		}
		affected, _ := res.RowsAffected()
		if affected == 0 {
			reply := tgbotapi.NewMessage(msg.Chat.ID, "Напоминание не найдено или не принадлежит вам.")
			b.bot.Send(reply)
			return
		}
		b.logger.Printf("Deleted reminder ID %s for chat ID %d", output.ReminderID, msg.Chat.ID)
		reply := tgbotapi.NewMessage(msg.Chat.ID, output.Answer)
		b.bot.Send(reply)
	default:
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Неверное действие в запросе.")
		b.bot.Send(reply)
	}
}

// transcribeAudio sends the audio file to OpenAI's Whisper API and returns its transcription.
func (b *Bot) transcribeAudio(filePath string) (string, error) {
	openaiAPIKey := os.Getenv("OPENAI_API_KEY")
	if openaiAPIKey == "" {
		return "", fmt.Errorf("OPENAI_API_KEY is not set in environment")
	}

	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	var requestBody bytes.Buffer
	writer := multipart.NewWriter(&requestBody)
	part, err := writer.CreateFormFile("file", filepath.Base(filePath))
	if err != nil {
		return "", err
	}
	if _, err = io.Copy(part, file); err != nil {
		return "", err
	}
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

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	var whisperResp struct {
		Text string `json:"text"`
	}
	if err = json.Unmarshal(respBody, &whisperResp); err != nil {
		return "", fmt.Errorf("error parsing transcription JSON: %v", err)
	}
	return whisperResp.Text, nil
}

// checkReminders periodically queries the database for due reminders and sends them.
func (b *Bot) checkReminders() {
	b.logger.Println("Starting reminder checker...")
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		now := time.Now()

		b.dbLock.Lock()
		rows, err := b.db.Query("SELECT id, chat_id, label FROM reminders WHERE reminder_time <= ? AND notified = 0", now)
		if err != nil {
			b.logger.Printf("DB query error: %v", err)
			b.dbLock.Unlock()
			continue
		}

		type reminder struct {
			id     int64
			chatID int64
			label  string
		}
		var reminders []reminder
		for rows.Next() {
			var r reminder
			if err = rows.Scan(&r.id, &r.chatID, &r.label); err != nil {
				b.logger.Printf("Row scan error: %v", err)
				continue
			}
			reminders = append(reminders, r)
		}
		rows.Close()
		b.dbLock.Unlock()

		for _, r := range reminders {
			msg := tgbotapi.NewMessage(r.chatID, r.label)
			if _, err = b.bot.Send(msg); err != nil {
				b.logger.Printf("Error sending reminder: %v", err)
				continue
			}
			b.logger.Printf("Reminder sent: chatID=%d, label=%s", r.chatID, r.label)

			b.dbLock.Lock()
			_, err := b.db.Exec("UPDATE reminders SET notified = 1 WHERE id = ?", r.id)
			b.dbLock.Unlock()
			if err != nil {
				b.logger.Printf("Error updating reminder status: %v", err)
			}
		}
	}
}

func main() {
	if err := godotenv.Load(); err != nil {
		log.Printf("Error loading .env file: %v", err)
	}

	logger := log.New(os.Stdout, "[RemindersBot] ", log.LstdFlags)

	db, err := initDB("reminders.db", logger)
	if err != nil {
		logger.Fatalf("Error initializing database: %v", err)
	}
	defer db.Close()

	botToken := os.Getenv("TELEGRAM_BOT_TOKEN")
	if botToken == "" {
		logger.Fatal("TELEGRAM_BOT_TOKEN is not set in environment")
	}

	tgBot, err := tgbotapi.NewBotAPI(botToken)
	if err != nil {
		logger.Fatalf("Error creating Telegram bot: %v", err)
	}
	logger.Printf("Authorized on account %s", tgBot.Self.UserName)

	myBot := NewBot(db, tgBot, logger)

	go myBot.checkReminders()

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := tgBot.GetUpdatesChan(u)

	for update := range updates {
		if update.Message == nil {
			continue
		}
		if update.Message.IsCommand() {
			go myBot.handleCommand(update.Message)
		} else if update.Message.Voice != nil {
			go myBot.handleAudioMessage(update.Message)
		} else {
			go myBot.handleMessage(update.Message)
		}
	}
}

// handleCommand processes Telegram bot commands.
func (b *Bot) handleCommand(msg *tgbotapi.Message) {
	switch msg.Command() {
	case "start":
		welcome := "Привет! Я бот-напоминалка. Отправь мне сообщение с датой, временем и напоминанием, и я напомню тебе в нужное время.\n\n" +
			"Команды:\n" +
			"• /start – Приветствие\n" +
			"• /list – Список будущих напоминаний"
		reply := tgbotapi.NewMessage(msg.Chat.ID, welcome)
		reply.ParseMode = "HTML"
		b.bot.Send(reply)
	case "list":
		now := time.Now()
		b.dbLock.Lock()
		rows, err := b.db.Query("SELECT reminder_time, label FROM reminders WHERE user_id = ? AND reminder_time > ? AND notified = 0 ORDER BY reminder_time", msg.From.ID, now)
		if err != nil {
			b.logger.Printf("DB query error in /list: %v", err)
			b.dbLock.Unlock()
			reply := tgbotapi.NewMessage(msg.Chat.ID, "Ошибка получения списка напоминаний.")
			b.bot.Send(reply)
			return
		}

		var reminders []string
		for rows.Next() {
			var rTime time.Time
			var label string
			if err = rows.Scan(&rTime, &label); err != nil {
				b.logger.Printf("Row scan error in /list: %v", err)
				continue
			}
			reminders = append(reminders, fmt.Sprintf("%s - %s", rTime.Format("02.01.2006 15:04"), label))
		}
		rows.Close()
		b.dbLock.Unlock()

		if len(reminders) == 0 {
			reply := tgbotapi.NewMessage(msg.Chat.ID, "У вас нет будущих напоминаний.")
			b.bot.Send(reply)
		} else {
			title := "🔔 <b>Ваши будущие напоминания:</b>\n\n"
			replyText := title + strings.Join(reminders, "\n")
			reply := tgbotapi.NewMessage(msg.Chat.ID, replyText)
			reply.ParseMode = "HTML"
			b.bot.Send(reply)
		}
	default:
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Неизвестная команда.")
		b.bot.Send(reply)
	}
}
