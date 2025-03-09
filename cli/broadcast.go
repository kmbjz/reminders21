package cli

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"os"
	"reminders21/storage"
	"strconv"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"reminders21/config"
)

// Command line arguments
var (
	allFlag     = flag.Bool("all", false, "Send message to all chats")
	chatIDFlag  = flag.String("chat", "", "Send message to specific chat ID")
	messageFlag = flag.String("message", "", "Message to send (if not provided, will read from stdin)")
)

// RunBroadcast runs the broadcast command
func RunBroadcast() {
	flag.Parse()

	// Load config
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// Initialize bot API
	bot, err := tgbotapi.NewBotAPI(cfg.TelegramToken)
	if err != nil {
		log.Fatalf("Failed to create bot: %v", err)
	}

	bot.Debug = cfg.Debug

	// Get message text
	var messageText string
	if *messageFlag != "" {
		messageText = *messageFlag
	} else {
		// Read message from stdin
		reader := bufio.NewReader(os.Stdin)
		fmt.Println("Enter message to broadcast (Ctrl+D to finish):")

		var lines []string
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				break
			}
			lines = append(lines, strings.TrimSuffix(line, "\n"))
		}

		messageText = strings.Join(lines, "\n")
	}

	if messageText == "" {
		log.Fatal("Message cannot be empty")
	}

	// Get a list of active chat IDs from the database
	chatIDs, err := getActiveChatIDs(cfg.DatabasePath)
	if err != nil {
		log.Fatalf("Failed to get chat IDs: %v", err)
	}

	// Send to specific chat if requested
	if *chatIDFlag != "" {
		chatID, err := strconv.ParseInt(*chatIDFlag, 10, 64)
		if err != nil {
			log.Fatalf("Invalid chat ID: %v", err)
		}

		msg := tgbotapi.NewMessage(chatID, messageText)
		if _, err := bot.Send(msg); err != nil {
			log.Printf("Failed to send message to chat %d: %v", chatID, err)
		} else {
			log.Printf("Successfully sent message to chat %d", chatID)
		}
		return
	}

	// Send to all chats if requested
	if *allFlag {
		successCount := 0
		failCount := 0

		for _, chatID := range chatIDs {
			msg := tgbotapi.NewMessage(chatID, messageText)
			if _, err := bot.Send(msg); err != nil {
				log.Printf("Failed to send message to chat %d: %v", chatID, err)
				failCount++
			} else {
				successCount++
			}
		}

		log.Printf("Broadcast complete: %d messages sent successfully, %d failed",
			successCount, failCount)
		return
	}

	// If neither -all nor -chat is specified
	log.Fatal("Please specify either -all or -chat flag")
}

// getActiveChatIDs retrieves a list of unique chat IDs from the database
func getActiveChatIDs(dbPath string) ([]int64, error) {
	// Initialize storage repository
	repo, err := storage.NewReminderRepository(dbPath, log.New(os.Stdout, "[BroadcastCLI] ", log.LstdFlags))
	if err != nil {
		return nil, fmt.Errorf("failed to initialize repository: %w", err)
	}
	defer repo.Close()

	// Get the chat IDs
	return repo.GetAllActiveChatIDs()
}
