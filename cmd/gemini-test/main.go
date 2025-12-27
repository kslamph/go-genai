package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"google.golang.org/genai"
)

func main() {
	ctx := context.Background()

	// Configuration to point to local omniproxy
	// The proxy expects requests at /{provider}/v1beta/...
	// We use 'gemini' provider.
	// BaseURL should be http://localhost:8143/gemini/
	// The SDK appends API version (v1beta) and method.
	
	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey: "test-api-key", // Dummy key, proxy should handle it
		HTTPOptions: genai.HTTPOptions{
			BaseURL: "http://localhost:8143/",
			APIVersion: "v1beta",
		},
	})
	if err != nil {
		log.Fatalf("Failed to create client: %v", err)
	}

	model := "gemini-2.5-flash"

	// Construct a simple text request
	contents := []*genai.Content{
		{
			Role: "user",
			Parts: []*genai.Part{
				{Text: "Hello, please reply with 'Hello from Gemini' and nothing else."},
			},
		},
	}

	// 1. Non-streaming
	fmt.Println("=== Testing Non-Streaming ===")
	resp, err := client.Models.GenerateContent(ctx, model, contents, nil)
	if err != nil {
		log.Printf("Non-streaming error: %v", err)
	} else {
		// Verify response structure
		jsonResp, _ := json.MarshalIndent(resp, "", "  ")
		fmt.Printf("Response: %s\n", string(jsonResp))
		
		if len(resp.Candidates) > 0 && len(resp.Candidates[0].Content.Parts) > 0 {
			fmt.Printf("Text: %s\n", resp.Candidates[0].Content.Parts[0].Text)
		}
	}

	// 2. Streaming
	fmt.Println("\n=== Testing Streaming ===")
	iter := client.Models.GenerateContentStream(ctx, model, contents, nil)
	
	chunkCount := 0
	for resp, err := range iter {
		if err != nil {
			log.Printf("Stream error: %v", err)
			break
		}
		chunkCount++
		// fmt.Printf("Chunk %d received\n", chunkCount)
		
		if len(resp.Candidates) > 0 && len(resp.Candidates[0].Content.Parts) > 0 {
			fmt.Printf("Chunk %d Text: %s\n", chunkCount, resp.Candidates[0].Content.Parts[0].Text)
		}
	}
	fmt.Printf("Stream completed with %d chunks\n", chunkCount)
}
