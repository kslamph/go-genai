package gemini

import (
	"time"

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

// finishReasonMap maps Gemini finish reasons to OpenAI finish reasons
var finishReasonMap = map[genai.FinishReason]openai.FinishReason{
	genai.FinishReasonStop:        openai.FinishReasonStop,
	genai.FinishReasonMaxTokens:   openai.FinishReasonLength,
	genai.FinishReasonSafety:      openai.FinishReasonContentFilter,
	genai.FinishReasonRecitation:  openai.FinishReasonContentFilter,
	genai.FinishReasonLanguage:    openai.FinishReasonContentFilter,
	genai.FinishReasonBlocklist:   openai.FinishReasonContentFilter,
	genai.FinishReasonOther:       openai.FinishReasonStop,
	genai.FinishReasonUnspecified: openai.FinishReasonStop,
}

// FromGeminiResponse converts Gemini response to OpenAI response
func FromGeminiResponse(resp *genai.GenerateContentResponse, model string) *openai.ChatCompletionResponse {
	created := time.Now().Unix()
	if !resp.CreateTime.IsZero() {
		created = resp.CreateTime.Unix()
	}

	var content string
	var finishReason openai.FinishReason = openai.FinishReasonStop

	if len(resp.Candidates) > 0 && resp.Candidates[0].Content != nil {
		parts := resp.Candidates[0].Content.Parts
		for _, p := range parts {
			content += p.Text
		}
		// Get finish reason from candidate
		if resp.Candidates[0].FinishReason != "" {
			if mapped, ok := finishReasonMap[resp.Candidates[0].FinishReason]; ok {
				finishReason = mapped
			}
		}
	}

	responseID := resp.ResponseID
	if responseID == "" {
		responseID = "chatcmpl-gemini"
	}

	choices := []openai.ChatCompletionChoice{
		{
			Index: 0,
			Message: openai.ChatCompletionMessage{
				Role:    openai.ChatMessageRoleAssistant,
				Content: content,
			},
			FinishReason: finishReason,
		},
	}

	usage := openai.Usage{}
	if resp.UsageMetadata != nil {
		usage.PromptTokens = int(resp.UsageMetadata.PromptTokenCount)
		usage.CompletionTokens = int(resp.UsageMetadata.CandidatesTokenCount)
		usage.TotalTokens = int(resp.UsageMetadata.PromptTokenCount + resp.UsageMetadata.CandidatesTokenCount)
	}

	return &openai.ChatCompletionResponse{
		ID:      responseID,
		Object:  "chat.completion",
		Created: created,
		Model:   model,
		Choices: choices,
		Usage:   usage,
	}
}

// FromGeminiChunk converts Gemini stream chunk to OpenAI chunk
func FromGeminiChunk(resp *genai.GenerateContentResponse, model string) *openai.ChatCompletionStreamResponse {
	created := time.Now().Unix()
	if !resp.CreateTime.IsZero() {
		created = resp.CreateTime.Unix()
	}

	var content string
	var finishReason openai.FinishReason = openai.FinishReasonNull

	if len(resp.Candidates) > 0 && resp.Candidates[0].Content != nil {
		parts := resp.Candidates[0].Content.Parts
		for _, p := range parts {
			content += p.Text
		}
		// Only set finish reason if we have one (non-null)
		if resp.Candidates[0].FinishReason != "" && resp.Candidates[0].FinishReason != genai.FinishReasonUnspecified {
			if mapped, ok := finishReasonMap[resp.Candidates[0].FinishReason]; ok {
				finishReason = mapped
			}
		}
	}

	responseID := resp.ResponseID
	if responseID == "" {
		responseID = "chatcmpl-gemini"
	}

	return &openai.ChatCompletionStreamResponse{
		ID:      responseID,
		Object:  "chat.completion.chunk",
		Created: created,
		Model:   model,
		Choices: []openai.ChatCompletionStreamChoice{
			{
				Index: 0,
				Delta: openai.ChatCompletionStreamChoiceDelta{
					Content: content,
				},
				FinishReason: finishReason,
			},
		},
	}
}
