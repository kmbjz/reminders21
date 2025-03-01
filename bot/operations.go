package bot

import (
	"fmt"
	"strconv"
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
		case "adjust":
			b.processAdjustOperation(op, msg)
		case "delete":
			b.processDeleteOperation(op, msg)
		case "show_list":
			b.processShowListOperation(op, msg)
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

// processAdjustOperation processes adjust operation
func (b *ReminderBot) processAdjustOperation(op llm.Operation, msg *tgbotapi.Message) {
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
