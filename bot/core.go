package bot

import (
	"fmt"
	"log"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"reminders21/config"
	"reminders21/llm"
	"reminders21/speech"
	"reminders21/storage"
)

// NewReminderBot creates a new ReminderBot
func NewReminderBot(cfg *config.Config, logger *log.Logger) (*ReminderBot, error) {
	// Initialize bot API
	bot, err := tgbotapi.NewBotAPI(cfg.TelegramToken)
	if err != nil {
		return nil, fmt.Errorf("failed to create bot: %w", err)
	}

	bot.Debug = cfg.Debug

	// Initialize database
	repo, err := storage.NewReminderRepository(cfg.DatabasePath, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize repository: %w", err)
	}

	// Initialize OpenAI client
	llmClient := llm.NewOpenAIClient(cfg.OpenAIAPIKey, cfg.APITimeout)

	// Initialize transcriber
	transcriber := speech.NewTranscriber(cfg.OpenAIAPIKey, cfg.APITimeout)

	return &ReminderBot{
		config:      cfg,
		bot:         bot,
		repo:        repo,
		llmClient:   llmClient,
		transcriber: transcriber,
		logger:      logger,
		stopChan:    make(chan struct{}),
	}, nil
}

// Start starts the bot
func (b *ReminderBot) Start() error {
	b.logger.Printf("Starting bot @%s", b.bot.Self.UserName)

	// Start reminder checker
	go b.checkReminders()

	// Start update listener
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := b.bot.GetUpdatesChan(u)

	for update := range updates {
		select {
		case <-b.stopChan:
			return nil
		default:
			go b.processUpdate(update)
		}
	}

	return nil
}

// Stop stops the bot
func (b *ReminderBot) Stop() {
	close(b.stopChan)
	b.logger.Println("Bot stopped")
}

// processUpdate processes a single update
func (b *ReminderBot) processUpdate(update tgbotapi.Update) {
	if update.Message != nil {
		if update.Message.IsCommand() {
			b.handleCommand(update.Message)
		} else if update.Message.Voice != nil {
			b.handleVoiceMessage(update.Message)
		} else if update.Message.Video != nil {
			b.handleVideoMessage(update.Message)
		} else {
			b.handleTextMessage(update.Message)
		}
	} else if update.EditedMessage != nil {
		b.handleEditedMessage(update.EditedMessage)
	}
}

// checkReminders periodically checks for due reminders
func (b *ReminderBot) checkReminders() {
	b.logger.Println("Starting reminder checker...")
	ticker := time.NewTicker(b.config.ReminderCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-b.stopChan:
			return
		case <-ticker.C:
			b.processDueReminders()
		}
	}
}

// processDueReminders processes due reminders
func (b *ReminderBot) processDueReminders() {
	now := time.Now()
	reminders, err := b.repo.GetDueReminders(now)
	if err != nil {
		b.logger.Printf("Error getting due reminders: %v", err)
		return
	}

	if len(reminders) == 0 {
		return
	}

	var reminderIDs []int64

	for _, r := range reminders {
		msg := tgbotapi.NewMessage(r.ChatID, r.Label)
		if _, err := b.bot.Send(msg); err != nil {
			b.logger.Printf("Error sending reminder: %v", err)
			continue
		}

		b.logger.Printf("Sent reminder: ID=%d, chat=%d, label=%s", r.ID, r.ChatID, r.Label)
		reminderIDs = append(reminderIDs, r.ID)
	}

	// Mark reminders as notified in a single transaction
	if err := b.repo.MarkMultipleAsNotified(reminderIDs); err != nil {
		b.logger.Printf("Error marking reminders as notified: %v", err)
	}
}

// getUserRemindersAsMap gets user reminders as map for LLM
func (b *ReminderBot) getUserRemindersAsMap(userID int64) ([]map[string]string, error) {
	reminders, err := b.repo.GetUserReminders(userID)
	if err != nil {
		return nil, err
	}

	var result []map[string]string
	for _, r := range reminders {
		reminder := map[string]string{
			"reminder_id": fmt.Sprintf("%d", r.ID),
			"datetime":    r.ReminderTime.Format("2006-01-02 15:04:05"),
			"label":       r.Label,
		}
		result = append(result, reminder)
	}

	return result, nil
}
