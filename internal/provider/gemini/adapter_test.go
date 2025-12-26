package gemini

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"google.golang.org/genai"
)

// TestFromGeminiResponseWithThoughtSignature verifies that thought_signature is preserved in extra_content
func TestFromGeminiResponseWithThoughtSignature(t *testing.T) {
	// Create a mock Gemini response with thought_signature
	geminiResp := &genai.GenerateContentResponse{
		ResponseID: "test-response-123",
		CreateTime: time.Time{},
		Candidates: []*genai.Candidate{
			{
				Content: &genai.Content{
					Role: "model",
					Parts: []*genai.Part{
						{
							FunctionCall: &genai.FunctionCall{
								ID:   "call_abc123",
								Name: "codebase_search",
								Args: map[string]any{
									"query": "test query",
								},
							},
							ThoughtSignature: []byte("test_signature_value_12345"),
						},
					},
				},
				FinishReason: genai.FinishReasonStop,
			},
		},
	}

	// Convert using FromGeminiResponse
	result := FromGeminiResponse(geminiResp, "gemini-3-pro-preview")

	// Verify the result is a CustomChatCompletionResponse
	if result == nil {
		t.Fatal("FromGeminiResponse returned nil")
	}

	// Marshal to JSON to verify the structure
	jsonData, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		t.Fatalf("Failed to marshal result: %v", err)
	}

	t.Logf("Response JSON:\n%s", string(jsonData))

	// Parse JSON to verify extra_content is present
	var resultMap map[string]interface{}
	if err := json.Unmarshal(jsonData, &resultMap); err != nil {
		t.Fatalf("Failed to unmarshal result: %v", err)
	}

	// Navigate to tool_calls
	choices, ok := resultMap["choices"].([]interface{})
	if !ok || len(choices) == 0 {
		t.Fatal("No choices in response")
	}

	choice := choices[0].(map[string]interface{})
	message, ok := choice["message"].(map[string]interface{})
	if !ok {
		t.Fatal("No message in choice")
	}

	toolCalls, ok := message["tool_calls"].([]interface{})
	if !ok || len(toolCalls) == 0 {
		t.Fatal("No tool_calls in message")
	}

	toolCall := toolCalls[0].(map[string]interface{})

	// Verify extra_content exists
	extraContent, ok := toolCall["extra_content"]
	if !ok {
		t.Fatal("extra_content not found in tool_call")
	}

	// Verify extra_content structure
	extraMap, ok := extraContent.(map[string]interface{})
	if !ok {
		t.Fatal("extra_content is not a map")
	}

	google, ok := extraMap["google"].(map[string]interface{})
	if !ok {
		t.Fatal("google field not found in extra_content")
	}

	signature, ok := google["thought_signature"].(string)
	if !ok {
		t.Fatal("thought_signature not found in google field")
	}

	// Decode base64 and verify
	decodedSig, err := base64.StdEncoding.DecodeString(signature)
	if err != nil {
		t.Fatalf("Failed to decode base64 thought_signature: %v", err)
	}

	if string(decodedSig) != "test_signature_value_12345" {
		t.Errorf("Expected thought_signature 'test_signature_value_12345', got '%s'", string(decodedSig))
	}

	// Verify other fields
	if toolCall["id"] != "call_abc123" {
		t.Errorf("Expected tool_call id 'call_abc123', got '%v'", toolCall["id"])
	}

	function := toolCall["function"].(map[string]interface{})
	if function["name"] != "codebase_search" {
		t.Errorf("Expected function name 'codebase_search', got '%v'", function["name"])
	}

	t.Logf("✓ Successfully verified thought_signature preservation in extra_content")
}

