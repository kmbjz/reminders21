package bot

import (
	"fmt"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// handleCommand handles bot commands
func (b *ReminderBot) handleCommand(msg *tgbotapi.Message) {
	b.logger.Printf("Received command: %s from %d", msg.Command(), msg.From.ID)

	switch msg.Command() {
	case "start":
		welcome := `Привет! Я бот-напоминалка. Напиши, что и когда тебе напомнить, и я сохраню напоминание.
Доступные команды:
• /start – Приветственное сообщение
• /list – Показать список будущих напоминаний
• /today – Показать напоминания на сегодня
• /tomorrow – Показать напоминания на завтра
• /help – Показать помощь`

		reply := tgbotapi.NewMessage(msg.Chat.ID, welcome)
		b.bot.Send(reply)

	case "list":
		reminders, err := b.repo.GetUserReminders(msg.From.ID)
		if err != nil {
			b.logger.Printf("Error getting reminders: %v", err)
			reply := tgbotapi.NewMessage(msg.Chat.ID, "Ошибка при получении списка напоминаний.")
			b.bot.Send(reply)
			return
		}

		if len(reminders) == 0 {
			reply := tgbotapi.NewMessage(msg.Chat.ID, "У вас пока нет активных напоминаний.")
			b.bot.Send(reply)
			return
		}

		var lines []string
		for _, r := range reminders {
			lines = append(lines, fmt.Sprintf("%s – %s", r.ReminderTime.Format("02.01.2006 15:04"), r.Label))
		}

		text := "Ваши активные напоминания:\n" + strings.Join(lines, "\n")
		reply := tgbotapi.NewMessage(msg.Chat.ID, text)
		b.bot.Send(reply)

	case "today":
		now := time.Now()
		start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		end := start.Add(24 * time.Hour)

		reminders, err := b.repo.GetUserRemindersByPeriod(msg.From.ID, start, end)
		if err != nil {
			b.logger.Printf("Error getting today's reminders: %v", err)
			reply := tgbotapi.NewMessage(msg.Chat.ID, "Ошибка при получении напоминаний на сегодня.")
			b.bot.Send(reply)
			return
		}

		if len(reminders) == 0 {
			reply := tgbotapi.NewMessage(msg.Chat.ID, "На сегодня нет напоминаний.")
			b.bot.Send(reply)
			return
		}

		var lines []string
		for _, r := range reminders {
			lines = append(lines, fmt.Sprintf("%s – %s", r.ReminderTime.Format("15:04"), r.Label))
		}

		text := "Напоминания на сегодня:\n" + strings.Join(lines, "\n")
		reply := tgbotapi.NewMessage(msg.Chat.ID, text)
		b.bot.Send(reply)

	case "tomorrow":
		now := time.Now()
		start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).Add(24 * time.Hour)
		end := start.Add(24 * time.Hour)

		reminders, err := b.repo.GetUserRemindersByPeriod(msg.From.ID, start, end)
		if err != nil {
			b.logger.Printf("Error getting tomorrow's reminders: %v", err)
			reply := tgbotapi.NewMessage(msg.Chat.ID, "Ошибка при получении напоминаний на завтра.")
			b.bot.Send(reply)
			return
		}

		if len(reminders) == 0 {
			reply := tgbotapi.NewMessage(msg.Chat.ID, "На завтра нет напоминаний.")
			b.bot.Send(reply)
			return
		}

		var lines []string
		for _, r := range reminders {
			lines = append(lines, fmt.Sprintf("%s – %s", r.ReminderTime.Format("15:04"), r.Label))
		}

		text := "Напоминания на завтра:\n" + strings.Join(lines, "\n")
		reply := tgbotapi.NewMessage(msg.Chat.ID, text)
		b.bot.Send(reply)

	case "help":
		helpText := `Как пользоваться ботом:

1. Создать напоминание:
   Просто напишите, что и когда вам напомнить, например:
   "Напомни купить молоко завтра в 18:00"
   "Напомни позвонить маме через 2 часа"
   "Совещание в понедельник в 10:00"

2. Посмотреть напоминания:
   • /list - все активные напоминания
   • /today - напоминания на сегодня
   • /tomorrow - напоминания на завтра
   • "Покажи мои дела на сегодня"
   • "Что у меня запланировано на эту неделю?"

3. Изменить напоминание:
   "Перенеси напоминание о совещании на 11:00"
   "Измени встречу с клиентом на завтра"

4. Удалить напоминание:
   "Удали напоминание о встрече"
   "Отмени напоминание про звонок врачу"

Вы также можете отправлять голосовые сообщения!`

		reply := tgbotapi.NewMessage(msg.Chat.ID, helpText)
		b.bot.Send(reply)

	default:
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Неизвестная команда. Используйте /help для справки.")
		b.bot.Send(reply)
	}
}
