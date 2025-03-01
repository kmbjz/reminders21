package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"

	"reminders21/bot"
	"reminders21/config"
)

func main() {
	// Set up logger
	logger := log.New(os.Stdout, "[RemindersBot] ", log.LstdFlags)
	logger.Println("Starting RemindersBot...")

	// Load config
	cfg, err := config.Load()
	if err != nil {
		logger.Fatalf("Failed to load config: %v", err)
	}

	// Create bot
	reminderBot, err := bot.NewReminderBot(cfg, logger)
	if err != nil {
		logger.Fatalf("Failed to create bot: %v", err)
	}

	// Handle graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Start bot in a goroutine
	go func() {
		if err := reminderBot.Start(); err != nil {
			logger.Fatalf("Bot error: %v", err)
		}
	}()

	// Wait for termination signal
	<-sigChan
	logger.Println("Received termination signal, shutting down...")

	// Stop bot
	reminderBot.Stop()
	logger.Println("Bot stopped gracefully")
}
