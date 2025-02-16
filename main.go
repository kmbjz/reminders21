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

// LLMOutput represents the JSON structure we expect from the LLM.
type LLMOutput struct {
	Datetime string `json:"datetime"`
	Label    string `json:"label"`
	Answer   string `json:"answer"`
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
	// Open SQLite with busy timeout and WAL mode.
	connStr := fmt.Sprintf("file:%s?_busy_timeout=5000&_journal_mode=WAL", path)
	db, err := sql.Open("sqlite3", connStr)
	if err != nil {
		return nil, err
	}
	// Limit to one connection to avoid concurrent writes.
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

// handleMessage processes incoming text messages that are not commands.
func (b *Bot) handleMessage(msg *tgbotapi.Message) {
	b.logger.Println("Received text message.")

	reminderTime, label, answer, err := b.parseMessageWithLLM(msg.Text)
	if err != nil {
		b.logger.Printf("LLM parse error: %v", err)
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Ошибка распознования запроса. Попробуй сформулировать по-другому.")
		b.bot.Send(reply)
		return
	}

	// Insert reminder into DB (using mutex for exclusive access)
	b.dbLock.Lock()
	_, err = b.db.Exec(
		"INSERT INTO reminders (chat_id, user_id, reminder_time, label) VALUES (?, ?, ?, ?)",
		msg.Chat.ID, msg.From.ID, reminderTime, label,
	)
	b.dbLock.Unlock()

	if err != nil {
		b.logger.Printf("DB insert error: %v", err)
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Ошибка сохранения напоминания.")
		b.bot.Send(reply)
		return
	}

	b.logger.Printf("Accepted reminder: '%s' at %s for chat ID %d", label, reminderTime.Format("2006-01-02 15:04:05"), msg.Chat.ID)
	reply := tgbotapi.NewMessage(msg.Chat.ID, answer)
	b.bot.Send(reply)
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

	reminderTime, label, answer, err := b.parseMessageWithLLM(transcription)
	if err != nil {
		b.logger.Printf("LLM parse error after transcription: %v", err)
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Ошибка распознования запроса из аудио. Попробуй сформулировать по-другому.")
		b.bot.Send(reply)
		return
	}

	b.dbLock.Lock()
	_, err = b.db.Exec(
		"INSERT INTO reminders (chat_id, user_id, reminder_time, label) VALUES (?, ?, ?, ?)",
		msg.Chat.ID, msg.From.ID, reminderTime, label,
	)
	b.dbLock.Unlock()
	if err != nil {
		b.logger.Printf("DB insert error: %v", err)
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Ошибка сохранения напоминания.")
		b.bot.Send(reply)
		return
	}

	b.logger.Printf("Accepted reminder (audio): '%s' at %s for chat ID %d", label, reminderTime.Format("2006-01-02 15:04:05"), msg.Chat.ID)
	reply := tgbotapi.NewMessage(msg.Chat.ID, answer)
	b.bot.Send(reply)
}

// handleCommand processes Telegram bot commands.
func (b *Bot) handleCommand(msg *tgbotapi.Message) {
	switch msg.Command() {
	case "start":
		// Welcome message (in Russian) with bullet list for commands.
		welcome := "Привет! Я бот-напоминалка. Отправь мне сообщение с датой, временем и напоминанием, и я напомню тебе в нужное время.\n\n" +
			"Команды:\n" +
			"• /start – Приветствие\n" +
			"• /list – Список будущих напоминаний"
		reply := tgbotapi.NewMessage(msg.Chat.ID, welcome)
		reply.ParseMode = "HTML"
		b.bot.Send(reply)
	case "list":
		// List future reminders for the user.
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
			// Format date and time as dd.mm.yyyy hh:mm
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
	// Add Whisper API model parameter.
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

// parseMessageWithLLM calls the OpenAI API to parse the user input and extract reminder details.
func (b *Bot) parseMessageWithLLM(input string) (time.Time, string, string, error) {
	openaiAPIKey := os.Getenv("OPENAI_API_KEY")
	if openaiAPIKey == "" {
		return time.Time{}, "", "", fmt.Errorf("OPENAI_API_KEY is not set in environment")
	}

	prompt := fmt.Sprintf(llmPrompt, time.Now().Format("2006-01-02 15:04:05"))
	reqBody, err := json.Marshal(map[string]interface{}{
		"model": "gpt-4o",
		"messages": []map[string]string{
			{"role": "developer", "content": prompt},
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

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return time.Time{}, "", "", err
	}

	type ChatChoice struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	type OpenAIChatResponse struct {
		Choices []ChatChoice `json:"choices"`
	}

	var openaiResp OpenAIChatResponse
	if err = json.Unmarshal(body, &openaiResp); err != nil {
		return time.Time{}, "", "", err
	}
	if len(openaiResp.Choices) == 0 {
		return time.Time{}, "", "", fmt.Errorf("no choices returned from OpenAI")
	}

	// Clean and extract the JSON from the model's output.
	outputText := strings.Trim(strings.TrimSpace(openaiResp.Choices[0].Message.Content), "\r\n```json")
	startIdx := strings.Index(outputText, "{")
	endIdx := strings.LastIndex(outputText, "}")
	if startIdx == -1 || endIdx == -1 || startIdx >= endIdx {
		return time.Time{}, "", "", fmt.Errorf("failed to extract JSON from model output")
	}
	jsonStr := outputText[startIdx : endIdx+1]

	var result LLMOutput
	if err = json.Unmarshal([]byte(jsonStr), &result); err != nil {
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

// checkReminders periodically queries the database for due reminders and sends them.
func (b *Bot) checkReminders() {
	b.logger.Println("Starting reminder checker...")
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		now := time.Now()

		// Lock DB access during query.
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
	// Load environment variables.
	if err := godotenv.Load(); err != nil {
		log.Printf("Error loading .env file: %v", err)
	}

	logger := log.New(os.Stdout, "[RemindersBot] ", log.LstdFlags)

	// Initialize database.
	db, err := initDB("reminders.db", logger)
	if err != nil {
		logger.Fatalf("Error initializing database: %v", err)
	}
	defer db.Close()

	// Retrieve Telegram bot token.
	botToken := os.Getenv("TELEGRAM_BOT_TOKEN")
	if botToken == "" {
		logger.Fatal("TELEGRAM_BOT_TOKEN is not set in environment")
	}

	tgBot, err := tgbotapi.NewBotAPI(botToken)
	if err != nil {
		logger.Fatalf("Error creating Telegram bot: %v", err)
	}
	logger.Printf("Authorized on account %s", tgBot.Self.UserName)

	// Create our Bot instance.
	myBot := NewBot(db, tgBot, logger)

	// Start the reminder checker.
	go myBot.checkReminders()

	// Configure Telegram updates.
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := tgBot.GetUpdatesChan(u)

	// Process incoming updates concurrently.
	for update := range updates {
		if update.Message == nil {
			continue
		}

		// If message is a command, handle it.
		if update.Message.IsCommand() {
			go myBot.handleCommand(update.Message)
		} else if update.Message.Voice != nil {
			go myBot.handleAudioMessage(update.Message)
		} else {
			go myBot.handleMessage(update.Message)
		}
	}
}
