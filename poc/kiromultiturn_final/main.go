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

	fmt.Println("=== Kiro Multiturn Tool Call Guide ===")
	fmt.Println("\nIMPORTANT: Kiro does NOT support OpenAI-style tool result messages.")
	fmt.Println("Instead, tool results should be provided as user messages.")
	fmt.Println()

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

	// Example 1: Simple multiturn without tools
	fmt.Println("=== Example 1: Simple Multiturn (No Tools) ===")
	testSimpleMultiturn(ctx, kiroProvider)

	// Example 2: Multiturn with tool calls (Kiro-style)
	fmt.Println("\n=== Example 2: Multiturn with Tool Calls (Kiro-Style) ===")
	testMultiturnWithToolCalls(ctx, kiroProvider, tools)

	// Example 3: Streaming with tool calls
	fmt.Println("\n=== Example 3: Streaming with Tool Calls ===")
	testStreamingWithToolCalls(ctx, kiroProvider, tools)

	fmt.Println("\n=== All tests completed ===")
}

// testSimpleMultiturn demonstrates basic multiturn conversation
func testSimpleMultiturn(ctx context.Context, p *kiropkg.Provider) {
	messages := []kiropkg.ChatMessage{
		{
			Role:    "user",
			Content: "What is the capital of France?",
		},
	}

	resp, err := p.ChatCompletion(ctx, &kiropkg.ChatRequest{
		Messages: messages,
		Model:    "claude-haiku-4-5",
	})
	if err != nil {
		log.Printf("Error: %v", err)
		return
	}

	fmt.Printf("Q: What is the capital of France?\n")
	fmt.Printf("A: %s\n", resp.Choices[0].Message.Content)

	// Follow-up question
	messages = append(messages, resp.Choices[0].Message)
	messages = append(messages, kiropkg.ChatMessage{
		Role:    "user",
		Content: "And what about Germany?",
	})

	resp2, err := p.ChatCompletion(ctx, &kiropkg.ChatRequest{
		Messages: messages,
		Model:    "claude-haiku-4-5",
	})
	if err != nil {
		log.Printf("Error: %v", err)
		return
	}

	fmt.Printf("Q: And what about Germany?\n")
	fmt.Printf("A: %s\n", resp2.Choices[0].Message.Content)
}

// testMultiturnWithToolCalls demonstrates the correct way to handle tool calls in Kiro
func testMultiturnWithToolCalls(ctx context.Context, p *kiropkg.Provider, tools []kiropkg.Tool) {
	// Step 1: User asks a question that requires a tool
	messages := []kiropkg.ChatMessage{
		{
			Role:    "user",
			Content: "What's the weather in Tokyo?",
		},
	}

	fmt.Println("Step 1: User asks for weather")
	resp, err := p.ChatCompletion(ctx, &kiropkg.ChatRequest{
		Messages: messages,
		Model:    "claude-haiku-4-5",
		Tools:    tools,
	})
	if err != nil {
		log.Printf("Error: %v", err)
		return
	}

	assistantMsg := resp.Choices[0].Message
	fmt.Printf("Assistant response: %s\n", assistantMsg.Content)

	// Step 2: Check if assistant made tool calls
	if len(assistantMsg.ToolCalls) > 0 {
		fmt.Printf("\nStep 2: Assistant requested tool calls (%d)\n", len(assistantMsg.ToolCalls))

		// Execute tools locally
		for _, toolCall := range assistantMsg.ToolCalls {
			fmt.Printf("  - %s(%s)\n", toolCall.Function.Name, toolCall.Function.Arguments)

			// Simulate tool execution
			toolResult := simulateToolExecution(toolCall)
			fmt.Printf("  - Tool result: %s\n", toolResult)
		}

		// Step 3: CRITICAL: In Kiro, provide tool results as a USER message
		// NOT as a "tool" role message like OpenAI
		fmt.Println("\nStep 3: Provide tool results as USER message (Kiro-style)")
		messages = append(messages, assistantMsg)
		messages = append(messages, kiropkg.ChatMessage{
			Role:    "user",
			Content: "Tool execution result: The weather in Tokyo is 22 degrees, sunny with 65% humidity.",
		})

		// Step 4: Get final response
		fmt.Println("Step 4: Get assistant's final response")
		resp2, err := p.ChatCompletion(ctx, &kiropkg.ChatRequest{
			Messages: messages,
			Model:    "claude-haiku-4-5",
			Tools:    tools,
		})
		if err != nil {
			log.Printf("Error: %v", err)
			return
		}

		fmt.Printf("Final answer: %s\n", resp2.Choices[0].Message.Content)
	} else {
		fmt.Println("No tool calls detected")
	}
}

// testStreamingWithToolCalls demonstrates streaming with tool calls
func testStreamingWithToolCalls(ctx context.Context, p *kiropkg.Provider, tools []kiropkg.Tool) {
	messages := []kiropkg.ChatMessage{
		{
			Role:    "user",
			Content: "Calculate 25 * 4 + 10",
		},
	}

	fmt.Println("Step 1: User asks for calculation")

	events, errChan, err := p.StreamChatCompletion(ctx, &kiropkg.ChatRequest{
		Messages: messages,
		Model:    "claude-haiku-4-5",
		Tools:    tools,
	})
	if err != nil {
		log.Printf("Error: %v", err)
		return
	}

	fmt.Print("Assistant (streaming): ")

	var assistantMsg kiropkg.ChatMessage
	var toolCalls []kiropkg.ToolCall

	for event := range events {
		switch event.Type {
		case "content":
			if content, ok := event.Content["content"].(string); ok {
				fmt.Print(content)
				if assistantMsg.Content == nil {
					assistantMsg.Content = content
				} else if contentStr, ok := assistantMsg.Content.(string); ok {
					assistantMsg.Content = contentStr + content
				}
			}
		case "tool_use":
			fmt.Printf("\n[Tool Call Detected]")
			if name, ok := event.Content["name"].(string); ok {
				if toolUseID, ok := event.Content["toolUseId"].(string); ok {
					if inputRaw, ok := event.Content["input"].(string); ok {
						toolCalls = append(toolCalls, kiropkg.ToolCall{
							ID:   toolUseID,
							Type: "function",
							Function: kiropkg.ToolCallFunction{
								Name:      name,
								Arguments: inputRaw,
							},
						})
						fmt.Printf(" Function: %s\n", name)
					}
				}
			}
		}
	}

	// Check for errors
	select {
	case err := <-errChan:
		if err != nil && err.Error() != "EOF" {
			log.Printf("Stream error: %v", err)
		}
	default:
	}

	assistantMsg.Role = "assistant"
	assistantMsg.ToolCalls = toolCalls

	fmt.Println("\n✓ Streaming completed")
}

// simulateToolExecution simulates tool execution
func simulateToolExecution(toolCall kiropkg.ToolCall) string {
	switch toolCall.Function.Name {
	case "get_weather":
		var args struct {
			Location string `json:"location"`
		}
		if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &args); err == nil {
			return fmt.Sprintf(`{"location": "%s", "temperature": 22, "condition": "Sunny", "humidity": 65}`, args.Location)
		}
		return `{"error": "Failed to parse arguments"}`
	default:
		return fmt.Sprintf(`{"error": "Unknown tool: %s"}`, toolCall.Function.Name)
	}
}
