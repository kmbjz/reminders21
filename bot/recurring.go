package bot

import (
	"fmt"
	"reminders21/storage"
	"reminders21/utils"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// startRecurringChecker starts a goroutine to check for recurring reminders
func (b *ReminderBot) startRecurringChecker() {
	b.logger.Println("Starting recurring reminder checker...")
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-b.stopChan:
			return
		case <-ticker.C:
			b.processRecurringReminders()
		}
	}
}

// processRecurringReminders processes recurring reminders
func (b *ReminderBot) processRecurringReminders() {
	now := time.Now()
	reminders, err := b.repo.GetDueRecurringReminders(now)
	if err != nil {
		b.logger.Printf("Error getting due recurring reminders: %v", err)
		return
	}

	if len(reminders) == 0 {
		return
	}

	for _, r := range reminders {
		var recurringInfo string
		switch r.RecurringType {
		case storage.RecurringDaily:
			recurringInfo = "ежедневно"
		case storage.RecurringWeekly:
			weekday := time.Weekday(r.DayOfWeek)
			weekdayName := utils.WeekdayToRussian(weekday)
			recurringInfo = fmt.Sprintf("еженедельно по %s", weekdayName)
		case storage.RecurringMonthly:
			recurringInfo = fmt.Sprintf("ежемесячно %d числа", r.DayOfMonth)
		}

		message := fmt.Sprintf("%s\n(повторяется %s в %s)", r.Label, recurringInfo, r.Time)
		msg := tgbotapi.NewMessage(r.ChatID, message)
		if _, err := b.bot.Send(msg); err != nil {
			b.logger.Printf("Error sending recurring reminder: %v", err)
			continue
		}

		// Update last triggered time
		if err := b.repo.UpdateRecurringReminderLastTriggered(r.ID, now); err != nil {
			b.logger.Printf("Error updating last triggered time: %v", err)
		}

		b.logger.Printf("Sent recurring reminder: ID=%d, chat=%d, label=%s", r.ID, r.ChatID, r.Label)
	}
}

// addRecurringReminder adds a recurring reminder
func (b *ReminderBot) addRecurringReminder(msg *tgbotapi.Message, label string, recurringType storage.RecurringType, timeStr string, dayOfWeek, dayOfMonth int) {
	id, err := b.repo.AddRecurringReminder(
		msg.Chat.ID,
		msg.From.ID,
		label,
		recurringType,
		timeStr,
		dayOfWeek,
		dayOfMonth,
	)

	if err != nil {
		b.logger.Printf("Error adding recurring reminder: %v", err)
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Ошибка при создании повторяющегося напоминания.")
		b.bot.Send(reply)
		return
	}

	var recurringText string
	switch recurringType {
	case storage.RecurringDaily:
		recurringText = fmt.Sprintf("каждый день в %s", timeStr)
	case storage.RecurringWeekly:
		weekday := time.Weekday(dayOfWeek)
		weekdayName := utils.WeekdayToRussian(weekday)
		recurringText = fmt.Sprintf("каждую %s в %s", weekdayName, timeStr)
	case storage.RecurringMonthly:
		recurringText = fmt.Sprintf("каждое %d число месяца в %s", dayOfMonth, timeStr)
	}

	answer := fmt.Sprintf("Создано повторяющееся напоминание: %s (%s)", label, recurringText)
	reply := tgbotapi.NewMessage(msg.Chat.ID, answer)
	b.bot.Send(reply)

	b.logger.Printf("Created recurring reminder: ID=%d, '%s' recurring=%s (chat %d)",
		id, label, string(recurringType), msg.Chat.ID)
}

// getUserRecurringRemindersAsMap gets user recurring reminders as map for LLM
func (b *ReminderBot) getUserRecurringRemindersAsMap(userID int64) ([]map[string]string, error) {
	reminders, err := b.repo.GetUserRecurringReminders(userID)
	if err != nil {
		return nil, err
	}

	var result []map[string]string
	for _, r := range reminders {
		var recurringText string
		switch r.RecurringType {
		case storage.RecurringDaily:
			recurringText = fmt.Sprintf("daily at %s", r.Time)
		case storage.RecurringWeekly:
			weekday := time.Weekday(r.DayOfWeek)
			recurringText = fmt.Sprintf("weekly on %s at %s", weekday.String(), r.Time)
		case storage.RecurringMonthly:
			recurringText = fmt.Sprintf("monthly on day %d at %s", r.DayOfMonth, r.Time)
		}

		reminder := map[string]string{
			"reminder_id":    fmt.Sprintf("rec_%d", r.ID),
			"recurring_type": string(r.RecurringType),
			"time":           r.Time,
			"day_of_week":    fmt.Sprintf("%d", r.DayOfWeek),
			"day_of_month":   fmt.Sprintf("%d", r.DayOfMonth),
			"label":          r.Label,
			"description":    recurringText,
		}
		result = append(result, reminder)
	}

	return result, nil
}

// processDeleteRecurringOperation processes delete operation
func (b *ReminderBot) processDeleteRecurringOperation(reminderID int64, msg *tgbotapi.Message, answer string) {
	deleted, err := b.repo.DeleteRecurringReminder(reminderID, msg.From.ID)
	if err != nil {
		b.logger.Printf("Error deleting recurring reminder: %v", err)
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Ошибка при удалении повторяющегося напоминания.")
		b.bot.Send(reply)
		return
	}

	if !deleted {
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Повторяющееся напоминание не найдено или не принадлежит вам.")
		b.bot.Send(reply)
		return
	}

	b.logger.Printf("Deleted recurring reminder: ID=%d (chat %d)", reminderID, msg.Chat.ID)

	if answer == "" {
		answer = "Повторяющееся напоминание удалено."
	}
	reply := tgbotapi.NewMessage(msg.Chat.ID, answer)
	b.bot.Send(reply)
}

// processListRecurringOperation processes show recurring list operation
func (b *ReminderBot) processListRecurringOperation(msg *tgbotapi.Message) {
	reminders, err := b.repo.GetUserRecurringReminders(msg.From.ID)
	if err != nil {
		b.logger.Printf("Error getting recurring reminders: %v", err)
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Ошибка при получении списка повторяющихся напоминаний.")
		b.bot.Send(reply)
		return
	}

	if len(reminders) == 0 {
		reply := tgbotapi.NewMessage(msg.Chat.ID, "У вас нет активных повторяющихся напоминаний.")
		b.bot.Send(reply)
		return
	}

	var lines []string
	for _, r := range reminders {
		var recurringInfo string
		switch r.RecurringType {
		case storage.RecurringDaily:
			recurringInfo = fmt.Sprintf("Ежедневно в %s", r.Time)
		case storage.RecurringWeekly:
			weekday := time.Weekday(r.DayOfWeek)
			weekdayName := utils.WeekdayToRussian(weekday)
			recurringInfo = fmt.Sprintf("Еженедельно по %s в %s", weekdayName, r.Time)
		case storage.RecurringMonthly:
			recurringInfo = fmt.Sprintf("Ежемесячно %d числа в %s", r.DayOfMonth, r.Time)
		}

		lines = append(lines, fmt.Sprintf("%s – %s", recurringInfo, r.Label))
	}

	text := "Ваши повторяющиеся напоминания:\n" + strings.Join(lines, "\n")
	reply := tgbotapi.NewMessage(msg.Chat.ID, text)
	b.bot.Send(reply)
}
