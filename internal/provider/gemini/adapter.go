package gemini

import (
	"github.com/sashabaranov/go-openai"
	"google.golang.org/genai"
)

// ToGeminiRequest converts OpenAI request to Gemini request parts
// It returns system instruction (if any) and a list of contents
func ToGeminiRequest(req openai.ChatCompletionRequest) (*genai.Content, []*genai.Content, *genai.GenerateContentConfig, error) {
	var systemInstruction *genai.Content
	var contents []*genai.Content

	for _, msg := range req.Messages {
		switch msg.Role {
		case openai.ChatMessageRoleSystem:
			// Combine multiple system messages? Gemini usually takes one.
			// We'll just concatenate text for now or overwrite.
			text := msg.Content
			if systemInstruction == nil {
				systemInstruction = &genai.Content{
					Parts: []*genai.Part{{Text: text}},
				}
			} else {
				// Append to existing system instruction
				systemInstruction.Parts = append(systemInstruction.Parts, &genai.Part{Text: text})
			}
		case openai.ChatMessageRoleUser:
			contents = append(contents, &genai.Content{
				Role:  "user",
				Parts: []*genai.Part{{Text: msg.Content}},
			})
		case openai.ChatMessageRoleAssistant:
			contents = append(contents, &genai.Content{
				Role:  "model",
				Parts: []*genai.Part{{Text: msg.Content}},
			})
		default:
			// Handle other roles or ignore
		}
	}

	config := &genai.GenerateContentConfig{}
	if req.Temperature != 0 {
		t := float32(req.Temperature)
		config.Temperature = &t
	}
	if req.MaxTokens != 0 {
		m := int32(req.MaxTokens)
		config.MaxOutputTokens = m
	}
	if req.TopP != 0 {
		p := float32(req.TopP)
		config.TopP = &p
	}
	// Stop sequences
	// req.Stop can be string or []string

	return systemInstruction, contents, config, nil
}

// FromGeminiResponse converts Gemini response to OpenAI response
func FromGeminiResponse(resp *genai.GenerateContentResponse, model string) *openai.ChatCompletionResponse {
	content := ""
	if len(resp.Candidates) > 0 && resp.Candidates[0].Content != nil {
		parts := resp.Candidates[0].Content.Parts
		for _, p := range parts {
			content += p.Text
		}
	}

	return &openai.ChatCompletionResponse{
		ID:      "chatcmpl-gemini", // Generate UUID?
		Object:  "chat.completion",
		Created: 0, // Current time?
		Model:   model,
		Choices: []openai.ChatCompletionChoice{
			{
				Index: 0,
				Message: openai.ChatCompletionMessage{
					Role:    openai.ChatMessageRoleAssistant,
					Content: content,
				},
				FinishReason: openai.FinishReasonStop, // Map finish reason properly
			},
		},
	}
}

// FromGeminiChunk converts Gemini stream chunk to OpenAI chunk
func FromGeminiChunk(resp *genai.GenerateContentResponse, model string) *openai.ChatCompletionStreamResponse {
	content := ""
	if len(resp.Candidates) > 0 && resp.Candidates[0].Content != nil {
		parts := resp.Candidates[0].Content.Parts
		for _, p := range parts {
			content += p.Text
		}
	}

	return &openai.ChatCompletionStreamResponse{
		ID:      "chatcmpl-gemini",
		Object:  "chat.completion.chunk",
		Created: 0,
		Model:   model,
		Choices: []openai.ChatCompletionStreamChoice{
			{
				Index: 0,
				Delta: openai.ChatCompletionStreamChoiceDelta{
					Content: content,
				},
				FinishReason: openai.FinishReasonNull,
			},
		},
	}
}

