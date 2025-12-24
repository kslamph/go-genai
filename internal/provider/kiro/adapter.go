package kiro

import (
	"fmt"
	"time"

	"github.com/sashabaranov/go-openai"
)

// CodeWhispererRequest is the Kiro API request structure
type CodeWhispererRequest struct {
	ConversationState ConversationState `json:"conversationState"`
	ProfileArn        *string           `json:"profileArn,omitempty"`
}

type ConversationState struct {
	ChatTriggerType string         `json:"chatTriggerType"`
	ConversationID  string         `json:"conversationId"`
	CurrentMessage  CurrentMessage `json:"currentMessage"`
	History         []HistoryItem  `json:"history,omitempty"`
}

type CurrentMessage struct {
	UserInputMessage UserInputMessage `json:"userInputMessage"`
}

type UserInputMessage struct {
	Content                 string                   `json:"content"`
	ModelID                 string                   `json:"modelId"`
	Origin                  string                   `json:"origin"`
	UserInputMessageContext *UserInputMessageContext `json:"userInputMessageContext,omitempty"`
}

type UserInputMessageContext struct {
	// Tools and ToolResults support can be added here
}

type HistoryItem struct {
	UserInputMessage         *UserInputMessage         `json:"userInputMessage,omitempty"`
	AssistantResponseMessage *AssistantResponseMessage `json:"assistantResponseMessage,omitempty"`
}

type AssistantResponseMessage struct {
	Content string `json:"content"`
	// ToolUses support can be added here
}

var modelMap = map[string]string{
	// Opus 4.5
	"claude-opus-4-5":          "claude-opus-4.5",
	"claude-opus-4-5-20251101": "claude-opus-4.5",

	// Haiku 4.5
	"claude-haiku-4-5":          "claude-haiku-4.5",
	"claude-haiku-4-5-20251001": "claude-haiku-4.5",

	// Sonnet 4.5
	"claude-sonnet-4-5":          "CLAUDE_SONNET_4_5_20250929_V1_0",
	"claude-sonnet-4-5-20250929": "CLAUDE_SONNET_4_5_20250929_V1_0",

	// Sonnet 4
	"claude-sonnet-4-20250514": "CLAUDE_SONNET_4_20250514_V1_0",

	// Sonnet 3.7/3.5
	"claude-3-7-sonnet-20250219": "CLAUDE_3_7_SONNET_20250219_V1_0",
	"claude-3-5-sonnet-20241022": "CLAUDE_3_7_SONNET_20250219_V1_0",
	"claude-3-5-sonnet-latest":   "CLAUDE_3_7_SONNET_20250219_V1_0",
}

func getKiroModel(openaiModel string) string {
	if kiroModel, exists := modelMap[openaiModel]; exists {
		return kiroModel
	}
	// Default to Sonnet 4.5 if not found, or maybe throw error
	return "CLAUDE_SONNET_4_5_20250929_V1_0"
}

// ToKiroRequest converts OpenAI request to Kiro request
func ToKiroRequest(req openai.ChatCompletionRequest, profileArn *string) (*CodeWhispererRequest, error) {
	conversationID := generateUUID()
	kiroModel := getKiroModel(req.Model)

	// Extract last message as current message
	if len(req.Messages) == 0 {
		return nil, fmt.Errorf("no messages provided")
	}
	lastMsg := req.Messages[len(req.Messages)-1]
	if lastMsg.Role != openai.ChatMessageRoleUser {
		return nil, fmt.Errorf("last message must be from user")
	}

	currentContent := lastMsg.Content

	// Extract history
	var history []HistoryItem
	for i := 0; i < len(req.Messages)-1; i++ {
		msg := req.Messages[i]
		if msg.Role == openai.ChatMessageRoleUser {
			history = append(history, HistoryItem{
				UserInputMessage: &UserInputMessage{
					Content: msg.Content,
				},
			})
		} else if msg.Role == openai.ChatMessageRoleAssistant {
			history = append(history, HistoryItem{
				AssistantResponseMessage: &AssistantResponseMessage{
					Content: msg.Content,
				},
			})
		}
		// Skip System messages for now or prepend to first User message?
	}

	return &CodeWhispererRequest{
		ConversationState: ConversationState{
			ChatTriggerType: "MANUAL",
			ConversationID:  conversationID,
			CurrentMessage: CurrentMessage{
				UserInputMessage: UserInputMessage{
					Content: currentContent,
					ModelID: kiroModel,
					Origin:  "AI_EDITOR",
				},
			},
			History: history,
		},
		ProfileArn: profileArn,
	}, nil
}

// Helper to generate UUID
func generateUUID() string {
	return fmt.Sprintf("%x", time.Now().UnixNano())
}

// Helper for Kiro response parsing (which is complex due to EventStream)
// ...
