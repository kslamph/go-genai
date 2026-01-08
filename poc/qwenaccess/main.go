package main

import (
	"context"
	"fmt"
	"log"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	qwenprovider "github.com/sunbankio/omniproxy/internal/provider/qwen"
)

func main() {
	ctx := context.Background()

	fmt.Println("=== Testing Qwen LLM Access ===")

	// Initialize Qwen authenticator
	qwenAuth := qwenprovider.NewAuthenticator(nil)

	// Check if authenticated
	if !qwenAuth.IsAuthenticated() {
		fmt.Println("Qwen: Not authenticated. Starting authentication flow...")
		if err := qwenAuth.Authenticate(ctx); err != nil {
			log.Fatalf("Authentication failed: %v", err)
		}
		fmt.Println("Authentication successful!")
	} else {
		fmt.Println("Qwen: Already authenticated.")
	}

	// Get access token
	token, err := qwenAuth.GetToken(ctx)
	if err != nil {
		log.Fatalf("Failed to get access token: %v", err)
	}
	fmt.Printf("Access token obtained successfully\n")

	// Create OpenAI client configured for Qwen
	client := openai.NewClient(
		option.WithAPIKey(token),
		option.WithBaseURL("https://portal.qwen.ai/v1"),
	)

	// Test with qwen3-coder-flash model
	model := "qwen3-coder-flash"

	// Test 1: Simple completion
	fmt.Printf("\nTesting simple completion with model: %s\n", model)
	resp, err := client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model:     openai.ChatModel(model),
		Messages:  []openai.ChatCompletionMessageParamUnion{openai.UserMessage("Explain Go pointers in one sentence.")},
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
	fmt.Println("\nDone.")

	// Test 3: Code generation
	fmt.Printf("\nTesting code generation with model: %s\n", model)
	codeResp, err := client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model:    openai.ChatModel(model),
		Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage("Write a Go function that implements binary search for a sorted slice of integers.")},
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