// TestFromGeminiResponseWithoutThoughtSignature verifies normal responses work without thought_signature
func TestFromGeminiResponseWithoutThoughtSignature(t *testing.T) {
	// Create a mock Gemini response without thought_signature
	geminiResp := &genai.GenerateContentResponse{
		ResponseID: "test-response-456",
		CreateTime: time.Time{},
		Candidates: []*genai.Candidate{
			{
				Content: &genai.Content{
					Role: "model",
					Parts: []*genai.Part{
						{
							FunctionCall: &genai.FunctionCall{
								ID:   "call_xyz789",
								Name: "get_weather",
								Args: map[string]any{
									"location": "Paris",
								},
							},
							// No ThoughtSignature
						},
					},
				},
				FinishReason: genai.FinishReasonStop,
			},
		},
	}

	// Convert using FromGeminiResponse
	result := FromGeminiResponse(geminiResp, "gemini-3-flash-preview")

	// Verify the result is a CustomChatCompletionResponse
	if result == nil {
		t.Fatal("FromGeminiResponse returned nil")
	}

	// Marshal to JSON
	jsonData, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Failed to marshal result: %v", err)
	}

	t.Logf("Response JSON (no signature):\n%s", string(jsonData))

	// Parse JSON
	var resultMap map[string]interface{}
	if err := json.Unmarshal(jsonData, &resultMap); err != nil {
		t.Fatalf("Failed to unmarshal result: %v", err)
	}

	// Navigate to tool_calls
	choices := resultMap["choices"].([]interface{})
	choice := choices[0].(map[string]interface{})
	message := choice["message"].(map[string]interface{})
	toolCalls := message["tool_calls"].([]interface{})
	toolCall := toolCalls[0].(map[string]interface{})

	// Verify extra_content does NOT exist (optional, not required)
	extraContent, ok := toolCall["extra_content"]
	if ok {
		t.Logf("extra_content present (unexpected but not an error): %+v", extraContent)
	} else {
		t.Logf("✓ No extra_content in response (as expected)")
	}

	// Verify other fields are correct
	if toolCall["id"] != "call_xyz789" {
		t.Errorf("Expected tool_call id 'call_xyz789', got '%v'", toolCall["id"])
	}

	t.Logf("✓ Successfully verified response without thought_signature")
}

// TestFromGeminiResponseWithParallelFunctionCalls verifies parallel function calls
func TestFromGeminiResponseWithParallelFunctionCalls(t *testing.T) {
	// Create a mock Gemini response with parallel function calls
	// According to docs, only the first function call should have thought_signature
	geminiResp := &genai.GenerateContentResponse{
		ResponseID: "test-response-789",
		CreateTime: time.Time{},
		Candidates: []*genai.Candidate{
			{
				Content: &genai.Content{
					Role: "model",
					Parts: []*genai.Part{
						{
							FunctionCall: &genai.FunctionCall{
								ID:   "call_1",
								Name: "get_weather",
								Args: map[string]any{
									"location": "Paris",
								},
							},
							ThoughtSignature: []byte("signature_for_first_call"),
						},
						{
							FunctionCall: &genai.FunctionCall{
								ID:   "call_2",
								Name: "get_weather",
								Args: map[string]any{
									"location": "London",
								},
							},
							// No ThoughtSignature for parallel second call
						},
					},
				},
				FinishReason: genai.FinishReasonStop,
			},
		},
	}

	// Convert using FromGeminiResponse
	result := FromGeminiResponse(geminiResp, "gemini-3-pro-preview")

	if result == nil {
		t.Fatal("FromGeminiResponse returned nil")
	}

	// Marshal to JSON
	jsonData, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		t.Fatalf("Failed to marshal result: %v", err)
	}

	t.Logf("Response JSON (parallel calls):\n%s", string(jsonData))

	// Parse JSON
	var resultMap map[string]interface{}
	if err := json.Unmarshal(jsonData, &resultMap); err != nil {
		t.Fatalf("Failed to unmarshal result: %v", err)
	}

	// Navigate to tool_calls
	choices := resultMap["choices"].([]interface{})
	choice := choices[0].(map[string]interface{})
	message := choice["message"].(map[string]interface{})
	toolCalls := message["tool_calls"].([]interface{})

	if len(toolCalls) != 2 {
		t.Fatalf("Expected 2 tool_calls, got %d", len(toolCalls))
	}

	// First tool call should have extra_content
	toolCall1 := toolCalls[0].(map[string]interface{})
	extraContent1, ok := toolCall1["extra_content"]
	if !ok {
		t.Fatal("First tool call missing extra_content")
	}

	extraMap1 := extraContent1.(map[string]interface{})
	google1 := extraMap1["google"].(map[string]interface{})
	signature1 := google1["thought_signature"].(string)

	// Decode base64 and verify
	decodedSig1, err := base64.StdEncoding.DecodeString(signature1)
	if err != nil {
		t.Fatalf("Failed to decode base64 thought_signature: %v", err)
	}

	if string(decodedSig1) != "signature_for_first_call" {
		t.Errorf("Expected signature 'signature_for_first_call', got '%s'", string(decodedSig1))
	}

	// Second tool call should NOT have extra_content
	toolCall2 := toolCalls[1].(map[string]interface{})
	extraContent2, ok := toolCall2["extra_content"]
	if ok {
		t.Logf("Second tool call has extra_content (unexpected): %+v", extraContent2)
	} else {
		t.Logf("✓ Second tool call correctly has no extra_content")
	}

	t.Logf("✓ Successfully verified parallel function calls")
}
