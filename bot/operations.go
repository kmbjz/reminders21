package bot

import (
	"fmt"
	"reminders21/storage"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"reminders21/llm"
)

// processOperations processes operations from LLM
func (b *ReminderBot) processOperations(operations []llm.Operation, msg *tgbotapi.Message) {
	for _, op := range operations {
		switch op.Action {
		case "create":
			b.processCreateOperation(op, msg)
		case "create_recurring":
			b.processCreateRecurringOperation(op, msg)
		case "adjust":
			b.processAdjustOperation(op, msg)
		case "delete":
			b.processDeleteOperation(op, msg)
		case "show_list":
			b.processShowListOperation(op, msg)
		case "show_recurring":
			b.processListRecurringOperation(msg)
		default:
			reply := tgbotapi.NewMessage(msg.Chat.ID, "Неизвестная операция. Попробуйте переформулировать запрос.")
			b.bot.Send(reply)
		}
	}
}

// processCreateOperation processes create operation
func (b *ReminderBot) processCreateOperation(op llm.Operation, msg *tgbotapi.Message) {
	reminderTime, err := time.Parse("2006-01-02 15:04:05", op.Datetime)
	if err != nil {
		b.logger.Printf("Error parsing date/time in create operation: %v", err)
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Неверный формат даты/времени в операции создания.")
		b.bot.Send(reply)
		return
	}

	// Add reminder to database
	id, err := b.repo.AddReminder(msg.Chat.ID, msg.From.ID, reminderTime, op.Label)
	if err != nil {
		b.logger.Printf("Error adding reminder: %v", err)
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Ошибка при создании напоминания.")
		b.bot.Send(reply)
		return
	}

	b.logger.Printf("Created reminder: ID=%d, '%s' at %s (chat %d)", id, op.Label, reminderTime.Format("2006-01-02 15:04:05"), msg.Chat.ID)

	// Format answer with human-readable time
	answer := op.Answer
	if answer == "" {
		answer = fmt.Sprintf("Создано напоминание: %s в %s", op.Label, reminderTime.Format("02.01.2006 15:04"))
	}

	reply := tgbotapi.NewMessage(msg.Chat.ID, answer)
	b.bot.Send(reply)
}

// processCreateRecurringOperation processes create recurring operation
func (b *ReminderBot) processCreateRecurringOperation(op llm.Operation, msg *tgbotapi.Message) {
	// Parse time (should be in format "15:04")
	timeStr := op.Time
	if timeStr == "" {
		// If Time is empty, try to extract time from DateTime
		if op.Datetime != "" {
			datetime, err := time.Parse("2006-01-02 15:04:05", op.Datetime)
			if err == nil {
				timeStr = datetime.Format("15:04")
			}
		}
	}

	if timeStr == "" {
		b.logger.Printf("Missing time in create recurring operation")
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Не указано время для повторяющегося напоминания.")
		b.bot.Send(reply)
		return
	}

	var recurringType storage.RecurringType
	var dayOfWeek, dayOfMonth int

	// Default values
	dayOfWeek = -1
	dayOfMonth = -1

	switch op.RecurringType {
	case "daily":
		recurringType = storage.RecurringDaily
	case "weekly":
		recurringType = storage.RecurringWeekly
		// Parse day of week
		if op.DayOfWeek != "" {
			dow, err := strconv.Atoi(op.DayOfWeek)
			if err == nil && dow >= 0 && dow <= 6 {
				dayOfWeek = dow
			} else {
				// Try to parse day name
				dayOfWeek = parseDayOfWeek(op.DayOfWeek)
			}
		}

		if dayOfWeek < 0 || dayOfWeek > 6 {
			b.logger.Printf("Invalid day of week: %s", op.DayOfWeek)
			reply := tgbotapi.NewMessage(msg.Chat.ID, "Неверный день недели для еженедельного напоминания.")
			b.bot.Send(reply)
			return
		}
	case "monthly":
		recurringType = storage.RecurringMonthly
		// Parse day of month
		if op.DayOfMonth != "" {
			dom, err := strconv.Atoi(op.DayOfMonth)
			if err == nil && dom >= 1 && dom <= 31 {
				dayOfMonth = dom
			}
		}

		if dayOfMonth < 1 || dayOfMonth > 31 {
			b.logger.Printf("Invalid day of month: %s", op.DayOfMonth)
			reply := tgbotapi.NewMessage(msg.Chat.ID, "Неверный день месяца для ежемесячного напоминания.")
			b.bot.Send(reply)
			return
		}
	default:
		b.logger.Printf("Invalid recurring type: %s", op.RecurringType)
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Неверный тип повторения. Используйте 'daily', 'weekly' или 'monthly'.")
		b.bot.Send(reply)
		return
	}

	b.addRecurringReminder(msg, op.Label, recurringType, timeStr, dayOfWeek, dayOfMonth)
}

