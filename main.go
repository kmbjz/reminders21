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
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/joho/godotenv"
	_ "github.com/mattn/go-sqlite3"
)

// Обновлённый llmPrompt с учётом более умеренного стиля, упоминанием текущего времени,
// указаниями о форматах дат/времени, поведения при запросах на период/конкретную дату,
// а также поддержкой нескольких операций в одном запросе.
// Не используй символы трёх обратных кавычек внутри этого промпта.
const llmPrompt = `
Текущее время: %s

Анализируй следующий запрос пользователя. Пользователь может просить создать, изменить или удалить несколько напоминаний в одном запросе, либо показать список своих напоминаний.
Если запрос на создание напоминания, то:
• Извлеки дату и время напоминания ("datetime" в формате "2006-01-02 15:04:05"). Если дата не указана, используй сегодняшнюю. Пользователь может использовать относительные обозначения (например, "сегодня", "завтра", "через 10 минут", "после 1 часа") – рассчитай время на основе текущего времени.
• Извлеки текст напоминания ("label").
• Укажи действие "create".
• Сгенерируй ответ на русском в неформальном, но вежливом стиле, например: "Окей, я запомнил, что [label] в [время]."

Если запрос на изменение напоминания, то:
• Извлеки reminder_id напоминания, которое нужно изменить (выбери самое подходящее из списка).
• Извлеки новые дату/время (необязательно) и/или новый текст ("label") (необязательно).
• Укажи действие "adjust".
• Сгенерируй ответ, например: "Окей, я поменял напоминание."

Если запрос на удаление напоминания, то:
• Извлеки reminder_id напоминания, которое нужно удалить (выбери самое подходящее из списка).
• Укажи действие "delete".
• Сгенерируй ответ, например: "Окей, напоминание удалено."

Если запрос на показ списка напоминаний, то:
• Укажи действие "show_list".
• Если пользователь задал период (например, "скажи дела на сегодня"), включи в ответ поля "start_date" и "end_date" (в формате "2006-01-02"). Если указана только start_date, значит запрос на конкретный день.
• Сгенерируй ответ, например: "Вот твои напоминания."
	
Выходной JSON должен иметь следующую структуру:
{
  "operations": [
    {
      "action": "create|adjust|delete|show_list",
      "datetime": "2006-01-02 15:04:05",
      "label": "string",
      "reminder_id": "string",
      "answer": "string",
      "start_date": "2006-01-02",
      "end_date": "2006-01-02"
    }
  ],
  "user_reminders": [
    {
      "reminder_id": "string",
      "datetime": "2006-01-02 15:04:05",
      "label": "string"
    }
  ]
}

Если в ответе встречается дата, выводи её в человеко-понятном формате "02.01.2006" (например, "29.11.2010").
Если какое-либо поле недоступно, оставь его пустым.
Экранируй двойные кавычки (\") и не добавляй лишнего текста.
`

// Operation представляет отдельную операцию над напоминанием.
type Operation struct {
	Action     string `json:"action"`
	Datetime   string `json:"datetime"`
	Label      string `json:"label"`
	ReminderID string `json:"reminder_id"`
	Answer     string `json:"answer"`
	StartDate  string `json:"start_date"`
	EndDate    string `json:"end_date"`
}

// LLMOutputMulti представляет выходной JSON от LLM, содержащий массив операций и user_reminders.
type LLMOutputMulti struct {
	Operations    []Operation         `json:"operations"`
	UserReminders []map[string]string `json:"user_reminders"`
}

// FunctionCall – описание вызова функции (для function_call).
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ChatMessage, ChatChoice, OpenAIChatResponse – типы для парсинга ответа OpenAI.
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

// Bot инкапсулирует Telegram-бота, базу данных, логгер и мьютекс.
type Bot struct {
	db     *sql.DB
	bot    *tgbotapi.BotAPI
	dbLock sync.Mutex
	logger *log.Logger
}

