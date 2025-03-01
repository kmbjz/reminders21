package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// OpenAIClient is a client for OpenAI API
type OpenAIClient struct {
	APIKey  string
	Timeout time.Duration
}

// NewOpenAIClient creates a new OpenAI client
func NewOpenAIClient(apiKey string, timeout time.Duration) *OpenAIClient {
	return &OpenAIClient{
		APIKey:  apiKey,
		Timeout: timeout,
	}
}

// FunctionCall describes a function call
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ChatMessage represents a message in a chat
type ChatMessage struct {
	Role         string        `json:"role"`
	Content      string        `json:"content"`
	FunctionCall *FunctionCall `json:"function_call,omitempty"`
}

// ChatChoice represents a choice in a chat response
type ChatChoice struct {
	Message ChatMessage `json:"message"`
}

// OpenAIChatResponse represents the response from OpenAI API
type OpenAIChatResponse struct {
	Choices []ChatChoice `json:"choices"`
}

// Operation represents a reminder operation
type Operation struct {
	Action        string `json:"action"`
	Datetime      string `json:"datetime"`
	Label         string `json:"label"`
	ReminderID    string `json:"reminder_id"`
	Answer        string `json:"answer"`
	StartDate     string `json:"start_date"`
	EndDate       string `json:"end_date"`
	RecurringType string `json:"recurring_type"`
	Time          string `json:"time"`
	DayOfWeek     string `json:"day_of_week"`
	DayOfMonth    string `json:"day_of_month"`
}

// LLMOutputMulti represents the output JSON from LLM
type LLMOutputMulti struct {
	Operations    []Operation         `json:"operations"`
	UserReminders []map[string]string `json:"user_reminders"`
}

// ParseMessage parses a message using OpenAI API
func (c *OpenAIClient) ParseMessage(ctx context.Context, prompt string, input string, userReminders []map[string]string) (LLMOutputMulti, error) {
	var result LLMOutputMulti

	// Add current time to the prompt
	fullPrompt := fmt.Sprintf(prompt, time.Now().Format("2006-01-02 15:04:05"))

	// Add user reminders as JSON to the prompt
	reminderJSON, _ := json.Marshal(userReminders)
	fullPrompt += "\n" + string(reminderJSON)

	// Define the functions available to the model
	functions := []map[string]interface{}{
		{
			"name":        "adjust_reminder",
			"description": "Изменить существующее напоминание: изменить текст и/или время.",
			"parameters": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"reminder_id": map[string]interface{}{
						"type":        "string",
						"description": "ID напоминания для изменения",
					},
					"datetime": map[string]interface{}{
						"type":        "string",
						"description": "Новая дата и время в формате '2006-01-02 15:04:05' (необязательно)",
					},
					"label": map[string]interface{}{
						"type":        "string",
						"description": "Новый текст напоминания (необязательно)",
					},
				},
				"required": []string{"reminder_id"},
			},
		},
		{
			"name":        "delete_reminder",
			"description": "Удалить существующее напоминание.",
			"parameters": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"reminder_id": map[string]interface{}{
						"type":        "string",
						"description": "ID напоминания для удаления",
					},
				},
				"required": []string{"reminder_id"},
			},
		},
	}

	// Create request body
	reqBodyMap := map[string]interface{}{
		"model": "gpt-4",
		"messages": []map[string]string{
			{"role": "developer", "content": fullPrompt},
			{"role": "user", "content": input},
		},
		"functions":     functions,
		"function_call": "auto",
	}

	reqBody, err := json.Marshal(reqBodyMap)
	if err != nil {
		return result, err
	}

	// Create HTTP request with context
	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.openai.com/v1/chat/completions", bytes.NewBuffer(reqBody))
	if err != nil {
		return result, err
	}

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)

	// Make request
	client := &http.Client{Timeout: c.Timeout}
	resp, err := client.Do(req)
	if err != nil {
		return result, err
	}
	defer resp.Body.Close()

	// Read response
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return result, err
	}

	// Check response status
	if resp.StatusCode != http.StatusOK {
		return result, fmt.Errorf("OpenAI API returned status: %d, body: %s", resp.StatusCode, string(body))
	}

	// Parse response
	var openaiResp OpenAIChatResponse
	if err = json.Unmarshal(body, &openaiResp); err != nil {
		return result, err
	}

	if len(openaiResp.Choices) == 0 {
		return result, fmt.Errorf("no choices returned from OpenAI")
	}

	// Process response
	choice := openaiResp.Choices[0].Message
	if choice.FunctionCall != nil {
		fc := choice.FunctionCall
		if err := json.Unmarshal([]byte(fc.Arguments), &result); err != nil {
			return result, fmt.Errorf("error parsing function call arguments: %v", err)
		}
	} else {
		// Extract JSON from model output
		outputText := choice.Content

		// Try to find code block markers and trim them
		outputText = strings.Trim(outputText, "\r\n```json")

		// Find the JSON part
		startIdx := strings.Index(outputText, "{")
		endIdx := strings.LastIndex(outputText, "}")

		if startIdx >= 0 && endIdx > startIdx {
			jsonStr := outputText[startIdx : endIdx+1]
			if err = json.Unmarshal([]byte(jsonStr), &result); err != nil {
				return result, fmt.Errorf("error parsing JSON from model response: %v", err)
			}
		} else {
			return result, fmt.Errorf("failed to extract JSON from model output")
		}
	}

	// Validate operations
	for i, op := range result.Operations {
		if op.Action == "create" {
			if strings.TrimSpace(op.Label) == "" || strings.TrimSpace(op.Datetime) == "" {
				return result, fmt.Errorf("for 'create' operation, 'label' and 'datetime' are required")
			}
		} else if op.Action == "create_recurring" {
			if strings.TrimSpace(op.Label) == "" ||
				(strings.TrimSpace(op.Time) == "" && strings.TrimSpace(op.Datetime) == "") {
				return result, fmt.Errorf("for 'create_recurring' operation, 'label' and 'time' are required")
			}
			if strings.TrimSpace(op.RecurringType) == "" {
				return result, fmt.Errorf("for 'create_recurring' operation, 'recurring_type' is required")
			}
		} else if op.Action == "adjust" || op.Action == "delete" {
			if strings.TrimSpace(op.ReminderID) == "" {
				return result, fmt.Errorf("for '%s' operation, 'reminder_id' is required", op.Action)
			}
		}

		// Ensure operations have answers
		if op.Answer == "" {
			result.Operations[i].Answer = getDefaultAnswer(op.Action)
		}
	}

	return result, nil
}

// Fallback answers for different operation types
func getDefaultAnswer(action string) string {
	switch action {
	case "create":
		return "Напоминание создано."
	case "create_recurring":
		return "Повторяющееся напоминание создано."
	case "adjust":
		return "Напоминание изменено."
	case "delete":
		return "Напоминание удалено."
	case "show_list":
		return "Вот список напоминаний."
	case "show_recurring":
		return "Вот список повторяющихся напоминаний."
	default:
		return "Операция выполнена."
	}
}
