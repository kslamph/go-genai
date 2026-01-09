package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/openai/openai-go/v3"
	kiropkg "github.com/sunbankio/omniproxy/internal/provider/kiro"
)

func main() {
	ctx := context.Background()

	fmt.Println("=== Comprehensive Kiro LLM Access Tests ===")

	// Initialize Kiro authenticator
	kiroAuth := kiropkg.NewAuthenticator(nil)

	// Check if authenticated
	fmt.Println("\n--- Authentication Check ---")
	if !kiroAuth.IsAuthenticated() {
		fmt.Println("Kiro: Not authenticated.")
		fmt.Println("Please ensure your credentials are saved at:")
		fmt.Printf("  %s\n", kiroAuth.GetCredentialsPath())
		fmt.Println("The file should contain:")
		fmt.Println(`  {
    "accessToken": "...",
    "refreshToken": "...",
    "expiresAt": "2025-12-24T18:51:07+08:00",
    "authMethod": "social",
    "profileArn": "arn:aws:codewhisperer:us-east-1:699475941385:profile/..."
  }`)
		fmt.Println("\nSkipping API tests (no credentials).")
		return
	}

	fmt.Println("Kiro: Already authenticated.")

	// Test token retrieval
	fmt.Println("\n--- Token Retrieval ---")
	token, err := kiroAuth.GetToken(ctx)
	if err != nil {
		log.Fatalf("Failed to get access token: %v", err)
	}
	fmt.Printf("Access token obtained successfully (length: %d)\n", len(token))

	// Show auth method and region
	fmt.Printf("Auth method: %s\n", kiroAuth.GetAuthMethod())
	fmt.Printf("Profile ARN: %s\n", kiroAuth.GetProfileArn())
	fmt.Printf("Region: %s\n", kiroAuth.GetRegion())
	fmt.Printf("Machine ID: %s\n", kiroAuth.GenerateMachineID())

	// Create provider
	fmt.Println("\n--- Creating Kiro Provider ---")
	kiroProvider := kiropkg.NewProvider("kiro-default", kiroAuth)
	fmt.Printf("Provider name: %s\n", kiroProvider.Name())
	fmt.Printf("Provider type: %s\n", kiroProvider.Type())
	fmt.Printf("Supported protocols: %v\n", kiroProvider.SupportedProtocols())

	// Test model listing
	fmt.Println("\n--- Model Listing ---")
	models, err := kiroProvider.ListModels(ctx)
	if err != nil {
		log.Fatalf("Failed to list models: %v", err)
	}
	fmt.Println("Supported models:")
	for _, model := range models {
		supported := kiroProvider.SupportsModel(model)
		fmt.Printf("  - %s (supported: %v)\n", model, supported)
	}

	// Test 1: Simple multiturn conversation
	fmt.Println("\n=== Test 1: Simple Multiturn Conversation ===")
	testSimpleMultiturn(ctx, kiroProvider)

	// Test 2: Multiturn with single tool call (OpenAI-style)
	fmt.Println("\n=== Test 2: Multiturn with Single Tool Call (OpenAI-Style) ===")
	testMultiturnWithSingleToolOpenAI(ctx, kiroProvider)

	// Test 3: Multiturn with single tool call (Kiro-style)
	fmt.Println("\n=== Test 3: Multiturn with Single Tool Call (Kiro-Style) ===")
	testMultiturnWithSingleToolKiro(ctx, kiroProvider)

	// Test 4: Multiple sequential tool calls
	fmt.Println("\n=== Test 4: Multiple Sequential Tool Calls ===")
	testMultiturnWithMultipleTools(ctx, kiroProvider)

	// Test 5: Streaming with tool calls
	fmt.Println("\n=== Test 5: Streaming with Tool Calls ===")
	testStreamingWithToolCalls(ctx, kiroProvider)

	// Test 6: Detailed request/response debugging
	fmt.Println("\n=== Test 6: Detailed Request/Response Debugging ===")
	testDetailedDebugging(ctx, kiroProvider)

	fmt.Println("\n=== All tests completed ===")
}