// NewBot возвращает экземпляр Bot.
func NewBot(db *sql.DB, bot *tgbotapi.BotAPI, logger *log.Logger) *Bot {
	return &Bot{
		db:     db,
		bot:    bot,
		logger: logger,
	}
}

// initDB – инициализация базы данных.
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

// getUserReminders – получение всех активных напоминаний пользователя.
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

// getUserRemindersByPeriod – получение напоминаний в заданном периоде.
func (b *Bot) getUserRemindersByPeriod(userID int64, start, end time.Time) ([]map[string]string, error) {
	b.dbLock.Lock()
	defer b.dbLock.Unlock()
	query := `SELECT id, reminder_time, label 
	          FROM reminders 
			  WHERE user_id = ? 
			    AND notified = 0
				AND reminder_time >= ? 
				AND reminder_time < ?
			  ORDER BY reminder_time`
	rows, err := b.db.Query(query, userID, start, end)
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

// parseMessageWithLLM – формирует промпт, отправляет запрос в OpenAI и возвращает LLMOutputMulti.
func (b *Bot) parseMessageWithLLM(input string, userID int64) (LLMOutputMulti, error) {
	var result LLMOutputMulti
	userReminders, err := b.getUserReminders(userID)
	if err != nil {
		return result, err
	}
	remindersJSON, _ := json.Marshal(userReminders)
	prompt := fmt.Sprintf(llmPrompt, time.Now().Format("2006-01-02 15:04:05")) + "\n" + string(remindersJSON)
	functions := []map[string]interface{}{
		{
			"name":        "adjust_reminder",
			"description": "Изменить существующее напоминание: изменить текст и/или время.",
			"parameters": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"reminder_id": map[string]interface{}{
						"type":        "string",
						"description": "ID напоминания для изменения",
					},
					"datetime": map[string]interface{}{
						"type":        "string",
						"description": "Новая дата и время в формате '2006-01-02 15:04:05' (необязательно)",
					},
					"label": map[string]interface{}{
						"type":        "string",
						"description": "Новый текст напоминания (необязательно)",
					},
				},
				"required": []string{"reminder_id"},
			},
		},
		{
			"name":        "delete_reminder",
			"description": "Удалить существующее напоминание.",
			"parameters": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"reminder_id": map[string]interface{}{
						"type":        "string",
						"description": "ID напоминания для удаления",
					},
				},
				"required": []string{"reminder_id"},
			},
		},
	}
	reqBodyMap := map[string]interface{}{
		"model": "gpt-4",
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
	if choice.FunctionCall != nil {
		fc := choice.FunctionCall
		switch fc.Name {
		case "adjust_reminder":
			// Если функция, то предполагаем, что все операции заданы в массиве.
			// Устанавливаем действие каждого элемента в "adjust"
			// (Модель должна вернуть массив операций)
		case "delete_reminder":
			// Аналогично для delete.
		}
		if err := json.Unmarshal([]byte(fc.Arguments), &result); err != nil {
			return result, fmt.Errorf("error parsing function call arguments: %v", err)
		}
		if len(result.Operations) == 0 {
			// Если операции пусты, устанавливаем дефолтное сообщение для каждого типа.
			// (Эта ветка может быть не нужна, если модель всегда возвращает массив)
		}
	} else {
		outputText := strings.TrimSpace(choice.Content)
		outputText = strings.Trim(outputText, "\r\n```json")
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
	// Простая проверка: для операций create необходимо, чтобы label и datetime были заданы.
	for _, op := range result.Operations {
		if op.Action == "create" {
			if strings.TrimSpace(op.Label) == "" || strings.TrimSpace(op.Datetime) == "" {
				return result, fmt.Errorf("для операции create поля label и datetime обязательны")
			}
		} else if op.Action == "adjust" || op.Action == "delete" {
			if strings.TrimSpace(op.ReminderID) == "" {
				return result, fmt.Errorf("для операции %s поле reminder_id обязательно", op.Action)
			}
		}
	}
	return result, nil
}

// processOperations обрабатывает массив операций, выполняя соответствующие действия.
func (b *Bot) processOperations(ops []Operation, msg *tgbotapi.Message) {
	for _, op := range ops {
		switch op.Action {
		case "create":
			reminderTime, err := time.Parse("2006-01-02 15:04:05", op.Datetime)
			if err != nil {
				reply := tgbotapi.NewMessage(msg.Chat.ID, "Формат даты/времени неверный в операции создания.")
				b.bot.Send(reply)
				continue
			}
			b.dbLock.Lock()
			_, err = b.db.Exec("INSERT INTO reminders (chat_id, user_id, reminder_time, label) VALUES (?, ?, ?, ?)",
				msg.Chat.ID, msg.From.ID, reminderTime, op.Label)
			b.dbLock.Unlock()
			if err != nil {
				reply := tgbotapi.NewMessage(msg.Chat.ID, "Ошибка сохранения напоминания.")
				b.bot.Send(reply)
				continue
			}
			b.logger.Printf("Создано напоминание: '%s' на %s (чат %d)", op.Label, reminderTime.Format("2006-01-02 15:04:05"), msg.Chat.ID)
			reply := tgbotapi.NewMessage(msg.Chat.ID, op.Answer)
			b.bot.Send(reply)
		case "adjust":
			var query string
			var args []interface{}
			hasDate := strings.TrimSpace(op.Datetime) != ""
			hasLabel := strings.TrimSpace(op.Label) != ""
			if hasDate && hasLabel {
				reminderTime, err := time.Parse("2006-01-02 15:04:05", op.Datetime)
				if err != nil {
					reply := tgbotapi.NewMessage(msg.Chat.ID, "Формат даты/времени неверный в операции изменения.")
					b.bot.Send(reply)
					continue
				}
				query = "UPDATE reminders SET reminder_time = ?, label = ? WHERE id = ? AND user_id = ?"
				args = []interface{}{reminderTime, op.Label, op.ReminderID, msg.From.ID}
			} else if hasDate {
				reminderTime, err := time.Parse("2006-01-02 15:04:05", op.Datetime)
				if err != nil {
					reply := tgbotapi.NewMessage(msg.Chat.ID, "Формат даты/времени неверный в операции изменения.")
					b.bot.Send(reply)
					continue
				}
				query = "UPDATE reminders SET reminder_time = ? WHERE id = ? AND user_id = ?"
				args = []interface{}{reminderTime, op.ReminderID, msg.From.ID}
			} else if hasLabel {
				query = "UPDATE reminders SET label = ? WHERE id = ? AND user_id = ?"
				args = []interface{}{op.Label, op.ReminderID, msg.From.ID}
			} else {
				reply := tgbotapi.NewMessage(msg.Chat.ID, "Нет данных для изменения напоминания.")
				b.bot.Send(reply)
				continue
			}
			b.dbLock.Lock()
			res, err := b.db.Exec(query, args...)
			b.dbLock.Unlock()
			if err != nil {
				reply := tgbotapi.NewMessage(msg.Chat.ID, "Ошибка при изменении напоминания.")
				b.bot.Send(reply)
				continue
			}
			n, _ := res.RowsAffected()
			if n == 0 {
				reply := tgbotapi.NewMessage(msg.Chat.ID, "Напоминание не найдено или не принадлежит вам.")
				b.bot.Send(reply)
				continue
			}
			b.logger.Printf("Изменено напоминание ID %s (чат %d)", op.ReminderID, msg.Chat.ID)
			reply := tgbotapi.NewMessage(msg.Chat.ID, op.Answer)
			b.bot.Send(reply)
		case "delete":
			b.dbLock.Lock()
			res, err := b.db.Exec("DELETE FROM reminders WHERE id = ? AND user_id = ?", op.ReminderID, msg.From.ID)
			b.dbLock.Unlock()
			if err != nil {
				reply := tgbotapi.NewMessage(msg.Chat.ID, "Ошибка при удалении напоминания.")
				b.bot.Send(reply)
				continue
			}
			n, _ := res.RowsAffected()
			if n == 0 {
				reply := tgbotapi.NewMessage(msg.Chat.ID, "Напоминание не найдено или не принадлежит вам.")
				b.bot.Send(reply)
				continue
			}
			b.logger.Printf("Удалено напоминание ID %s (чат %d)", op.ReminderID, msg.Chat.ID)
			reply := tgbotapi.NewMessage(msg.Chat.ID, op.Answer)
			b.bot.Send(reply)
		case "show_list":
			var reminders []map[string]string
			if op.StartDate != "" {
				start, err := time.Parse("2006-01-02", op.StartDate)
				if err != nil {
					reply := tgbotapi.NewMessage(msg.Chat.ID, "Неверный формат даты начала в операции вывода списка.")
					b.bot.Send(reply)
					continue
				}
				var end time.Time
				if op.EndDate != "" {
					endParsed, err := time.Parse("2006-01-02", op.EndDate)
					if err != nil {
						reply := tgbotapi.NewMessage(msg.Chat.ID, "Неверный формат даты конца в операции вывода списка.")
						b.bot.Send(reply)
						continue
					}
					end = endParsed.Add(24 * time.Hour)
				} else {
					end = start.Add(24 * time.Hour)
				}
				reminders, err = b.getUserRemindersByPeriod(msg.From.ID, start, end)
				if err != nil {
					reply := tgbotapi.NewMessage(msg.Chat.ID, "Ошибка получения списка напоминаний.")
					b.bot.Send(reply)
					continue
				}
				nowDate := time.Now().Format("2006-01-02")
				tomorrowDate := time.Now().Add(24 * time.Hour).Format("2006-01-02")
				var title string
				if op.EndDate != "" && op.EndDate != op.StartDate {
					title = fmt.Sprintf("Список с %s по %s", start.Format("02.01.2006"), end.Add(-24*time.Hour).Format("02.01.2006"))
				} else {
					if op.StartDate == nowDate {
						title = "Список на сегодня"
					} else if op.StartDate == tomorrowDate {
						title = "Список на завтра"
					} else {
						weekdayName := weekdayToRussian(start.Weekday())
						title = fmt.Sprintf("Список на %s, %s", weekdayName, start.Format("02.01.2006"))
					}
				}
				if len(reminders) == 0 {
					reply := tgbotapi.NewMessage(msg.Chat.ID, title+": пока нет напоминаний.")
					b.bot.Send(reply)
					continue
				}
				var lines []string
				if op.EndDate == "" {
					for _, r := range reminders {
						rt, err := time.Parse("2006-01-02 15:04:05", r["datetime"])
						if err != nil {
							continue
						}
						lines = append(lines, fmt.Sprintf("%s – %s", rt.Format("15:04"), r["label"]))
					}
				} else {
					for _, r := range reminders {
						rt, err := time.Parse("2006-01-02 15:04:05", r["datetime"])
						if err != nil {
							continue
						}
						lines = append(lines, fmt.Sprintf("%s – %s", rt.Format("02.01.2006 15:04"), r["label"]))
					}
				}
				text := title + ":\n" + strings.Join(lines, "\n")
				reply := tgbotapi.NewMessage(msg.Chat.ID, text)
				b.bot.Send(reply)
			} else {
				reminders, _ = b.getUserReminders(msg.From.ID)
				if len(reminders) == 0 {
					reply := tgbotapi.NewMessage(msg.Chat.ID, "Пока нет ни одного напоминания.")
					b.bot.Send(reply)
					continue
				}
				var lines []string
				for _, r := range reminders {
					rt, err := time.Parse("2006-01-02 15:04:05", r["datetime"])
					if err != nil {
						continue
					}
					lines = append(lines, fmt.Sprintf("%s – %s", rt.Format("02.01.2006 15:04"), r["label"]))
				}
				text := "Вот все активные напоминания:\n" + strings.Join(lines, "\n")
				reply := tgbotapi.NewMessage(msg.Chat.ID, text)
				b.bot.Send(reply)
			}
		default:
			reply := tgbotapi.NewMessage(msg.Chat.ID, "Не понял запрос.")
			b.bot.Send(reply)
		}
	}
}

// handleMessage – обработка текстовых сообщений.
func (b *Bot) handleMessage(msg *tgbotapi.Message) {
	b.logger.Println("Получено текстовое сообщение.")
	multi, err := b.parseMessageWithLLM(msg.Text, msg.From.ID)
	if err != nil {
		b.logger.Printf("LLM parse error: %v", err)
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Не смог разобрать запрос. Попробуйте переформулировать.")
		b.bot.Send(reply)
		return
	}
	b.processOperations(multi.Operations, msg)
}

// handleAudioMessage – обработка голосовых сообщений.
func (b *Bot) handleAudioMessage(msg *tgbotapi.Message) {
	b.logger.Println("Получено голосовое сообщение.")
	fileID := msg.Voice.FileID
	fileCfg := tgbotapi.FileConfig{FileID: fileID}
	file, err := b.bot.GetFile(fileCfg)
	if err != nil {
		b.logger.Printf("Ошибка получения аудио файла: %v", err)
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Не удалось получить аудио файл.")
		b.bot.Send(reply)
		return
	}
	fileURL := file.Link(b.bot.Token)
	resp, err := http.Get(fileURL)
	if err != nil {
		b.logger.Printf("Ошибка скачивания аудио: %v", err)
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Не удалось скачать аудио файл.")
		b.bot.Send(reply)
		return
	}
	defer resp.Body.Close()
	tmpFile, err := os.CreateTemp("", "audio-*.ogg")
	if err != nil {
		b.logger.Printf("Ошибка создания временного файла: %v", err)
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Ошибка обработки аудио.")
		b.bot.Send(reply)
		return
	}
	defer func() {
		tmpFile.Close()
		os.Remove(tmpFile.Name())
	}()
	if _, err = io.Copy(tmpFile, resp.Body); err != nil {
		b.logger.Printf("Ошибка сохранения аудио: %v", err)
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Ошибка сохранения аудио файла.")
		b.bot.Send(reply)
		return
	}
	transcription, err := b.transcribeAudio(tmpFile.Name())
	if err != nil {
		b.logger.Printf("Ошибка транскрипции аудио: %v", err)
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Не удалось распознать аудио.")
		b.bot.Send(reply)
		return
	}
	multi, err := b.parseMessageWithLLM(transcription, msg.From.ID)
	if err != nil {
		b.logger.Printf("Ошибка парсинга LLM из аудио: %v", err)
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Не смог разобрать аудио. Попробуйте ещё раз.")
		b.bot.Send(reply)
		return
	}
	b.processOperations(multi.Operations, msg)
}

// handleVideoMessage – обработка видео сообщений: извлечение аудио через ffmpeg.
func (b *Bot) handleVideoMessage(msg *tgbotapi.Message) {
	b.logger.Println("Получено видео сообщение.")
	fileID := msg.Video.FileID
	fileCfg := tgbotapi.FileConfig{FileID: fileID}
	file, err := b.bot.GetFile(fileCfg)
	if err != nil {
		b.logger.Printf("Ошибка получения видео файла: %v", err)
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Не удалось получить видео файл.")
		b.bot.Send(reply)
		return
	}
	fileURL := file.Link(b.bot.Token)
	resp, err := http.Get(fileURL)
	if err != nil {
		b.logger.Printf("Ошибка скачивания видео: %v", err)
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Не удалось скачать видео файл.")
		b.bot.Send(reply)
		return
	}
	defer resp.Body.Close()
	tmpVideo, err := os.CreateTemp("", "video-*.mp4")
	if err != nil {
		b.logger.Printf("Ошибка создания временного видео файла: %v", err)
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Ошибка обработки видео.")
		b.bot.Send(reply)
		return
	}
	defer func() {
		tmpVideo.Close()
		os.Remove(tmpVideo.Name())
	}()
	if _, err = io.Copy(tmpVideo, resp.Body); err != nil {
		b.logger.Printf("Ошибка сохранения видео файла: %v", err)
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Ошибка сохранения видео файла.")
		b.bot.Send(reply)
		return
	}
	tmpAudio, err := os.CreateTemp("", "audio-*.ogg")
	if err != nil {
		b.logger.Printf("Ошибка создания временного аудио файла: %v", err)
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Ошибка обработки аудио из видео.")
		b.bot.Send(reply)
		return
	}
	tmpAudioPath := tmpAudio.Name()
	tmpAudio.Close()
	cmd := exec.Command("ffmpeg", "-i", tmpVideo.Name(), "-vn", "-acodec", "libopus", "-f", "ogg", tmpAudioPath)
	if err := cmd.Run(); err != nil {
		b.logger.Printf("Ошибка ffmpeg: %v", err)
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Не удалось извлечь аудио из видео.")
		b.bot.Send(reply)
		os.Remove(tmpAudioPath)
		return
	}
	defer os.Remove(tmpAudioPath)
	transcription, err := b.transcribeAudio(tmpAudioPath)
	if err != nil {
		b.logger.Printf("Ошибка транскрипции аудио из видео: %v", err)
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Не удалось распознать аудио из видео.")
		b.bot.Send(reply)
		return
	}
	multi, err := b.parseMessageWithLLM(transcription, msg.From.ID)
	if err != nil {
		b.logger.Printf("Ошибка LLM при разборе видео: %v", err)
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Не смог разобрать запрос из видео. Попробуйте ещё раз.")
		b.bot.Send(reply)
		return
	}
	b.processOperations(multi.Operations, msg)
}

// transcribeAudio – отправляет аудиофайл в Whisper API и возвращает распознанный текст.
func (b *Bot) transcribeAudio(filePath string) (string, error) {
	openaiAPIKey := os.Getenv("OPENAI_API_KEY")
	if openaiAPIKey == "" {
		return "", fmt.Errorf("OPENAI_API_KEY не задан в переменных окружения")
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
		return "", fmt.Errorf("error parsing Whisper response JSON: %v", err)
	}
	return whisperResp.Text, nil
}

// checkReminders – периодическая проверка просроченных напоминаний и их отправка.
func (b *Bot) checkReminders() {
	b.logger.Println("Запущена проверка напоминаний...")
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		now := time.Now()
		b.dbLock.Lock()
		rows, err := b.db.Query("SELECT id, chat_id, label FROM reminders WHERE reminder_time <= ? AND notified = 0", now)
		if err != nil {
			b.logger.Printf("Ошибка запроса к БД: %v", err)
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
				b.logger.Printf("Ошибка чтения строки: %v", err)
				continue
			}
			reminders = append(reminders, r)
		}
		rows.Close()
		b.dbLock.Unlock()
		for _, r := range reminders {
			msg := tgbotapi.NewMessage(r.chatID, r.label)
			if _, err = b.bot.Send(msg); err != nil {
				b.logger.Printf("Ошибка отправки напоминания: %v", err)
				continue
			}
			b.logger.Printf("Отправлено напоминание: chatID=%d, label=%s", r.chatID, r.label)
			b.dbLock.Lock()
			_, err := b.db.Exec("UPDATE reminders SET notified = 1 WHERE id = ?", r.id)
			b.dbLock.Unlock()
			if err != nil {
				b.logger.Printf("Ошибка обновления статуса напоминания: %v", err)
			}
		}
	}
}

