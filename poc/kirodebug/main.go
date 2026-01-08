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

	fmt.Println("=== Debugging Kiro Tool Call Response ===")

	// Initialize Kiro authenticator
	kiroAuth := kiropkg.NewAuthenticator(nil)

	// Check if authenticated
	if !kiroAuth.IsAuthenticated() {
		fmt.Println("Kiro: Not authenticated.")
		return
	}

	fmt.Println("Kiro: Already authenticated.")

	// Create provider
	kiroProvider := kiropkg.NewProvider("kiro-default", kiroAuth)

	// Test tool call request with simpler prompt
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

	req := &kiropkg.ChatRequest{
		Messages: []kiropkg.ChatMessage{
			{
				Role:    "user",
				Content: "What's the weather in Tokyo?",
			},
		},
		Model: "claude-haiku-4-5",
		Tools: tools,
	}

	fmt.Println("\n--- Sending Request ---")
	fmt.Printf("Model: %s\n", req.Model)
	fmt.Printf("Tools: %d\n", len(req.Tools))

	// Print the request structure
	reqJSON, _ := json.MarshalIndent(req, "", "  ")
	fmt.Printf("\nRequest:\n%s\n", string(reqJSON))

	fmt.Println("\n--- Waiting for Response ---")
	resp, err := kiroProvider.ChatCompletion(ctx, req)
	if err != nil {
		log.Fatalf("Request failed: %v", err)
	}

	fmt.Println("\n--- Response Received ---")

	// Print the response structure
	respJSON, _ := json.MarshalIndent(resp, "", "  ")
	fmt.Printf("\nResponse:\n%s\n", string(respJSON))

	// Analyze the response
	fmt.Println("\n--- Analysis ---")
	fmt.Printf("Response ID: %s\n", resp.ID)
	fmt.Printf("Number of choices: %d\n", len(resp.Choices))

	if len(resp.Choices) > 0 {
		choice := resp.Choices[0]
		fmt.Printf("Message role: %s\n", choice.Message.Role)
		fmt.Printf("Message content: %s\n", choice.Message.Content)
		fmt.Printf("Message content type: %T\n", choice.Message.Content)
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

	if resp.Usage != nil {
		fmt.Printf("\nUsage:\n")
		fmt.Printf("  Credit usage: %.6f\n", resp.Usage.CreditUsage)
	}
}