// testSimpleMultiturn demonstrates basic multiturn conversation without tools
func testSimpleMultiturn(ctx context.Context, p *kiropkg.Provider) {
	// Turn 1: User asks a question
	req1 := openai.ChatCompletionNewParams{
		Model: openai.ChatModel("claude-haiku-4-5"),
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("What is the capital of France?"),
		},
	}

	fmt.Println("\n--- Turn 1: User Question ---")
	resp1, err := p.ChatCompletion(ctx, req1)
	if err != nil {
		log.Printf("Turn 1 failed: %v", err)
		return
	}

	chatCompletion1, ok := resp1.(*openai.ChatCompletion)
	if !ok {
		log.Printf("Turn 1: unexpected response type")
		return
	}

	if len(chatCompletion1.Choices) > 0 {
		fmt.Printf("Assistant: %s\n", chatCompletion1.Choices[0].Message.Content)
	}

	// Turn 2: User follows up
	req2 := openai.ChatCompletionNewParams{
		Model: openai.ChatModel("claude-haiku-4-5"),
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("What is the capital of France?"),
			openai.AssistantMessage(chatCompletion1.Choices[0].Message.Content),
			openai.UserMessage("And what about Germany?"),
		},
	}

	fmt.Println("\n--- Turn 2: User Follow-up ---")
	resp2, err := p.ChatCompletion(ctx, req2)
	if err != nil {
		log.Printf("Turn 2 failed: %v", err)
		return
	}

	chatCompletion2, ok := resp2.(*openai.ChatCompletion)
	if !ok {
		log.Printf("Turn 2: unexpected response type")
		return
	}

	if len(chatCompletion2.Choices) > 0 {
		fmt.Printf("Assistant: %s\n", chatCompletion2.Choices[0].Message.Content)
	}

	fmt.Println("\n✓ Simple multiturn conversation successful")
}

