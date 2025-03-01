package bot

import (
	"fmt"
	"reminders21/storage"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"reminders21/llm"
	"reminders21/utils"
)

//// processDeleteOperation processes delete operation
//func (b *ReminderBot) processDeleteOperation(op llm.Operation, msg *tgbotapi.Message) {
//	reminderID, err := strconv.ParseInt(op.ReminderID, 10, 64)
//	if err != nil {
//		b.logger.Printf("Error parsing reminder ID: %v", err)
//		reply := tgbotapi.NewMessage(msg.Chat.ID, "Неверный формат ID напоминания.")
//		b.bot.Send(reply)
//		return
//	}
//
//	deleted, err := b.repo.DeleteReminder(reminderID, msg.From.ID)
//	if err != nil {
//		b.logger.Printf("Error deleting reminder: %v", err)
//		reply := tgbotapi.NewMessage(msg.Chat.ID, "Ошибка при удалении напоминания.")
//		b.bot.Send(reply)
//		return
//	}
//
//	if !deleted {
//		reply := tgbotapi.NewMessage(msg.Chat.ID, "Напоминание не найдено или не принадлежит вам.")
//		b.bot.Send(reply)
//		return
//	}
//
//	b.logger.Printf("Deleted reminder: ID=%s (chat %d)", op.ReminderID, msg.Chat.ID)
//
//	reply := tgbotapi.NewMessage(msg.Chat.ID, op.Answer)
//	b.bot.Send(reply)
//}

// processShowListOperation processes show_list operation
func (b *ReminderBot) processShowListOperation(op llm.Operation, msg *tgbotapi.Message) {
	var reminders []storage.ReminderItem
	var err error
	var title string

	if op.StartDate != "" {
		// Show reminders for a specific period
		start, err := time.Parse("2006-01-02", op.StartDate)
		if err != nil {
			b.logger.Printf("Error parsing start date: %v", err)
			reply := tgbotapi.NewMessage(msg.Chat.ID, "Неверный формат даты начала.")
			b.bot.Send(reply)
			return
		}

		var end time.Time
		if op.EndDate != "" {
			// Period with end date
			endParsed, err := time.Parse("2006-01-02", op.EndDate)
			if err != nil {
				b.logger.Printf("Error parsing end date: %v", err)
				reply := tgbotapi.NewMessage(msg.Chat.ID, "Неверный формат даты конца.")
				b.bot.Send(reply)
				return
			}
			end = endParsed.Add(24 * time.Hour)

			if op.EndDate != op.StartDate {
				title = fmt.Sprintf("Список с %s по %s", start.Format("02.01.2006"), endParsed.Format("02.01.2006"))
			} else {
				title = formatDayTitle(start)
			}
		} else {
			// Single day
			end = start.Add(24 * time.Hour)
			title = formatDayTitle(start)
		}

		reminders, err = b.repo.GetUserRemindersByPeriod(msg.From.ID, start, end)
	} else {
		// Show all reminders
		reminders, err = b.repo.GetUserReminders(msg.From.ID)
		title = "Все активные напоминания"
	}

	if err != nil {
		b.logger.Printf("Error getting reminders: %v", err)
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Ошибка при получении списка напоминаний.")
		b.bot.Send(reply)
		return
	}

	if len(reminders) == 0 {
		reply := tgbotapi.NewMessage(msg.Chat.ID, title+": пока нет напоминаний.")
		b.bot.Send(reply)
		return
	}

	var lines []string

	if op.StartDate != "" && op.EndDate == "" {
		// Format for single day (only time)
		for _, r := range reminders {
			lines = append(lines, fmt.Sprintf("%s – %s", r.ReminderTime.Format("15:04"), r.Label))
		}
	} else {
		// Format with date and time
		for _, r := range reminders {
			lines = append(lines, fmt.Sprintf("%s – %s", r.ReminderTime.Format("02.01.2006 15:04"), r.Label))
		}
	}

	text := title + ":\n" + strings.Join(lines, "\n")
	reply := tgbotapi.NewMessage(msg.Chat.ID, text)
	b.bot.Send(reply)
}

// formatDayTitle formats a title for day list
func formatDayTitle(date time.Time) string {
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	tomorrow := today.Add(24 * time.Hour)

	if date.Equal(today) {
		return "Напоминания на сегодня"
	} else if date.Equal(tomorrow) {
		return "Напоминания на завтра"
	} else {
		weekdayName := utils.WeekdayToRussian(date.Weekday())
		return fmt.Sprintf("Напоминания на %s, %s", weekdayName, date.Format("02.01.2006"))
	}
}