// parseDayOfWeek parses day of week from Russian or English name
func parseDayOfWeek(day string) int {
	day = strings.ToLower(strings.TrimSpace(day))

	// English
	switch day {
	case "sunday", "sun":
		return 0
	case "monday", "mon":
		return 1
	case "tuesday", "tue":
		return 2
	case "wednesday", "wed":
		return 3
	case "thursday", "thu":
		return 4
	case "friday", "fri":
		return 5
	case "saturday", "sat":
		return 6
	}

	// Russian
	switch day {
	case "воскресенье", "вс":
		return 0
	case "понедельник", "пн":
		return 1
	case "вторник", "вт":
		return 2
	case "среда", "ср":
		return 3
	case "четверг", "чт":
		return 4
	case "пятница", "пт":
		return 5
	case "суббота", "сб":
		return 6
	}

	return -1
}

// processAdjustOperation processes adjust operation
func (b *ReminderBot) processAdjustOperation(op llm.Operation, msg *tgbotapi.Message) {
	// Check if this is a recurring reminder (IDs start with "rec_")
	if strings.HasPrefix(op.ReminderID, "rec_") {
		// Extract the numeric ID
		idStr := strings.TrimPrefix(op.ReminderID, "rec_")
		reminderID, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			b.logger.Printf("Error parsing recurring reminder ID: %v", err)
			reply := tgbotapi.NewMessage(msg.Chat.ID, "Неверный формат ID повторяющегося напоминания.")
			b.bot.Send(reply)
			return
		}

		// Process as recurring reminder adjustment
		b.processAdjustRecurringOperation(reminderID, op, msg)
		return
	}

	// Regular (non-recurring) reminder
	reminderID, err := strconv.ParseInt(op.ReminderID, 10, 64)
	if err != nil {
		b.logger.Printf("Error parsing reminder ID: %v", err)
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Неверный формат ID напоминания.")
		b.bot.Send(reply)
		return
	}

	var updated bool
	hasDate := op.Datetime != ""
	hasLabel := op.Label != ""

	if hasDate && hasLabel {
		// Update both time and label
		reminderTime, err := time.Parse("2006-01-02 15:04:05", op.Datetime)
		if err != nil {
			b.logger.Printf("Error parsing date/time in adjust operation: %v", err)
			reply := tgbotapi.NewMessage(msg.Chat.ID, "Неверный формат даты/времени в операции изменения.")
			b.bot.Send(reply)
			return
		}

		updated, err = b.repo.UpdateReminder(reminderID, msg.From.ID, reminderTime, op.Label)
	} else if hasDate {
		// Update only time
		reminderTime, err := time.Parse("2006-01-02 15:04:05", op.Datetime)
		if err != nil {
			b.logger.Printf("Error parsing date/time in adjust operation: %v", err)
			reply := tgbotapi.NewMessage(msg.Chat.ID, "Неверный формат даты/времени в операции изменения.")
			b.bot.Send(reply)
			return
		}

		updated, err = b.repo.UpdateReminderTime(reminderID, msg.From.ID, reminderTime)
	} else if hasLabel {
		// Update only label
		updated, err = b.repo.UpdateReminderLabel(reminderID, msg.From.ID, op.Label)
	} else {
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Нет данных для изменения напоминания.")
		b.bot.Send(reply)
		return
	}

	if err != nil {
		b.logger.Printf("Error updating reminder: %v", err)
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Ошибка при изменении напоминания.")
		b.bot.Send(reply)
		return
	}

	if !updated {
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Напоминание не найдено или не принадлежит вам.")
		b.bot.Send(reply)
		return
	}

	b.logger.Printf("Updated reminder: ID=%s (chat %d)", op.ReminderID, msg.Chat.ID)

	reply := tgbotapi.NewMessage(msg.Chat.ID, op.Answer)
	b.bot.Send(reply)
}

