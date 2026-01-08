package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	kiropkg "github.com/sunbankio/omniproxy/internal/provider/kiro"
)

func main() {
	ctx := context.Background()

	fmt.Println("=== Debugging Kiro Multiturn Request ===")

	// Initialize Kiro authenticator
	kiroAuth := kiropkg.NewAuthenticator(nil)

	// Check if authenticated
	if !kiroAuth.IsAuthenticated() {
		fmt.Println("Kiro: Not authenticated.")
		return
	}

	// Create provider
	kiroProvider := kiropkg.NewProvider("kiro-default", kiroAuth)

	tools := []kiropkg.Tool{
		{
			Type: "function",
			Function: kiropkg.ToolFunctionSpec{
				Name:        "get_weather",
				Description: "Get current weather for a location",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"location": map[string]interface{}{
							"type":        "string",
							"description": "City name",
						},
					},
					"required": []string{"location"},
				},
			},
		},
	}

	// Turn 1: User asks for weather (triggers tool call)
	messages := []kiropkg.ChatMessage{
		{
			Role:    "user",
			Content: "What's the weather in Tokyo?",
		},
	}

	fmt.Println("\n--- Turn 1: User Request (Tool Call) ---")
	resp, err := kiroProvider.ChatCompletion(ctx, &kiropkg.ChatRequest{
		Messages: messages,
		Model:    "claude-haiku-4-5",
		Tools:    tools,
	})
	if err != nil {
		log.Fatalf("Turn 1 failed: %v", err)
	}

	assistantMsg := resp.Choices[0].Message
	fmt.Printf("Tool calls: %d\n", len(assistantMsg.ToolCalls))

	// Add tool response to conversation
	messages = append(messages, assistantMsg)
	messages = append(messages, kiropkg.ChatMessage{
		Role:       "tool",
		ToolCallID: assistantMsg.ToolCalls[0].ID,
		Content:    `{"location": "Tokyo", "temperature": 22, "condition": "Sunny", "humidity": 65}`,
	})

	// Turn 2: Get final response after tool execution
	fmt.Println("\n--- Turn 2: Assistant Response After Tool Execution ---")

	// Print the request that will be sent
	req := &kiropkg.ChatRequest{
		Messages: messages,
		Model:    "claude-haiku-4-5",
		Tools:    tools,
	}

	reqJSON, _ := json.MarshalIndent(req, "", "  ")
	fmt.Printf("\nOpenAI-like Request:\n%s\n", string(reqJSON))

	resp2, err := kiroProvider.ChatCompletion(ctx, req)
	if err != nil {
		log.Fatalf("Turn 2 failed: %v", err)
	}

	assistantMsg2 := resp2.Choices[0].Message
	fmt.Printf("Assistant: %s\n", assistantMsg2.Content)
}