// weekdayToRussian – утилита для вывода дня недели по-русски.
func weekdayToRussian(w time.Weekday) string {
	switch w {
	case time.Monday:
		return "понедельник"
	case time.Tuesday:
		return "вторник"
	case time.Wednesday:
		return "среда"
	case time.Thursday:
		return "четверг"
	case time.Friday:
		return "пятница"
	case time.Saturday:
		return "суббота"
	case time.Sunday:
		return "воскресенье"
	default:
		return w.String()
	}
}

func main() {
	if err := godotenv.Load(); err != nil {
		log.Printf("Ошибка загрузки .env файла: %v", err)
	}
	logger := log.New(os.Stdout, "[RemindersBot] ", log.LstdFlags)
	db, err := initDB("reminders.db", logger)
	if err != nil {
		logger.Fatalf("Ошибка инициализации БД: %v", err)
	}
	defer db.Close()
	botToken := os.Getenv("TELEGRAM_BOT_TOKEN")
	if botToken == "" {
		logger.Fatal("TELEGRAM_BOT_TOKEN не задан в переменных окружения")
	}
	tgBot, err := tgbotapi.NewBotAPI(botToken)
	if err != nil {
		logger.Fatalf("Ошибка создания Telegram бота: %v", err)
	}
	logger.Printf("Авторизован как %s", tgBot.Self.UserName)
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
		} else if update.Message.Video != nil {
			go myBot.handleVideoMessage(update.Message)
		} else {
			go myBot.handleMessage(update.Message)
		}
	}
}

