package bot

import (
	"context"
	"os"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// handleTextMessage handles text messages
func (b *ReminderBot) handleTextMessage(msg *tgbotapi.Message) {
	b.logger.Printf("Received text message from %d: %s", msg.From.ID, msg.Text)

	// Get user reminders for context
	userReminders, err := b.getUserRemindersAsMap(msg.From.ID)
	if err != nil {
		b.logger.Printf("Error getting user reminders: %v", err)
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Произошла ошибка при обработке запроса.")
		b.bot.Send(reply)
		return
	}

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), b.config.APITimeout)
	defer cancel()

	// Parse message with LLM
	llmOutput, err := b.llmClient.ParseMessage(ctx, llmPrompt, msg.Text, userReminders)
	if err != nil {
		b.logger.Printf("Error parsing message with LLM: %v", err)
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Не смог разобрать запрос. Попробуйте переформулировать.")
		b.bot.Send(reply)
		return
	}

	// Process operations
	b.processOperations(llmOutput.Operations, msg)
}

// handleEditedMessage handles edited messages
func (b *ReminderBot) handleEditedMessage(msg *tgbotapi.Message) {
	b.logger.Printf("Received edited message from %d: %s", msg.From.ID, msg.Text)

	// Get user reminders for context
	userReminders, err := b.getUserRemindersAsMap(msg.From.ID)
	if err != nil {
		b.logger.Printf("Error getting user reminders: %v", err)
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Произошла ошибка при обработке запроса.")
		b.bot.Send(reply)
		return
	}

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), b.config.APITimeout)
	defer cancel()

	// Add prefix to indicate this is an edited message
	editedText := "Отредактировано: " + msg.Text

	// Parse message with LLM
	llmOutput, err := b.llmClient.ParseMessage(ctx, llmPrompt, editedText, userReminders)
	if err != nil {
		b.logger.Printf("Error parsing edited message with LLM: %v", err)
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Не смог разобрать отредактированный запрос. Попробуйте ещё раз.")
		b.bot.Send(reply)
		return
	}

	// Process operations
	b.processOperations(llmOutput.Operations, msg)
}

// handleVoiceMessage handles voice messages
func (b *ReminderBot) handleVoiceMessage(msg *tgbotapi.Message) {
	b.logger.Printf("Received voice message from %d", msg.From.ID)

	// Send typing action
	b.bot.Send(tgbotapi.NewChatAction(msg.Chat.ID, tgbotapi.ChatRecordVoice))

	// Download voice file
	fileID := msg.Voice.FileID
	filePath, err := b.downloadTelegramFile(fileID)
	if err != nil {
		b.logger.Printf("Error downloading voice file: %v", err)
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Не удалось обработать голосовое сообщение.")
		b.bot.Send(reply)
		return
	}
	defer os.Remove(filePath)

	// Transcribe
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	transcription, err := b.transcriber.TranscribeFile(ctx, filePath)
	if err != nil {
		b.logger.Printf("Error transcribing voice: %v", err)
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Не удалось распознать голосовое сообщение.")
		b.bot.Send(reply)
		return
	}

	// Process transcription
	b.logger.Printf("Transcription: %s", transcription)

	// Get user reminders for context
	userReminders, err := b.getUserRemindersAsMap(msg.From.ID)
	if err != nil {
		b.logger.Printf("Error getting user reminders: %v", err)
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Произошла ошибка при обработке запроса.")
		b.bot.Send(reply)
		return
	}

	// Parse transcription with LLM
	llmOutput, err := b.llmClient.ParseMessage(ctx, llmPrompt, transcription, userReminders)
	if err != nil {
		b.logger.Printf("Error parsing transcription with LLM: %v", err)
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Не смог разобрать запрос из голосового сообщения. Попробуйте ещё раз.")
		b.bot.Send(reply)
		return
	}

	// Process operations
	b.processOperations(llmOutput.Operations, msg)
}

// handleVideoMessage handles video messages
func (b *ReminderBot) handleVideoMessage(msg *tgbotapi.Message) {
	b.logger.Printf("Received video message from %d", msg.From.ID)

	// Send typing action
	b.bot.Send(tgbotapi.NewChatAction(msg.Chat.ID, tgbotapi.ChatRecordVoice))

	// Download video file
	fileID := msg.Video.FileID
	videoPath, err := b.downloadTelegramFile(fileID)
	if err != nil {
		b.logger.Printf("Error downloading video file: %v", err)
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Не удалось обработать видео сообщение.")
		b.bot.Send(reply)
		return
	}
	defer os.Remove(videoPath)

	// Extract audio from video
	audioPath, err := b.extractAudioFromVideo(videoPath)
	if err != nil {
		b.logger.Printf("Error extracting audio from video: %v", err)
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Не удалось извлечь аудио из видео.")
		b.bot.Send(reply)
		return
	}
	defer os.Remove(audioPath)

	// Transcribe audio
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	transcription, err := b.transcriber.TranscribeFile(ctx, audioPath)
	if err != nil {
		b.logger.Printf("Error transcribing audio from video: %v", err)
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Не удалось распознать аудио из видео.")
		b.bot.Send(reply)
		return
	}

	// Process transcription
	b.logger.Printf("Transcription from video: %s", transcription)

	// Get user reminders for context
	userReminders, err := b.getUserRemindersAsMap(msg.From.ID)
	if err != nil {
		b.logger.Printf("Error getting user reminders: %v", err)
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Произошла ошибка при обработке запроса.")
		b.bot.Send(reply)
		return
	}

	// Parse transcription with LLM
	llmOutput, err := b.llmClient.ParseMessage(ctx, llmPrompt, transcription, userReminders)
	if err != nil {
		b.logger.Printf("Error parsing transcription with LLM: %v", err)
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Не смог разобрать запрос из видео. Попробуйте ещё раз.")
		b.bot.Send(reply)
		return
	}

	// Process operations
	b.processOperations(llmOutput.Operations, msg)
}