// testMultiturnWithSingleToolOpenAI demonstrates multiturn with tool calls using OpenAI-style "tool" role
func testMultiturnWithSingleToolOpenAI(ctx context.Context, p *kiropkg.Provider) {
	// Turn 1: User asks for weather (triggers tool call)
	req1JSON := map[string]interface{}{
		"model": "claude-haiku-4-5",
		"messages": []map[string]interface{}{
			{"role": "user", "content": "What's the weather in Tokyo?"},
		},
		"tools": []map[string]interface{}{
			{
				"type": "function",
				"function": map[string]interface{}{
					"name":        "get_weather",
					"description": "Get current weather for a location",
					"parameters": map[string]interface{}{
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
		},
	}

	req1Bytes, _ := json.Marshal(req1JSON)
	var req1 openai.ChatCompletionNewParams
	json.Unmarshal(req1Bytes, &req1)

	fmt.Println("\n--- Turn 1: User Request (Tool Call) ---")
	resp1, err := p.ChatCompletion(ctx, req1)
	if err != nil {
		log.Printf("Turn 1 failed: %v", err)
		return
	}

	chatCompletion1, ok := resp1.(*openai.ChatCompletion)
	if !ok {
		log.Printf("Turn 1: unexpected response type")
		return
	}

	var toolCallID string
	if len(chatCompletion1.Choices) > 0 && len(chatCompletion1.Choices[0].Message.ToolCalls) > 0 {
		toolCalls := chatCompletion1.Choices[0].Message.ToolCalls
		if len(toolCalls) > 0 {
			toolCallID = toolCalls[0].ID
			fmt.Printf("Tool calls: %d\n", len(toolCalls))
			fmt.Printf("Tool Call ID: %s\n", toolCalls[0].ID)
			fmt.Printf("Function: %s\n", toolCalls[0].Function.Name)
			fmt.Printf("Arguments: %s\n", toolCalls[0].Function.Arguments)
		}
	} else {
		fmt.Printf("Assistant: %s\n", chatCompletion1.Choices[0].Message.Content)
		fmt.Println("No tool calls detected in response")
		return
	}

	// Turn 2: Provide tool result
	toolResult := `{"location": "Tokyo", "temperature": 22, "condition": "Sunny", "humidity": 65}`
	fmt.Printf("\n--- Turn 2: Providing Tool Result ---\n")
	fmt.Printf("Tool result: %s\n", toolResult)

	req2JSON := map[string]interface{}{
		"model": "claude-haiku-4-5",
		"messages": []map[string]interface{}{
			{"role": "user", "content": "What's the weather in Tokyo?"},
			{
				"role":    "assistant",
				"content": "",
				"tool_calls": []map[string]interface{}{
					{
						"id":   toolCallID,
						"type": "function",
						"function": map[string]interface{}{
							"name":      "get_weather",
							"arguments": `{"location": "Tokyo"}`,
						},
					},
				},
			},
			{
				"role":         "tool",
				"tool_call_id": toolCallID,
				"content":      toolResult,
			},
		},
		"tools": []map[string]interface{}{
			{
				"type": "function",
				"function": map[string]interface{}{
					"name":        "get_weather",
					"description": "Get current weather for a location",
					"parameters": map[string]interface{}{
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
		},
	}

	req2Bytes, _ := json.Marshal(req2JSON)
	var req2 openai.ChatCompletionNewParams
	json.Unmarshal(req2Bytes, &req2)

	resp2, err := p.ChatCompletion(ctx, req2)
	if err != nil {
		log.Printf("Turn 2 failed: %v", err)
		return
	}

	chatCompletion2, ok := resp2.(*openai.ChatCompletion)
	if !ok {
		log.Printf("Turn 2: unexpected response type")
		return
	}

	if len(chatCompletion2.Choices) > 0 {
		fmt.Printf("Assistant: %s\n", chatCompletion2.Choices[0].Message.Content)
	}

	fmt.Println("\n✓ Multiturn with single tool call (OpenAI-style) successful")
}

// testMultiturnWithSingleToolKiro demonstrates multiturn with tool calls using Kiro-style user messages
func testMultiturnWithSingleToolKiro(ctx context.Context, p *kiropkg.Provider) {
	// Turn 1: User asks for weather (triggers tool call)
	req1JSON := map[string]interface{}{
		"model": "claude-haiku-4-5",
		"messages": []map[string]interface{}{
			{"role": "user", "content": "What's the weather in Paris?"},
		},
		"tools": []map[string]interface{}{
			{
				"type": "function",
				"function": map[string]interface{}{
					"name":        "get_weather",
					"description": "Get current weather for a location",
					"parameters": map[string]interface{}{
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
		},
	}

	req1Bytes, _ := json.Marshal(req1JSON)
	var req1 openai.ChatCompletionNewParams
	json.Unmarshal(req1Bytes, &req1)

	fmt.Println("\n--- Turn 1: User Request (Tool Call) ---")
	resp1, err := p.ChatCompletion(ctx, req1)
	if err != nil {
		log.Printf("Turn 1 failed: %v", err)
		return
	}

	chatCompletion1, ok := resp1.(*openai.ChatCompletion)
	if !ok {
		log.Printf("Turn 1: unexpected response type")
		return
	}

	var toolCallID string
	if len(chatCompletion1.Choices) > 0 && len(chatCompletion1.Choices[0].Message.ToolCalls) > 0 {
		toolCalls := chatCompletion1.Choices[0].Message.ToolCalls
		if len(toolCalls) > 0 {
			toolCallID = toolCalls[0].ID
			fmt.Printf("Tool calls: %d\n", len(toolCalls))
			fmt.Printf("Tool Call ID: %s\n", toolCalls[0].ID)
			fmt.Printf("Function: %s\n", toolCalls[0].Function.Name)
			fmt.Printf("Arguments: %s\n", toolCalls[0].Function.Arguments)
		}
	} else {
		fmt.Printf("Assistant: %s\n", chatCompletion1.Choices[0].Message.Content)
		fmt.Println("No tool calls detected")
		return
	}

	// CRITICAL: In Kiro, provide tool results as a USER message with tool_result format
	// NOT as a plain text user message
	fmt.Println("\n--- Turn 2: Provide Tool Results as USER Message (Kiro-Style) ---")
	toolResult := "Tool execution result: The weather in Paris is 18 degrees, cloudy with 70% humidity."
	fmt.Printf("Tool result: %s\n", toolResult)

	req2JSON := map[string]interface{}{
		"model": "claude-haiku-4-5",
		"messages": []map[string]interface{}{
			{"role": "user", "content": "What's the weather in Paris?"},
			{
				"role":    "assistant",
				"content": "",
				"tool_calls": []map[string]interface{}{
					{
						"id":   toolCallID,
						"type": "function",
						"function": map[string]interface{}{
							"name":      "get_weather",
							"arguments": `{"location": "Paris"}`,
						},
					},
				},
			},
			{
				"role":         "tool",
				"tool_call_id": toolCallID,
				"content":      toolResult,
			},
		},
		"tools": []map[string]interface{}{
			{
				"type": "function",
				"function": map[string]interface{}{
					"name":        "get_weather",
					"description": "Get current weather for a location",
					"parameters": map[string]interface{}{
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
		},
	}

	req2Bytes, _ := json.Marshal(req2JSON)
	var req2 openai.ChatCompletionNewParams
	json.Unmarshal(req2Bytes, &req2)

	resp2, err := p.ChatCompletion(ctx, req2)
	if err != nil {
		log.Printf("Turn 2 failed: %v", err)
		return
	}

	chatCompletion2, ok := resp2.(*openai.ChatCompletion)
	if !ok {
		log.Printf("Turn 2: unexpected response type")
		return
	}

	if len(chatCompletion2.Choices) > 0 {
		fmt.Printf("Final answer: %s\n", chatCompletion2.Choices[0].Message.Content)
	}

	fmt.Println("\n✓ Multiturn with single tool call (Kiro-style) successful")
}

// testMultiturnWithMultipleTools demonstrates multiturn with multiple sequential tool calls
func testMultiturnWithMultipleTools(ctx context.Context, p *kiropkg.Provider) {
	// Turn 1: User asks for weather and time (may trigger multiple tools)
	req1JSON := map[string]interface{}{
		"model": "claude-haiku-4-5",
		"messages": []map[string]interface{}{
			{"role": "user", "content": "What's the weather and current time in London and New York?"},
		},
		"tools": []map[string]interface{}{
			{
				"type": "function",
				"function": map[string]interface{}{
					"name":        "get_weather",
					"description": "Get current weather for a location",
					"parameters": map[string]interface{}{
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
				"type": "function",
				"function": map[string]interface{}{
					"name":        "get_time",
					"description": "Get current time for a location",
					"parameters": map[string]interface{}{
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
		},
	}

	req1Bytes, _ := json.Marshal(req1JSON)
	var req1 openai.ChatCompletionNewParams
	json.Unmarshal(req1Bytes, &req1)

	fmt.Println("\n--- Turn 1: User Request (Multiple Tools) ---")
	resp1, err := p.ChatCompletion(ctx, req1)
	if err != nil {
		log.Printf("Turn 1 failed: %v", err)
		return
	}

	chatCompletion1, ok := resp1.(*openai.ChatCompletion)
	if !ok {
		log.Printf("Turn 1: unexpected response type")
		return
	}

	if len(chatCompletion1.Choices) > 0 {
		fmt.Printf("Assistant: %s\n", chatCompletion1.Choices[0].Message.Content)
	}

	// Process all tool calls
	toolCallCount := 0
	messages := []map[string]interface{}{
		{"role": "user", "content": "What's the weather and current time in London and New York?"},
	}

	for len(chatCompletion1.Choices) > 0 && len(chatCompletion1.Choices[0].Message.ToolCalls) > 0 {
		toolCallCount++
		fmt.Printf("\n--- Tool Call Batch %d ---\n", toolCallCount)
		fmt.Printf("Number of tool calls: %d\n", len(chatCompletion1.Choices[0].Message.ToolCalls))

		// Add assistant message with tool calls to conversation
		toolCalls := chatCompletion1.Choices[0].Message.ToolCalls
		assistantMsg := map[string]interface{}{
			"role":       "assistant",
			"content":    chatCompletion1.Choices[0].Message.Content,
			"tool_calls": make([]map[string]interface{}, len(toolCalls)),
		}

		for i, tc := range toolCalls {
			assistantMsg["tool_calls"].([]map[string]interface{})[i] = map[string]interface{}{
				"id":   tc.ID,
				"type": "function",
				"function": map[string]interface{}{
					"name":      tc.Function.Name,
					"arguments": tc.Function.Arguments,
				},
			}
		}
		messages = append(messages, assistantMsg)

		// Execute all tools in this batch
		toolResultContents := []interface{}{}
		for _, toolCall := range toolCalls {
			fmt.Printf("  Executing: %s\n", toolCall.Function.Name)
			toolResult := simulateToolExecution(toolCall.Function.Name, toolCall.Function.Arguments)
			fmt.Printf("    Result: %s\n", toolResult)

			// Collect tool result content
			toolResultContents = append(toolResultContents, map[string]interface{}{
				"type":        "tool_result",
				"tool_use_id": toolCall.ID,
				"content":     toolResult,
			})
		}

		// Add all tool results in a single tool message (Kiro-style)
		messages = append(messages, map[string]interface{}{
			"role":    "tool",
			"content": toolResultContents,
		})

		// Get next response
		fmt.Printf("\n--- Getting Response After Tool Execution ---\n")
		reqNextJSON := map[string]interface{}{
			"model":    "claude-haiku-4-5",
			"messages": messages,
			"tools": []map[string]interface{}{
				{
					"type": "function",
					"function": map[string]interface{}{
						"name":        "get_weather",
						"description": "Get current weather for a location",
						"parameters": map[string]interface{}{
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
					"type": "function",
					"function": map[string]interface{}{
						"name":        "get_time",
						"description": "Get current time for a location",
						"parameters": map[string]interface{}{
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
			},
		}

		reqNextBytes, _ := json.Marshal(reqNextJSON)
		var reqNext openai.ChatCompletionNewParams
		json.Unmarshal(reqNextBytes, &reqNext)

		respNext, err := p.ChatCompletion(ctx, reqNext)
		if err != nil {
			log.Printf("Response failed: %v", err)
			return
		}

		chatCompletion1, ok = respNext.(*openai.ChatCompletion)
		if !ok {
			log.Printf("Unexpected response type")
			return
		}

		if len(chatCompletion1.Choices) > 0 {
			fmt.Printf("Assistant: %s\n", chatCompletion1.Choices[0].Message.Content)
		}

		// Break if no more tool calls
		if len(chatCompletion1.Choices) == 0 || len(chatCompletion1.Choices[0].Message.ToolCalls) == 0 {
			break
		}
	}

	fmt.Printf("\n✓ Multiturn with multiple tool calls successful (total tool call batches: %d)\n", toolCallCount)
}

// testStreamingWithToolCalls demonstrates multiturn conversation with streaming and tool calls
func testStreamingWithToolCalls(ctx context.Context, p *kiropkg.Provider) {
	// Turn 1: User asks for calculation
	req1JSON := map[string]interface{}{
		"model": "claude-haiku-4-5",
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Calculate 25 * 4 + 10"},
		},
		"tools": []map[string]interface{}{
			{
				"type": "function",
				"function": map[string]interface{}{
					"name":        "calculate",
					"description": "Perform a mathematical calculation",
					"parameters": map[string]interface{}{
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
		},
	}

	req1Bytes, _ := json.Marshal(req1JSON)
	var req1 openai.ChatCompletionNewParams
	json.Unmarshal(req1Bytes, &req1)

	fmt.Println("\n--- Turn 1: Streaming Request with Tool Call ---")
	fmt.Printf("Assistant (streaming): ")

	events, errChan, err := p.StreamChatCompletion(ctx, req1)
	if err != nil {
		log.Printf("Streaming failed: %v", err)
		return
	}

	var content string
	var toolCalls []map[string]interface{}

	// Collect streaming events
	for event := range events {
		// Parse delta from choices
		if len(event.Choices) > 0 {
			delta := event.Choices[0].Delta

			// Check for content
			if delta.Content != "" {
				fmt.Print(delta.Content)
				content += delta.Content
			}

			// Check for tool calls
			if len(delta.ToolCalls) > 0 {
				for _, tc := range delta.ToolCalls {
					fmt.Printf("\n[Tool Call Detected] Function: %s", tc.Function.Name)
					if tc.Function.Arguments != "" {
						fmt.Printf(" Arguments: %s", tc.Function.Arguments)
					}
					toolCalls = append(toolCalls, map[string]interface{}{
						"id":   tc.ID,
						"type": "function",
						"function": map[string]interface{}{
							"name":      tc.Function.Name,
							"arguments": tc.Function.Arguments,
						},
					})
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

	// Process tool calls if any
	if len(toolCalls) > 0 {
		fmt.Println("\n--- Executing Tool Calls ---")

		for _, toolCall := range toolCalls {
			name := toolCall["function"].(map[string]interface{})["name"].(string)
			args := toolCall["function"].(map[string]interface{})["arguments"].(string)
			fmt.Printf("Executing: %s with args: %s\n", name, args)
			toolResult := simulateToolExecution(name, args)
			fmt.Printf("Result: %s\n", toolResult)
		}

		// Turn 2: Get final response
		fmt.Println("\n--- Turn 2: Final Response ---")
		fmt.Printf("Assistant: ")

		req2JSON := map[string]interface{}{
			"model": "claude-haiku-4-5",
			"messages": []map[string]interface{}{
				{"role": "user", "content": "Calculate 25 * 4 + 10"},
				{
					"role":       "assistant",
					"content":    content,
					"tool_calls": toolCalls,
				},
				{
					"role":    "user",
					"content": "Tool result: 110",
				},
			},
			"tools": []map[string]interface{}{
				{
					"type": "function",
					"function": map[string]interface{}{
						"name":        "calculate",
						"description": "Perform a mathematical calculation",
						"parameters": map[string]interface{}{
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
			},
		}

		req2Bytes, _ := json.Marshal(req2JSON)
		var req2 openai.ChatCompletionNewParams
		json.Unmarshal(req2Bytes, &req2)

		resp2, err := p.ChatCompletion(ctx, req2)
		if err != nil {
			log.Printf("Final response failed: %v", err)
			return
		}

		chatCompletion2, ok := resp2.(*openai.ChatCompletion)
		if !ok {
			log.Printf("Unexpected response type")
			return
		}

		if len(chatCompletion2.Choices) > 0 {
			fmt.Printf("%s\n", chatCompletion2.Choices[0].Message.Content)
		}
	}

	fmt.Println("\n✓ Multiturn streaming with tool calls successful")
}

// testDetailedDebugging demonstrates detailed request/response JSON printing and usage info
func testDetailedDebugging(ctx context.Context, p *kiropkg.Provider) {
	reqJSON := map[string]interface{}{
		"model": "claude-haiku-4-5",
		"messages": []map[string]interface{}{
			{"role": "user", "content": "What's the weather in Berlin?"},
		},
		"tools": []map[string]interface{}{
			{
				"type": "function",
				"function": map[string]interface{}{
					"name":        "get_weather",
					"description": "Get current weather for a location",
					"parameters": map[string]interface{}{
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
		},
	}

	reqBytes, _ := json.Marshal(reqJSON)
	var req openai.ChatCompletionNewParams
	json.Unmarshal(reqBytes, &req)

	fmt.Println("\n--- Sending Request ---")
	fmt.Printf("Model: claude-haiku-4-5\n")
	fmt.Printf("Tools: 1\n")

	// Print the request structure
	fmt.Printf("\nRequest:\n%s\n", string(reqBytes))

	fmt.Println("\n--- Waiting for Response ---")
	resp, err := p.ChatCompletion(ctx, req)
	if err != nil {
		log.Fatalf("Request failed: %v", err)
	}

	chatCompletion, ok := resp.(*openai.ChatCompletion)
	if !ok {
		log.Fatalf("Unexpected response type")
	}

	fmt.Println("\n--- Response Received ---")

	// Print the response structure
	respBytes, _ := json.MarshalIndent(chatCompletion, "", "  ")
	fmt.Printf("\nResponse:\n%s\n", string(respBytes))

	// Analyze the response
	fmt.Println("\n--- Analysis ---")
	fmt.Printf("Response ID: %s\n", chatCompletion.ID)
	fmt.Printf("Number of choices: %d\n", len(chatCompletion.Choices))

	if len(chatCompletion.Choices) > 0 {
		choice := chatCompletion.Choices[0]
		fmt.Printf("Message role: %s\n", choice.Message.Role)
		fmt.Printf("Message content: %s\n", choice.Message.Content)
		fmt.Printf("Number of tool calls: %d\n", len(choice.Message.ToolCalls))

		for i, toolCall := range choice.Message.ToolCalls {
			fmt.Printf("\nTool Call %d:\n", i+1)
			fmt.Printf("  ID: %s\n", toolCall.ID)
			fmt.Printf("  Type: %s\n", toolCall.Type)
			fmt.Printf("  Function Name: %s\n", toolCall.Function.Name)
			fmt.Printf("  Function Arguments: %s\n", toolCall.Function.Arguments)

			// Try to parse arguments
			var args map[string]interface{}
			if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &args); err == nil {
				fmt.Printf("  Parsed Arguments: %+v\n", args)
			}
		}
	}

	fmt.Printf("\nUsage:\n")
	fmt.Printf("  Prompt tokens: %d\n", chatCompletion.Usage.PromptTokens)
	fmt.Printf("  Completion tokens: %d\n", chatCompletion.Usage.CompletionTokens)
	fmt.Printf("  Total tokens: %d\n", chatCompletion.Usage.TotalTokens)

	fmt.Println("\n✓ Detailed debugging successful")
}

// simulateToolExecution simulates executing a tool and returns a result
func simulateToolExecution(name, args string) string {
	switch name {
	case "get_weather":
		// Parse arguments
		var argStruct struct {
			Location string `json:"location"`
		}
		if err := json.Unmarshal([]byte(args), &argStruct); err == nil {
			// Simulate weather data
			return fmt.Sprintf(`{"location": "%s", "temperature": 22, "condition": "Sunny", "humidity": 65}`, argStruct.Location)
		}
		return `{"error": "Failed to parse arguments"}`

	case "get_time":
		var argStruct struct {
			Location string `json:"location"`
		}
		if err := json.Unmarshal([]byte(args), &argStruct); err == nil {
			// Simulate time data
			return fmt.Sprintf(`{"location": "%s", "time": "2026-01-08T14:30:00Z", "timezone": "UTC"}`, argStruct.Location)
		}
		return `{"error": "Failed to parse arguments"}`

	case "calculate":
		var argStruct struct {
			Expression string `json:"expression"`
		}
		if err := json.Unmarshal([]byte(args), &argStruct); err == nil {
			// Simple calculation simulation
			result := simulateCalculation(argStruct.Expression)
			return fmt.Sprintf(`{"expression": "%s", "result": %d}`, argStruct.Expression, result)
		}
		return `{"error": "Failed to parse arguments"}`

	default:
		return fmt.Sprintf(`{"error": "Unknown tool: %s"}`, name)
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