// handleCommand – обработка команд ("/start", "/list" и т.д.).
func (b *Bot) handleCommand(msg *tgbotapi.Message) {
	switch msg.Command() {
	case "start":
		welcome := "Привет! Я бот-напоминалка. Напиши, что и когда тебе напомнить, и я сохраню напоминание.\n\n" +
			"Доступные команды:\n" +
			"• /start – Приветственное сообщение\n" +
			"• /list – Показать список будущих напоминаний"
		reply := tgbotapi.NewMessage(msg.Chat.ID, welcome)
		reply.ParseMode = "HTML"
		b.bot.Send(reply)
	case "list":
		now := time.Now()
		b.dbLock.Lock()
		rows, err := b.db.Query("SELECT reminder_time, label FROM reminders WHERE user_id = ? AND reminder_time > ? AND notified = 0 ORDER BY reminder_time", msg.From.ID, now)
		if err != nil {
			b.logger.Printf("Ошибка запроса в /list: %v", err)
			b.dbLock.Unlock()
			reply := tgbotapi.NewMessage(msg.Chat.ID, "Ошибка при получении списка напоминаний.")
			b.bot.Send(reply)
			return
		}
		var lines []string
		for rows.Next() {
			var rTime time.Time
			var label string
			if err = rows.Scan(&rTime, &label); err != nil {
				b.logger.Printf("Ошибка чтения строки в /list: %v", err)
				continue
			}
			lines = append(lines, fmt.Sprintf("%s – %s", rTime.Format("02.01.2006 15:04"), label))
		}
		rows.Close()
		b.dbLock.Unlock()
		if len(lines) == 0 {
			reply := tgbotapi.NewMessage(msg.Chat.ID, "Пока нет будущих напоминаний.")
			b.bot.Send(reply)
		} else {
			title := "Вот твои будущие напоминания:\n"
			replyText := title + strings.Join(lines, "\n")
			reply := tgbotapi.NewMessage(msg.Chat.ID, replyText)
			reply.ParseMode = "HTML"
			b.bot.Send(reply)
		}
	default:
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Неизвестная команда.")
		b.bot.Send(reply)
	}
}
