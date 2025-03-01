package config

import (
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
)

// Config holds all configuration for the application
type Config struct {
	TelegramToken         string
	OpenAIAPIKey          string
	DatabasePath          string
	ReminderCheckInterval time.Duration
	APITimeout            time.Duration
	LogFilePath           string
	Debug                 bool
}

// Load loads configuration from environment variables
func Load() (*Config, error) {
	// Load .env file if it exists
	_ = godotenv.Load()

	cfg := &Config{
		TelegramToken:         getEnv("TELEGRAM_BOT_TOKEN", ""),
		OpenAIAPIKey:          getEnv("OPENAI_API_KEY", ""),
		DatabasePath:          getEnv("DATABASE_PATH", "reminders.db"),
		ReminderCheckInterval: getDurationEnv("REMINDER_CHECK_INTERVAL", 10*time.Second),
		APITimeout:            getDurationEnv("API_TIMEOUT", 15*time.Second),
		LogFilePath:           getEnv("LOG_FILE_PATH", ""),
		Debug:                 getBoolEnv("DEBUG", false),
	}

	// Validate required configs
	if cfg.TelegramToken == "" {
		return nil, ErrMissingTelegramToken
	}

	if cfg.OpenAIAPIKey == "" {
		return nil, ErrMissingOpenAIAPIKey
	}

	return cfg, nil
}

func getEnv(key, defaultValue string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return defaultValue
}

func getBoolEnv(key string, defaultValue bool) bool {
	if value, exists := os.LookupEnv(key); exists {
		boolValue, err := strconv.ParseBool(value)
		if err != nil {
			return defaultValue
		}
		return boolValue
	}
	return defaultValue
}

func getDurationEnv(key string, defaultValue time.Duration) time.Duration {
	if value, exists := os.LookupEnv(key); exists {
		duration, err := time.ParseDuration(value)
		if err != nil {
			return defaultValue
		}
		return duration
	}
	return defaultValue
}

// Errors
var (
	ErrMissingTelegramToken = ErrConfig("missing TELEGRAM_BOT_TOKEN")
	ErrMissingOpenAIAPIKey  = ErrConfig("missing OPENAI_API_KEY")
)

// ErrConfig represents a configuration error
type ErrConfig string

func (e ErrConfig) Error() string {
	return string(e)
}