// processAdjustRecurringOperation adjusts a recurring reminder
func (b *ReminderBot) processAdjustRecurringOperation(reminderID int64, op llm.Operation, msg *tgbotapi.Message) {
	// Get current reminder
	reminders, err := b.repo.GetUserRecurringReminders(msg.From.ID)
	if err != nil {
		b.logger.Printf("Error getting recurring reminders: %v", err)
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Ошибка при получении повторяющихся напоминаний.")
		b.bot.Send(reply)
		return
	}

	// Find the specific reminder
	var foundReminder storage.RecurringReminder
	found := false
	for _, r := range reminders {
		if r.ID == reminderID {
			foundReminder = r
			found = true
			break
		}
	}

	if !found {
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Регулярное напоминание не найдено или не принадлежит вам.")
		b.bot.Send(reply)
		return
	}

	// Update fields if provided
	timeStr := foundReminder.Time
	if op.Time != "" {
		timeStr = op.Time
	}

	label := foundReminder.Label
	if op.Label != "" {
		label = op.Label
	}

	recurringType := foundReminder.RecurringType
	if op.RecurringType != "" {
		switch op.RecurringType {
		case "daily":
			recurringType = storage.RecurringDaily
		case "weekly":
			recurringType = storage.RecurringWeekly
		case "monthly":
			recurringType = storage.RecurringMonthly
		}
	}

	dayOfWeek := foundReminder.DayOfWeek
	if op.DayOfWeek != "" && recurringType == storage.RecurringWeekly {
		dow, err := strconv.Atoi(op.DayOfWeek)
		if err == nil && dow >= 0 && dow <= 6 {
			dayOfWeek = dow
		} else {
			// Try to parse day name
			parsedDow := parseDayOfWeek(op.DayOfWeek)
			if parsedDow >= 0 {
				dayOfWeek = parsedDow
			}
		}
	}

	dayOfMonth := foundReminder.DayOfMonth
	if op.DayOfMonth != "" && recurringType == storage.RecurringMonthly {
		dom, err := strconv.Atoi(op.DayOfMonth)
		if err == nil && dom >= 1 && dom <= 31 {
			dayOfMonth = dom
		}
	}

	// Update the reminder
	updated, err := b.repo.UpdateRecurringReminder(
		reminderID,
		msg.From.ID,
		label,
		recurringType,
		timeStr,
		dayOfWeek,
		dayOfMonth,
	)

	if err != nil {
		b.logger.Printf("Error updating recurring reminder: %v", err)
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Ошибка при изменении повторяющегося напоминания.")
		b.bot.Send(reply)
		return
	}

	if !updated {
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Не удалось изменить регулярное напоминание.")
		b.bot.Send(reply)
		return
	}

	b.logger.Printf("Updated recurring reminder: ID=%d (chat %d)", reminderID, msg.Chat.ID)

	answer := op.Answer
	if answer == "" {
		answer = "Регулярное напоминание изменено."
	}

	reply := tgbotapi.NewMessage(msg.Chat.ID, answer)
	b.bot.Send(reply)
}

// processDeleteOperation processes delete operation
func (b *ReminderBot) processDeleteOperation(op llm.Operation, msg *tgbotapi.Message) {
	// Check if this is a recurring reminder
	if strings.HasPrefix(op.ReminderID, "rec_") {
		// Extract the numeric ID
		idStr := strings.TrimPrefix(op.ReminderID, "rec_")
		reminderID, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			b.logger.Printf("Error parsing recurring reminder ID: %v", err)
			reply := tgbotapi.NewMessage(msg.Chat.ID, "Неверный формат ID повторяющегося напоминания.")
			b.bot.Send(reply)
			return
		}

		// Delete recurring reminder
		b.processDeleteRecurringOperation(reminderID, msg, op.Answer)
		return
	}

	// Regular reminder
	reminderID, err := strconv.ParseInt(op.ReminderID, 10, 64)
	if err != nil {
		b.logger.Printf("Error parsing reminder ID: %v", err)
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Неверный формат ID напоминания.")
		b.bot.Send(reply)
		return
	}

	deleted, err := b.repo.DeleteReminder(reminderID, msg.From.ID)
	if err != nil {
		b.logger.Printf("Error deleting reminder: %v", err)
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Ошибка при удалении напоминания.")
		b.bot.Send(reply)
		return
	}

	if !deleted {
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Напоминание не найдено или не принадлежит вам.")
		b.bot.Send(reply)
		return
	}

	b.logger.Printf("Deleted reminder: ID=%s (chat %d)", op.ReminderID, msg.Chat.ID)

	reply := tgbotapi.NewMessage(msg.Chat.ID, op.Answer)
	b.bot.Send(reply)
}
