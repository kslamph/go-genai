package main

import (
	"context"
	"fmt"
	"log"

	"github.com/sashabaranov/go-openai"
	"github.com/sunbankio/omniproxy/auth"
)

func main() {
	ctx := context.Background()

	fmt.Println("=== Testing iFlow LLM Access ===")

	// Initialize iFlow authenticator
	iflowAuth := auth.NewIFlowAuthenticator(nil)

	// Check if authenticated
	if !iflowAuth.IsAuthenticated() {
		fmt.Println("iFlow: Not authenticated. Starting authentication flow...")
		if err := iflowAuth.Authenticate(ctx); err != nil {
			log.Fatalf("Authentication failed: %v", err)
		}
		fmt.Println("Authentication successful!")
	} else {
		fmt.Println("iFlow: Already authenticated.")
	}

	// Get API key for bearer token authentication
	apiKey := iflowAuth.GetAPIKey()
	if apiKey == "" {
		log.Fatalf("Failed to get API key: empty API key")
	}
	fmt.Printf("API key obtained successfully\n")

	// Create OpenAI client configured for iFlow using bearer token
	clientConfig := openai.DefaultConfig(apiKey)
	clientConfig.BaseURL = "https://apis.iflow.cn/v1" // Set iFlow API endpoint
	client := openai.NewClientWithConfig(clientConfig)

	// Test with qwen3-max model
	model := "glm-4.7"

	// Test 1: Simple completion
	fmt.Printf("\nTesting simple completion with model: %s\n", model)
	resp, err := client.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model: model,
		Messages: []openai.ChatCompletionMessage{
			{
				Role:    openai.ChatMessageRoleUser,
				Content: "Explain Go pointers in one sentence.",
			},
		},
	})

	if err != nil {
		log.Printf("Chat completion failed: %v", err)
		return
	}

	if len(resp.Choices) > 0 {
		fmt.Printf("Response: %s\n", resp.Choices[0].Message.Content)
	}

	// Test 2: Streaming completion
	fmt.Printf("\nTesting streaming completion with model: %s\n", model)
	fmt.Print("Streamed Response: ")

	stream, err := client.CreateChatCompletionStream(ctx, openai.ChatCompletionRequest{
		Model: model,
		Messages: []openai.ChatCompletionMessage{
			{
				Role:    openai.ChatMessageRoleUser,
				Content: "Write a short Go function to reverse a string.",
			},
		},
	})

	if err != nil {
		log.Printf("Stream creation failed: %v", err)
		return
	}
	defer stream.Close()

	for {
		resp, err := stream.Recv()
		if err != nil {
			// NOTE: iFlow API deliberately sends EOF error at end of stream
			// This is expected behavior and should be handled gracefully in production
			log.Printf("Stream failed: %v", err)
			break
		}

		if len(resp.Choices) > 0 {
			fmt.Print(resp.Choices[0].Delta.Content)
		}
		fmt.Println("\nDone.")
	}

	// Test 3: Code generation
	fmt.Printf("\nTesting code generation with model: %s\n", model)
	codeResp, err := client.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model: model,
		Messages: []openai.ChatCompletionMessage{
			{
				Role:    openai.ChatMessageRoleUser,
				Content: "Write a Go function that implements binary search for a sorted slice of integers.",
			},
		},
	})

	if err != nil {
		log.Printf("Code generation failed: %v", err)
		return
	}

	if len(codeResp.Choices) > 0 {
		fmt.Printf("Generated Code:\n%s\n", codeResp.Choices[0].Message.Content)
	}

	fmt.Println("\n=== All tests completed ===")
}
