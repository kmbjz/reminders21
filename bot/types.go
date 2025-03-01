package bot

import (
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"log"
	"reminders21/config"
	"reminders21/llm"
	"reminders21/speech"
	"reminders21/storage"
)

// The LLM prompt template
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

// ReminderBot represents the Telegram bot
type ReminderBot struct {
	config      *config.Config
	bot         *tgbotapi.BotAPI
	repo        *storage.ReminderRepository
	llmClient   *llm.OpenAIClient
	transcriber *speech.Transcriber
	logger      *log.Logger
	stopChan    chan struct{}
}
