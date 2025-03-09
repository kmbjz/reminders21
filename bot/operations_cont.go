package bot

import (
	"fmt"
	"reminders21/storage"
	"sort"
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
// processShowListOperation processes show_list operation
func (b *ReminderBot) processShowListOperation(op llm.Operation, msg *tgbotapi.Message) {
	var reminders []storage.ReminderItem
	var err error
	var title string

	var start, end time.Time
	if op.StartDate != "" {
		// Show reminders for a specific period
		start, err = time.Parse("2006-01-02", op.StartDate)
		if err != nil {
			b.logger.Printf("Error parsing start date: %v", err)
			reply := tgbotapi.NewMessage(msg.Chat.ID, "Неверный формат даты начала.")
			b.bot.Send(reply)
			return
		}

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

	// Get applicable recurring reminders if showing a specific day or period
	var recurringEvents []RecurringEvent
	if op.StartDate != "" {
		recurringEvents, err = b.getApplicableRecurringReminders(msg.From.ID, start, end)
		if err != nil {
			b.logger.Printf("Error getting recurring reminders: %v", err)
			// Continue with the regular reminders we already have
		}
	}

	if len(reminders) == 0 && len(recurringEvents) == 0 {
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

		// Add recurring reminders
		for _, r := range recurringEvents {
			lines = append(lines, fmt.Sprintf("%s – %s (регулярное)", r.Time, r.Label))
		}

		// Sort lines by time
		sortLinesByTime(lines)
	} else {
		// Format with date and time
		for _, r := range reminders {
			lines = append(lines, fmt.Sprintf("%s – %s", r.ReminderTime.Format("02.01.2006 15:04"), r.Label))
		}

		// Add recurring reminders with date
		for _, r := range recurringEvents {
			lines = append(lines, fmt.Sprintf("%s %s – %s (регулярное)",
				r.Date.Format("02.01.2006"), r.Time, r.Label))
		}

		// Sort lines by date and time
		sortLinesByDateTime(lines)
	}

	text := title + ":\n" + strings.Join(lines, "\n")
	reply := tgbotapi.NewMessage(msg.Chat.ID, text)
	b.bot.Send(reply)
}

// RecurringEvent represents a recurring reminder occurrence on a specific date
type RecurringEvent struct {
	ID    int64
	Label string
	Time  string
	Date  time.Time
}

// getApplicableRecurringReminders retrieves recurring reminders applicable within a date range
func (b *ReminderBot) getApplicableRecurringReminders(userID int64, start, end time.Time) ([]RecurringEvent, error) {
	recurringReminders, err := b.repo.GetUserRecurringReminders(userID)
	if err != nil {
		return nil, err
	}

	var events []RecurringEvent
	for currentDate := start; currentDate.Before(end); currentDate = currentDate.Add(24 * time.Hour) {
		dayOfWeek := int(currentDate.Weekday())
		dayOfMonth := currentDate.Day()

		for _, reminder := range recurringReminders {
			applicable := false

			switch reminder.RecurringType {
			case storage.RecurringDaily:
				applicable = true
			case storage.RecurringWeekly:
				applicable = reminder.DayOfWeek == dayOfWeek
			case storage.RecurringMonthly:
				applicable = reminder.DayOfMonth == dayOfMonth
			}

			if applicable {
				events = append(events, RecurringEvent{
					ID:    reminder.ID,
					Label: reminder.Label,
					Time:  reminder.Time,
					Date:  currentDate,
				})
			}
		}
	}

	return events, nil
}

// sortLinesByTime sorts reminder lines by time
func sortLinesByTime(lines []string) {
	sort.Slice(lines, func(i, j int) bool {
		timeI := extractTimeFromLine(lines[i])
		timeJ := extractTimeFromLine(lines[j])
		return timeI < timeJ
	})
}

// sortLinesByDateTime sorts reminder lines by date and time
func sortLinesByDateTime(lines []string) {
	sort.Slice(lines, func(i, j int) bool {
		dateTimeI := extractDateTimeFromLine(lines[i])
		dateTimeJ := extractDateTimeFromLine(lines[j])
		return dateTimeI < dateTimeJ
	})
}

// extractTimeFromLine extracts time from a line in format "15:04 – Label"
func extractTimeFromLine(line string) string {
	parts := strings.Split(line, " – ")
	if len(parts) < 2 {
		return ""
	}
	return parts[0]
}

// extractDateTimeFromLine extracts date and time from a line in format "02.01.2006 15:04 – Label"
func extractDateTimeFromLine(line string) string {
	parts := strings.Split(line, " – ")
	if len(parts) < 2 {
		return ""
	}
	return parts[0]
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
