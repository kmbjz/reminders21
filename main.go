package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"reminders21/bot"
	"reminders21/cli"
	"reminders21/config"
)

var (
	broadcastMode = flag.Bool("broadcast", false, "Run in broadcast mode")
)

func main() {
	flag.Parse()

	// Set up logger
	logger := log.New(os.Stdout, "[RemindersBot] ", log.LstdFlags)

	// Check if we're running in broadcast mode
	if *broadcastMode {
		logger.Println("Running in broadcast mode...")
		cli.RunBroadcast()
		return
	}

	// Normal bot operation
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
