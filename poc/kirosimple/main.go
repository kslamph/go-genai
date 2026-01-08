package main

import (
	"context"
	"fmt"
	"log"

	kiropkg "github.com/sunbankio/omniproxy/internal/provider/kiro"
)

func main() {
	ctx := context.Background()

	fmt.Println("=== Testing Simple Multiturn (No Tool Results) ===")

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

	// Try a different approach: just continue with a new user message
	// instead of sending tool results
	messages = append(messages, kiropkg.ChatMessage{
		Role:    "user",
		Content: "The weather in Tokyo is 22 degrees, sunny with 65% humidity.",
	})

	// Turn 2: Get final response
	fmt.Println("\n--- Turn 2: User Provides Weather Info ---")
	resp2, err := kiroProvider.ChatCompletion(ctx, &kiropkg.ChatRequest{
		Messages: messages,
		Model:    "claude-haiku-4-5",
		Tools:    tools,
	})
	if err != nil {
		log.Fatalf("Turn 2 failed: %v", err)
	}

	assistantMsg2 := resp2.Choices[0].Message
	fmt.Printf("Assistant: %s\n", assistantMsg2.Content)

	fmt.Println("\n✓ Simple multiturn successful")
}
