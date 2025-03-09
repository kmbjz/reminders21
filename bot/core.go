package bot

import (
	"fmt"
	"log"
	"strconv"
	"strings"
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

	// Register bot commands
	err := b.registerBotCommands()
	if err != nil {
		b.logger.Printf("Warning: Failed to register bot commands: %v", err)
		// Continue anyway since this is not critical
	}

	// Start reminder checker
	go b.checkReminders()

	// Start recurring reminder checker
	go b.startRecurringChecker()

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

// registerBotCommands registers the available commands for the bot
func (b *ReminderBot) registerBotCommands() error {
	commands := []tgbotapi.BotCommand{
		{Command: "start", Description: "Начать работу с ботом"},
		{Command: "list", Description: "Показать все активные напоминания"},
		{Command: "recurring", Description: "Показать регулярные напоминания"},
		{Command: "today", Description: "Показать напоминания на сегодня"},
		{Command: "tomorrow", Description: "Показать напоминания на завтра"},
		{Command: "timezone", Description: "Установить часовой пояс"},
		{Command: "help", Description: "Показать справку по использованию бота"},
	}

	cfg := tgbotapi.NewSetMyCommands(commands...)
	_, err := b.bot.Request(cfg)
	return err
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
	} else if update.CallbackQuery != nil {
		b.handleCallbackQuery(update.CallbackQuery)
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

// handleCallbackQuery handles callback queries from inline keyboards
func (b *ReminderBot) handleCallbackQuery(query *tgbotapi.CallbackQuery) {
	// Extract action and parameters
	callback := query.Data

	// Acknowledge the callback to stop the loading indicator
	callback_resp := tgbotapi.NewCallback(query.ID, "")
	b.bot.Request(callback_resp)

	if strings.HasPrefix(callback, "delete_rec_") {
		// Extract recurring reminder ID from callback data
		reminderIDStr := strings.TrimPrefix(callback, "delete_rec_")
		reminderID, err := strconv.ParseInt(reminderIDStr, 10, 64)
		if err != nil {
			b.logger.Printf("Error parsing recurring reminder ID from callback: %v", err)
			return
		}

		// Delete recurring reminder
		deleted, err := b.repo.DeleteRecurringReminder(reminderID, query.From.ID)
		if err != nil {
			b.logger.Printf("Error deleting recurring reminder: %v", err)
			return
		}

		if !deleted {
			notification := tgbotapi.NewMessage(query.Message.Chat.ID, "Регулярное напоминание не найдено или не принадлежит вам.")
			b.bot.Send(notification)
			return
		}

		// Delete the message where the button was clicked
		deleteMsg := tgbotapi.NewDeleteMessage(query.Message.Chat.ID, query.Message.MessageID)
		_, err = b.bot.Request(deleteMsg)
		if err != nil {
			b.logger.Printf("Error deleting message: %v", err)
			// If we can't delete the message, at least send a confirmation
			notification := tgbotapi.NewMessage(query.Message.Chat.ID, "✅ Регулярное напоминание удалено.")
			b.bot.Send(notification)
		}

		b.logger.Printf("Deleted recurring reminder via inline button: ID=%d (user %d)",
			reminderID, query.From.ID)
	} else if strings.HasPrefix(callback, "delete_") {
		// Extract regular reminder ID from callback data
		reminderIDStr := strings.TrimPrefix(callback, "delete_")
		reminderID, err := strconv.ParseInt(reminderIDStr, 10, 64)
		if err != nil {
			b.logger.Printf("Error parsing reminder ID from callback: %v", err)
			return
		}

		// Delete the reminder
		deleted, err := b.repo.DeleteReminder(reminderID, query.From.ID)
		if err != nil {
			b.logger.Printf("Error deleting reminder: %v", err)
			return
		}

		if !deleted {
			notification := tgbotapi.NewMessage(query.Message.Chat.ID, "Напоминание не найдено или не принадлежит вам.")
			b.bot.Send(notification)
			return
		}

		// Delete the message where the button was clicked
		deleteMsg := tgbotapi.NewDeleteMessage(query.Message.Chat.ID, query.Message.MessageID)
		_, err = b.bot.Request(deleteMsg)
		if err != nil {
			b.logger.Printf("Error deleting message: %v", err)
			// If we can't delete the message, at least send a confirmation
			notification := tgbotapi.NewMessage(query.Message.Chat.ID, "✅ Напоминание удалено.")
			b.bot.Send(notification)
		}

		b.logger.Printf("Deleted reminder via inline button: ID=%d (user %d)",
			reminderID, query.From.ID)
	}
}
