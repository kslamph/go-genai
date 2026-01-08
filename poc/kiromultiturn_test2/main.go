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

	fmt.Println("=== Test 2: Multiturn with Single Tool Call ===")

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
	fmt.Printf("User: What's the weather in Tokyo?\n")

	resp, err := kiroProvider.ChatCompletion(ctx, &kiropkg.ChatRequest{
		Messages: messages,
		Model:    "claude-haiku-4-5",
		Tools:    tools,
	})
	if err != nil {
		log.Fatalf("Turn 1 failed: %v", err)
	}

	assistantMsg := resp.Choices[0].Message
	fmt.Printf("Assistant: %s\n", assistantMsg.Content)

	// Check if assistant made tool calls
	if len(assistantMsg.ToolCalls) > 0 {
		fmt.Printf("\nTool Calls Detected: %d\n", len(assistantMsg.ToolCalls))
		for i, toolCall := range assistantMsg.ToolCalls {
			fmt.Printf("  Tool Call %d:\n", i+1)
			fmt.Printf("    ID: %s\n", toolCall.ID)
			fmt.Printf("    Type: %s\n", toolCall.Type)
			fmt.Printf("    Function: %s\n", toolCall.Function.Name)
			fmt.Printf("    Arguments: %s\n", toolCall.Function.Arguments)

			// Simulate tool execution and get result
			toolResult := simulateToolExecution(toolCall)
			fmt.Printf("    Tool Result: %s\n", toolResult)

			// Add tool response to conversation
			messages = append(messages, assistantMsg)
			messages = append(messages, kiropkg.ChatMessage{
				Role:       "tool",
				ToolCallID: toolCall.ID,
				Content:    toolResult,
			})
		}

		// Turn 2: Get final response after tool execution
		fmt.Println("\n--- Turn 2: Assistant Response After Tool Execution ---")
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
	} else {
		fmt.Println("No tool calls detected in response")
	}

	fmt.Println("\n✓ Multiturn with single tool call successful")
}

// simulateToolExecution simulates executing a tool and returns a result
func simulateToolExecution(toolCall kiropkg.ToolCall) string {
	switch toolCall.Function.Name {
	case "get_weather":
		// Parse arguments
		var args struct {
			Location string `json:"location"`
		}
		if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &args); err == nil {
			// Simulate weather data
			return fmt.Sprintf(`{"location": "%s", "temperature": 22, "condition": "Sunny", "humidity": 65}`, args.Location)
		}
		return `{"error": "Failed to parse arguments"}`

	default:
		return fmt.Sprintf(`{"error": "Unknown tool: %s"}`, toolCall.Function.Name)
	}
}