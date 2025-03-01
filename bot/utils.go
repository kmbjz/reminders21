package bot

import (
	"fmt"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
)

// downloadTelegramFile downloads a file from Telegram
func (b *ReminderBot) downloadTelegramFile(fileID string) (string, error) {
	fileCfg := tgbotapi.FileConfig{FileID: fileID}
	file, err := b.bot.GetFile(fileCfg)
	if err != nil {
		return "", fmt.Errorf("failed to get file info: %w", err)
	}

	fileURL := file.Link(b.bot.Token)
	resp, err := http.Get(fileURL)
	if err != nil {
		return "", fmt.Errorf("failed to download file: %w", err)
	}
	defer resp.Body.Close()

	// Create temporary file
	tmpFile, err := os.CreateTemp("", "tgfile-*"+filepath.Ext(file.FilePath))
	if err != nil {
		return "", fmt.Errorf("failed to create temporary file: %w", err)
	}

	// Copy downloaded content to temporary file
	if _, err = io.Copy(tmpFile, resp.Body); err != nil {
		os.Remove(tmpFile.Name())
		return "", fmt.Errorf("failed to save file: %w", err)
	}

	tmpFile.Close()
	return tmpFile.Name(), nil
}

// extractAudioFromVideo extracts audio from video using ffmpeg
func (b *ReminderBot) extractAudioFromVideo(videoPath string) (string, error) {
	// Create temporary file for audio
	tmpAudio, err := os.CreateTemp("", "audio-*.ogg")
	if err != nil {
		return "", fmt.Errorf("failed to create temporary audio file: %w", err)
	}
	tmpAudio.Close()

	// Extract audio using ffmpeg
	cmd := exec.Command("ffmpeg", "-i", videoPath, "-vn", "-acodec", "libopus", "-f", "ogg", tmpAudio.Name())
	if err := cmd.Run(); err != nil {
		os.Remove(tmpAudio.Name())
		return "", fmt.Errorf("failed to extract audio with ffmpeg: %w", err)
	}

	return tmpAudio.Name(), nil
}
