package main

import (
	"context"
	"fmt"
	"log"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	iflowprovider "github.com/sunbankio/omniproxy/internal/provider/iflow"
)

func main() {
	ctx := context.Background()

	fmt.Println("=== Testing iFlow LLM Access ===")

	// Initialize iFlow authenticator
	iflowAuth := iflowprovider.NewAuthenticator(nil)

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
	apiKey, err := iflowAuth.GetToken(ctx)
	if err != nil {
		log.Fatalf("Failed to get API key: %v", err)
	}
	if apiKey == "" {
		log.Fatalf("Failed to get API key: empty API key")
	}
	fmt.Printf("API key obtained successfully\n")

	// Create OpenAI client configured for iFlow using bearer token
	client := openai.NewClient(
		option.WithAPIKey(apiKey),
		option.WithBaseURL("https://apis.iflow.cn/v1"),
	)

	// Test with qwen3-max model
	model := "glm-4.6"

	// Streaming completion
	fmt.Printf("\nTesting streaming completion with model: %s\n", model)
	fmt.Print("Streamed Response: ")

	stream := client.Chat.Completions.NewStreaming(ctx, openai.ChatCompletionNewParams{
		Model:    openai.ChatModel(model),
		Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage("Write a short Go function to reverse a string.")},
	})

	for stream.Next() {
		chunk := stream.Current()
		if len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content != "" {
			fmt.Print(chunk.Choices[0].Delta.Content)
		}
	}

	if err := stream.Err(); err != nil {
		log.Printf("Stream error: %v", err)
	}

	fmt.Println("\n=== All tests completed ===")
}
