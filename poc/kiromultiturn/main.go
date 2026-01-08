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

	fmt.Println("=== Testing Kiro Multiturn Tool Call Conversations ===")

	// Initialize Kiro authenticator
	kiroAuth := kiropkg.NewAuthenticator(nil)

	// Check if authenticated
	fmt.Println("Checking authentication status...")
	if !kiroAuth.IsAuthenticated() {
		fmt.Println("Kiro: Not authenticated.")
		fmt.Println("Please ensure your credentials are saved at:")
		fmt.Printf("  %s\n", kiroAuth.GetCredentialsPath())
		fmt.Println("\nSkipping API tests (no credentials).")
		return
	}

	fmt.Println("Kiro: Already authenticated.")

	// Test token retrieval
	fmt.Println("\n--- Testing Token Retrieval ---")
	token, err := kiroAuth.GetToken(ctx)
	if err != nil {
		log.Fatalf("Failed to get access token: %v", err)
	}
	fmt.Printf("Access token obtained successfully (length: %d)\n", len(token))

	// Create provider
	fmt.Println("\n--- Creating Kiro Provider ---")
	kiroProvider := kiropkg.NewProvider("kiro-default", kiroAuth)

	// Test 1: Simple multiturn conversation
	fmt.Println("\n=== Test 1: Simple Multiturn Conversation ===")
	testSimpleMultiturn(ctx, kiroProvider)

	// Test 2: Multiturn with single tool call
	fmt.Println("\n=== Test 2: Multiturn with Single Tool Call ===")
	testMultiturnWithSingleTool(ctx, kiroProvider)

	// Test 3: Multiturn with multiple sequential tool calls
	fmt.Println("\n=== Test 3: Multiturn with Multiple Sequential Tool Calls ===")
	testMultiturnWithMultipleTools(ctx, kiroProvider)

	// Test 4: Multiturn streaming with tool calls
	fmt.Println("\n=== Test 4: Multiturn Streaming with Tool Calls ===")
	testMultiturnStreamingWithTools(ctx, kiroProvider)

	fmt.Println("\n=== All multiturn tests completed ===")
}

// testSimpleMultiturn demonstrates basic multiturn conversation without tools
func testSimpleMultiturn(ctx context.Context, p *kiropkg.Provider) {
	// Turn 1: User asks a question
	messages := []kiropkg.ChatMessage{
		{
			Role:    "user",
			Content: "What is the capital of France?",
		},
	}

	fmt.Println("\n--- Turn 1: User Question ---")
	for i, msg := range messages {
		fmt.Printf("Message %d [%s]: %s\n", i+1, msg.Role, msg.Content)
	}

	resp, err := p.ChatCompletion(ctx, &kiropkg.ChatRequest{
		Messages: messages,
		Model:    "claude-haiku-4-5",
	})
	if err != nil {
		log.Printf("Turn 1 failed: %v", err)
		return
	}

	assistantMsg := resp.Choices[0].Message
	fmt.Printf("Assistant: %s\n", assistantMsg.Content)

	// Add assistant response to conversation history
	messages = append(messages, assistantMsg)

	// Turn 2: User follows up
	messages = append(messages, kiropkg.ChatMessage{
		Role:    "user",
		Content: "And what about Germany?",
	})

	fmt.Println("\n--- Turn 2: User Follow-up ---")
	fmt.Printf("User: And what about Germany?\n")

	resp2, err := p.ChatCompletion(ctx, &kiropkg.ChatRequest{
		Messages: messages,
		Model:    "claude-haiku-4-5",
	})
	if err != nil {
		log.Printf("Turn 2 failed: %v", err)
		return
	}

	assistantMsg2 := resp2.Choices[0].Message
	fmt.Printf("Assistant: %s\n", assistantMsg2.Content)

	fmt.Println("\n✓ Simple multiturn conversation successful")
}

// testMultiturnWithSingleTool demonstrates multiturn conversation with one tool call
func testMultiturnWithSingleTool(ctx context.Context, p *kiropkg.Provider) {
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

	resp, err := p.ChatCompletion(ctx, &kiropkg.ChatRequest{
		Messages: messages,
		Model:    "claude-haiku-4-5",
		Tools:    tools,
	})
	if err != nil {
		log.Printf("Turn 1 failed: %v", err)
		return
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
		resp2, err := p.ChatCompletion(ctx, &kiropkg.ChatRequest{
			Messages: messages,
			Model:    "claude-haiku-4-5",
			Tools:    tools,
		})
		if err != nil {
			log.Printf("Turn 2 failed: %v", err)
			return
		}

		assistantMsg2 := resp2.Choices[0].Message
		fmt.Printf("Assistant: %s\n", assistantMsg2.Content)
	} else {
		fmt.Println("No tool calls detected in response")
	}

	fmt.Println("\n✓ Multiturn with single tool call successful")
}

