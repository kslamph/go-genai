package main

import (
	"context"
	"fmt"
	"log"

	kiropkg "github.com/sunbankio/omniproxy/internal/provider/kiro"
)

func main() {
	ctx := context.Background()

	fmt.Println("=== Testing Kiro LLM Access ===")

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

	// Test simple completion
	fmt.Println("\n--- Testing Simple Completion ---")
	testSimpleCompletion(ctx, kiroProvider)

	// Test tool call request
	fmt.Println("\n--- Testing Tool Call Request ---")
	testToolCallRequest(ctx, kiroProvider)

	// Test streaming completion
	fmt.Println("\n--- Testing Streaming Completion ---")
	testStreamingCompletion(ctx, kiroProvider)

	fmt.Println("\n=== All tests completed ===")
}

func testSimpleCompletion(ctx context.Context, p *kiropkg.Provider) {
	req := &kiropkg.ChatRequest{
		Messages: []kiropkg.ChatMessage{
			{
				Role:    "user",
				Content: "Explain Go pointers in one sentence.",
			},
		},
		Model: "claude-haiku-4-5",
	}

	fmt.Printf("Sending request with model: %s\n", req.Model)

	resp, err := p.ChatCompletion(ctx, req)
	if err != nil {
		log.Printf("Simple completion failed: %v", err)
		return
	}

	fmt.Printf("Response ID: %s\n", resp.ID)
	if len(resp.Choices) > 0 {
		fmt.Printf("Response content: %s\n", resp.Choices[0].Message.Content)
	}
	if resp.Usage != nil && resp.Usage.CreditUsage > 0 {
		fmt.Printf("Credit usage: %.6f\n", resp.Usage.CreditUsage)
	}
}

func testToolCallRequest(ctx context.Context, p *kiropkg.Provider) {
	req := &kiropkg.ChatRequest{
		Messages: []kiropkg.ChatMessage{
			{
				Role:    "user",
				Content: "What's the weather in Tokyo?",
			},
		},
		Model: "claude-haiku-4-5",
		Tools: []kiropkg.Tool{
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
		},
	}

	fmt.Printf("Sending tool call request with model: %s\n", req.Model)

	resp, err := p.ChatCompletion(ctx, req)
	if err != nil {
		log.Printf("Tool call request failed: %v", err)
		return
	}

	fmt.Printf("Response ID: %s\n", resp.ID)
	if len(resp.Choices) > 0 {
		fmt.Printf("Response content: %s\n", resp.Choices[0].Message.Content)
	}
}

func testStreamingCompletion(ctx context.Context, p *kiropkg.Provider) {
	req := &kiropkg.ChatRequest{
		Messages: []kiropkg.ChatMessage{
			{
				Role:    "user",
				Content: "Write a short Go function to reverse a string.",
			},
		},
		Model:  "claude-haiku-4-5",
		Stream: true,
	}

	fmt.Printf("Sending streaming request with model: %s\n", req.Model)
	fmt.Print("Streamed Response: ")

	events, errChan, err := p.StreamChatCompletion(ctx, req)
	if err != nil {
		log.Printf("Streaming request failed: %v", err)
		return
	}

	for event := range events {
		if event.Type == "content" {
			if content, ok := event.Content["content"].(string); ok {
				fmt.Print(content)
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

	fmt.Println("\nStream completed.")
}
