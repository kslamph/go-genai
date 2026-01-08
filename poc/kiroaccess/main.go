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

	fmt.Println("=== Testing Kiro LLM Access (OpenAI-Compatible) ===")

	// Initialize Kiro authenticator
	kiroAuth := kiropkg.NewAuthenticator(nil)

	// Check if authenticated
	fmt.Println("Checking authentication status...")
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
	fmt.Println("\n--- Testing Token Retrieval ---")
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
	fmt.Println("\n--- Testing Model Listing ---")
	models, err := kiroProvider.ListModels(ctx)
	if err != nil {
		log.Fatalf("Failed to list models: %v", err)
	}
	fmt.Println("Supported models:")
	for _, model := range models {
		supported := kiroProvider.SupportsModel(model)
		fmt.Printf("  - %s (supported: %v)\n", model, supported)
	}

	// Test multiturn conversation
	fmt.Println("\n--- Testing Multiturn Conversation ---")
	testMultiturnConversation(ctx, kiroProvider)

	// Test multiturn with tool calls
	fmt.Println("\n--- Testing Multiturn with Tool Calls ---")
	testMultiturnToolCalls(ctx, kiroProvider)

	fmt.Println("\n=== All tests completed ===")
}

// testMultiturnConversation tests a simple multi-turn conversation using OpenAI-compatible interface
func testMultiturnConversation(ctx context.Context, p *kiropkg.Provider) {
	// Turn 1: User asks a question
	fmt.Println("Turn 1: User asks 'What is the capital of France?'")

	req1 := openai.ChatCompletionNewParams{
		Model: openai.ChatModel("claude-haiku-4-5"),
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("What is the capital of France?"),
		},
	}

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
		fmt.Printf("Turn 1 Assistant: %s\n", chatCompletion1.Choices[0].Message.Content)
	}

	// Turn 2: Follow-up question
	fmt.Println("\nTurn 2: User asks 'What language is spoken there?'")

	req2 := openai.ChatCompletionNewParams{
		Model: openai.ChatModel("claude-haiku-4-5"),
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("What is the capital of France?"),
			openai.AssistantMessage(chatCompletion1.Choices[0].Message.Content),
			openai.UserMessage("What language is spoken there?"),
		},
	}

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
		fmt.Printf("Turn 2 Assistant: %s\n", chatCompletion2.Choices[0].Message.Content)
	}
}

// testMultiturnToolCalls tests multiturn with tool calls using OpenAI-compatible interface
func testMultiturnToolCalls(ctx context.Context, p *kiropkg.Provider) {
	// Turn 1: User asks for weather
	fmt.Println("Turn 1: User asks 'What's the weather in Tokyo?'")

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
			fmt.Printf("Turn 1 Assistant requested tool: %s(%s)\n", toolCalls[0].Function.Name, toolCalls[0].Function.Arguments)
		}
	} else {
		fmt.Printf("Turn 1 Assistant (no tool): %s\n", chatCompletion1.Choices[0].Message.Content)
		return
	}

	// Turn 2: Provide tool result
	fmt.Println("\nTurn 2: Providing tool result")

	toolResult := `{"location": "Tokyo", "temperature": 22, "condition": "Sunny", "humidity": 65}`
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
						"id":    toolCallID,
						"type":  "function",
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
		fmt.Printf("Turn 2 Assistant: %s\n", chatCompletion2.Choices[0].Message.Content)
	}

	// Turn 3: Follow-up
	fmt.Println("\nTurn 3: User asks 'What about Paris?'")

	req3JSON := map[string]interface{}{
		"model": "claude-haiku-4-5",
		"messages": []map[string]interface{}{
			{"role": "user", "content": "What's the weather in Tokyo?"},
			{
				"role":    "assistant",
				"content": "",
				"tool_calls": []map[string]interface{}{
					{
						"id":    toolCallID,
						"type":  "function",
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
			{"role": "assistant", "content": chatCompletion2.Choices[0].Message.Content},
			{"role": "user", "content": "What about Paris?"},
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

	req3Bytes, _ := json.Marshal(req3JSON)
	var req3 openai.ChatCompletionNewParams
	json.Unmarshal(req3Bytes, &req3)

	resp3, err := p.ChatCompletion(ctx, req3)
	if err != nil {
		log.Printf("Turn 3 failed: %v", err)
		return
	}

	chatCompletion3, ok := resp3.(*openai.ChatCompletion)
	if !ok {
		log.Printf("Turn 3: unexpected response type")
		return
	}

	if len(chatCompletion3.Choices) > 0 {
		fmt.Printf("Turn 3 Assistant: %s\n", chatCompletion3.Choices[0].Message.Content)
	}
}