// testMultiturnWithMultipleTools demonstrates multiturn with multiple sequential tool calls
func testMultiturnWithMultipleTools(ctx context.Context, p *kiropkg.Provider) {
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
		{
			Type: "function",
			Function: kiropkg.ToolFunctionSpec{
				Name:        "get_time",
				Description: "Get current time for a location",
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

	// Turn 1: User asks for weather and time (may trigger multiple tools)
	messages := []kiropkg.ChatMessage{
		{
			Role:    "user",
			Content: "What's the weather and current time in London and New York?",
		},
	}

	fmt.Println("\n--- Turn 1: User Request (Multiple Tools) ---")
	fmt.Printf("User: What's the weather and current time in London and New York?\n")

	resp, err := p.ChatCompletion(ctx, &kiropkg.ChatRequest{
		Messages: messages,
		Model:    "claude-haiku-4-5",
		Tools:    tools,
	})
	if err != nil {
		log.Printf("Turn 1 failed: %v", err)
		return
	}

	assistantMsg := resp.Choices[0].Message
	fmt.Printf("Assistant: %s\n", assistantMsg.Content)

	// Process all tool calls
	toolCallCount := 0
	for len(assistantMsg.ToolCalls) > 0 {
		toolCallCount++
		fmt.Printf("\n--- Tool Call Batch %d ---\n", toolCallCount)
		fmt.Printf("Number of tool calls: %d\n", len(assistantMsg.ToolCalls))

		// Add assistant message with tool calls to conversation
		messages = append(messages, assistantMsg)

		// Execute all tools in this batch
		for _, toolCall := range assistantMsg.ToolCalls {
			fmt.Printf("  Executing: %s\n", toolCall.Function.Name)
			toolResult := simulateToolExecution(toolCall)
			fmt.Printf("    Result: %s\n", toolResult)

			// Add tool response
			messages = append(messages, kiropkg.ChatMessage{
				Role:       "tool",
				ToolCallID: toolCall.ID,
				Content:    toolResult,
			})
		}

		// Get next response
		fmt.Printf("\n--- Getting Response After Tool Execution ---\n")
		resp, err = p.ChatCompletion(ctx, &kiropkg.ChatRequest{
			Messages: messages,
			Model:    "claude-haiku-4-5",
			Tools:    tools,
		})
		if err != nil {
			log.Printf("Response failed: %v", err)
			return
		}

		assistantMsg = resp.Choices[0].Message
		fmt.Printf("Assistant: %s\n", assistantMsg.Content)
	}

	fmt.Printf("\n✓ Multiturn with multiple tool calls successful (total tool call batches: %d)\n", toolCallCount)
}

// testMultiturnStreamingWithTools demonstrates multiturn conversation with streaming and tool calls
func testMultiturnStreamingWithTools(ctx context.Context, p *kiropkg.Provider) {
	tools := []kiropkg.Tool{
		{
			Type: "function",
			Function: kiropkg.ToolFunctionSpec{
				Name:        "calculate",
				Description: "Perform a mathematical calculation",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"expression": map[string]interface{}{
							"type":        "string",
							"description": "Mathematical expression to evaluate",
						},
					},
					"required": []string{"expression"},
				},
			},
		},
	}

	// Turn 1: User asks for calculation
	messages := []kiropkg.ChatMessage{
		{
			Role:    "user",
			Content: "Calculate 25 * 4 + 10",
		},
	}

	fmt.Println("\n--- Turn 1: Streaming Request with Tool Call ---")
	fmt.Printf("User: Calculate 25 * 4 + 10\n")
	fmt.Printf("Assistant (streaming): ")

	var assistantMsg kiropkg.ChatMessage
	var toolCalls []kiropkg.ToolCall

	events, errChan, err := p.StreamChatCompletion(ctx, &kiropkg.ChatRequest{
		Messages: messages,
		Model:    "claude-haiku-4-5",
		Tools:    tools,
	})
	if err != nil {
		log.Printf("Streaming failed: %v", err)
		return
	}

	// Collect streaming events
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
			// Parse tool use from event
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
						fmt.Printf(" Function: %s, ID: %s\n", name, toolUseID)
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

	// Set role and tool calls
	assistantMsg.Role = "assistant"
	assistantMsg.ToolCalls = toolCalls

	// Process tool calls if any
	if len(toolCalls) > 0 {
		fmt.Println("\n--- Executing Tool Calls ---")
		messages = append(messages, assistantMsg)

		for _, toolCall := range toolCalls {
			fmt.Printf("Executing: %s with args: %s\n", toolCall.Function.Name, toolCall.Function.Arguments)
			toolResult := simulateToolExecution(toolCall)
			fmt.Printf("Result: %s\n", toolResult)

			messages = append(messages, kiropkg.ChatMessage{
				Role:       "tool",
				ToolCallID: toolCall.ID,
				Content:    toolResult,
			})
		}

		// Turn 2: Get final response
		fmt.Println("\n--- Turn 2: Final Response ---")
		fmt.Printf("Assistant: ")

		resp, err := p.ChatCompletion(ctx, &kiropkg.ChatRequest{
			Messages: messages,
			Model:    "claude-haiku-4-5",
			Tools:    tools,
		})
		if err != nil {
			log.Printf("Final response failed: %v", err)
			return
		}

		fmt.Printf("%s\n", resp.Choices[0].Message.Content)
	}

	fmt.Println("\n✓ Multiturn streaming with tool calls successful")
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

	case "get_time":
		var args struct {
			Location string `json:"location"`
		}
		if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &args); err == nil {
			// Simulate time data
			return fmt.Sprintf(`{"location": "%s", "time": "2026-01-08T14:30:00Z", "timezone": "UTC"}`, args.Location)
		}
		return `{"error": "Failed to parse arguments"}`

	case "calculate":
		var args struct {
			Expression string `json:"expression"`
		}
		if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &args); err == nil {
			// Simple calculation simulation
			result := simulateCalculation(args.Expression)
			return fmt.Sprintf(`{"expression": "%s", "result": %d}`, args.Expression, result)
		}
		return `{"error": "Failed to parse arguments"}`

	default:
		return fmt.Sprintf(`{"error": "Unknown tool: %s"}`, toolCall.Function.Name)
	}
}

// simulateCalculation simulates a simple calculation
func simulateCalculation(expr string) int {
	// Very basic simulation - just return a plausible result
	if expr == "25 * 4 + 10" {
		return 110
	}
	return 42 // Default answer
}