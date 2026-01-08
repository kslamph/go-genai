package main

import (
	"context"
	"fmt"
	"log"

	kiropkg "github.com/sunbankio/omniproxy/internal/provider/kiro"
)

func main() {
	ctx := context.Background()

	fmt.Println("=== Test 1: Simple Multiturn Conversation ===")

	// Initialize Kiro authenticator
	kiroAuth := kiropkg.NewAuthenticator(nil)

	// Check if authenticated
	fmt.Println("Checking authentication status...")
	if !kiroAuth.IsAuthenticated() {
		fmt.Println("Kiro: Not authenticated.")
		return
	}

	fmt.Println("Kiro: Already authenticated.")

	// Create provider
	kiroProvider := kiropkg.NewProvider("kiro-default", kiroAuth)

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

	resp, err := kiroProvider.ChatCompletion(ctx, &kiropkg.ChatRequest{
		Messages: messages,
		Model:    "claude-haiku-4-5",
	})
	if err != nil {
		log.Fatalf("Turn 1 failed: %v", err)
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

	resp2, err := kiroProvider.ChatCompletion(ctx, &kiropkg.ChatRequest{
		Messages: messages,
		Model:    "claude-haiku-4-5",
	})
	if err != nil {
		log.Fatalf("Turn 2 failed: %v", err)
	}

	assistantMsg2 := resp2.Choices[0].Message
	fmt.Printf("Assistant: %s\n", assistantMsg2.Content)

	fmt.Println("\n✓ Simple multiturn conversation successful")
}