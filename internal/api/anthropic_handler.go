package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/sunbankio/omniproxy/internal/provider"
	"github.com/sunbankio/omniproxy/internal/router"
	"github.com/sunbankio/omniproxy/pkg/utils"
)

// convertAnthropicRequestToOpenAI converts an Anthropic MessageNewParams to an OpenAI ChatCompletionNewParams
func convertAnthropicRequestToOpenAI(anthropicReq anthropic.MessageNewParams) (*openai.ChatCompletionNewParams, error) {
	openAIReq := &openai.ChatCompletionNewParams{
		Model:    openai.ChatModel(string(anthropicReq.Model)),
		Messages: []openai.ChatCompletionMessageParamUnion{},
	}

	// Handle optional parameters
	if anthropicReq.Temperature.Valid() {
		openAIReq.Temperature = param.NewOpt(anthropicReq.Temperature.Value)
	}
	if anthropicReq.TopP.Valid() {
		openAIReq.TopP = param.NewOpt(anthropicReq.TopP.Value)
	}
	// MaxTokens is required in Anthropic, use param.NewOpt
	if anthropicReq.MaxTokens > 0 {
		openAIReq.MaxTokens = param.NewOpt(int64(anthropicReq.MaxTokens))
	}

	// Convert messages
	messages := []openai.ChatCompletionMessageParamUnion{}

	// Handle system message (Anthropic has a separate system field)
	if len(anthropicReq.System) > 0 {
		var systemContent strings.Builder
		for _, block := range anthropicReq.System {
			systemContent.WriteString(block.Text)
		}

		if systemContent.Len() > 0 {
			messages = append(messages, openai.SystemMessage(systemContent.String()))
		}
	}

	// Convert user and assistant messages
	for _, msg := range anthropicReq.Messages {
		role := msg.Role
		if role == "user" {
			userMsg := openai.UserMessage("")
			
			// Handle content blocks
			for _, block := range msg.Content {
				if textBlock := block.OfText; textBlock != nil {
					userMsg = openai.UserMessage(textBlock.Text)
				} else if imageBlock := block.OfImage; imageBlock != nil {
					// Convert image to OpenAI format
					imageURL := ""
					if source := imageBlock.Source.OfBase64; source != nil {
						imageURL = fmt.Sprintf("data:%s;base64,%s", source.MediaType, source.Data)
					} else if source := imageBlock.Source.OfURL; source != nil {
						imageURL = source.URL
					}
					userMsg = openai.UserMessage(imageURL)
				}
			}
			messages = append(messages, userMsg)
		} else if role == "assistant" {
			assistantMsg := openai.AssistantMessage("")
			
			// Handle content blocks
			for _, block := range msg.Content {
				if textBlock := block.OfText; textBlock != nil {
					assistantMsg = openai.AssistantMessage(textBlock.Text)
				}
			}
			
			// Handle tool results (tool use responses)
			// These would come from separate ToolResult blocks
			messages = append(messages, assistantMsg)
		}
	}

	openAIReq.Messages = messages

	// Note: Tools conversion is complex and requires more detailed implementation
	// For now, we'll skip tool conversion as it requires careful mapping

	return openAIReq, nil
}

// convertOpenAIResponseToAnthropic converts an OpenAI ChatCompletion to an Anthropic Message
func convertOpenAIResponseToAnthropic(openAIResp *openai.ChatCompletion) (*anthropic.Message, error) {
	if len(openAIResp.Choices) == 0 {
		return nil, fmt.Errorf("no choices in response")
	}

	choice := openAIResp.Choices[0]

	// Map stop reason - v3 uses string values
	stopReason := anthropic.StopReasonEndTurn
	switch choice.FinishReason {
	case "stop":
		stopReason = anthropic.StopReasonEndTurn
	case "length":
		stopReason = anthropic.StopReasonMaxTokens
	case "tool_calls":
		stopReason = anthropic.StopReasonToolUse
	case "content_filter":
		stopReason = anthropic.StopReasonStopSequence
	}

	// Convert content blocks
	contentBlocks := []anthropic.ContentBlockUnion{}

	// Handle text content
	if choice.Message.Content != "" {
		contentBlocks = append(contentBlocks, anthropic.ContentBlockUnion{
			Type: "text",
			Text: choice.Message.Content,
		})
	}

	// Handle tool calls
	for _, toolCall := range choice.Message.ToolCalls {
		inputJSON := json.RawMessage(toolCall.Function.Arguments)
		contentBlocks = append(contentBlocks, anthropic.ContentBlockUnion{
			Type:  "tool_use",
			ID:    toolCall.ID,
			Name:  toolCall.Function.Name,
			Input: inputJSON,
		})
	}

	// Build Anthropic message
	anthropicMsg := &anthropic.Message{
		ID:           openAIResp.ID,
		Type:         "message",
		Role:         "assistant",
		Content:      contentBlocks,
		Model:        anthropic.Model(openAIResp.Model),
		StopReason:   stopReason,
		StopSequence: "",
		Usage: anthropic.Usage{
			InputTokens:  openAIResp.Usage.PromptTokens,
			OutputTokens: openAIResp.Usage.CompletionTokens,
		},
	}

	return anthropicMsg, nil
}

// handleAnthropicStreaming converts OpenAI streaming response to Anthropic streaming format
func handleAnthropicStreaming(w http.ResponseWriter, openAIStream io.ReadCloser) error {
	defer openAIStream.Close()

	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	decoder := json.NewDecoder(openAIStream)
	currentBlockIndex := 0
	currentBlockType := ""
	messageID := ""
	model := ""
	outputTokens := 0

	for {
		var openAIChunk openai.ChatCompletionChunk
		if err := decoder.Decode(&openAIChunk); err != nil {
			if err == io.EOF {
				break
			}
			return err
		}

		// Initialize message on first chunk
		if messageID == "" && openAIChunk.ID != "" {
			messageID = openAIChunk.ID
			model = openAIChunk.Model

			// Send message_start event
			msgStart := map[string]interface{}{
				"type": "message_start",
				"message": map[string]interface{}{
					"id":      messageID,
					"type":    "message",
					"role":    "assistant",
					"content": []interface{}{},
					"model":   model,
					"stop_reason": nil,
					"stop_sequence": nil,
					"usage": map[string]interface{}{
						"input_tokens":  0,
						"output_tokens": 0,
					},
				},
			}
			sendSSEEvent(w, "message_start", msgStart)
		}

		// Handle content delta
		if len(openAIChunk.Choices) > 0 {
			delta := openAIChunk.Choices[0].Delta

			// Text content
			if delta.Content != "" {
				if currentBlockType != "text" {
					// Stop previous block if needed
					if currentBlockType != "" {
						sendSSEEvent(w, "content_block_stop", map[string]interface{}{
							"type":  "content_block_stop",
							"index": currentBlockIndex,
						})
						currentBlockIndex++
					}

					// Start new text block
					currentBlockType = "text"
					blockStart := map[string]interface{}{
						"type": "content_block_start",
						"index": currentBlockIndex,
						"content_block": map[string]interface{}{
							"type": "text",
							"text": "",
						},
					}
					sendSSEEvent(w, "content_block_start", blockStart)
				}

				// Send text delta
				textDelta := map[string]interface{}{
					"type": "content_block_delta",
					"index": currentBlockIndex,
					"delta": map[string]interface{}{
						"type": "text_delta",
						"text": delta.Content,
					},
				}
				sendSSEEvent(w, "content_block_delta", textDelta)
			}

			// Tool calls
			if len(delta.ToolCalls) > 0 {
				for _, toolCall := range delta.ToolCalls {
					toolID := toolCall.ID
					toolName := toolCall.Function.Name
					toolArgs := toolCall.Function.Arguments

					// Check if this is a new tool call
					if toolID != "" && currentBlockType != "tool_use_"+toolID {
						// Stop previous block if needed
						if currentBlockType != "" {
							sendSSEEvent(w, "content_block_stop", map[string]interface{}{
								"type":  "content_block_stop",
								"index": currentBlockIndex,
							})
							currentBlockIndex++
						}

						currentBlockType = "tool_use_" + toolID

						// Start tool use block
						blockStart := map[string]interface{}{
							"type": "content_block_start",
							"index": currentBlockIndex,
							"content_block": map[string]interface{}{
								"type": "tool_use",
								"id":   toolID,
								"name": toolName,
								"input": map[string]interface{}{},
							},
						}
						sendSSEEvent(w, "content_block_start", blockStart)
					}

					// Send input JSON delta
					if toolArgs != "" {
						inputDelta := map[string]interface{}{
							"type": "content_block_delta",
							"index": currentBlockIndex,
							"delta": map[string]interface{}{
								"type":         "input_json_delta",
								"partial_json": toolArgs,
							},
						}
						sendSSEEvent(w, "content_block_delta", inputDelta)
					}
				}
			}

			// Handle finish reason - v3 uses string values
			finishReason := openAIChunk.Choices[0].FinishReason
			if finishReason != "" {
				// Stop current block
				if currentBlockType != "" {
					sendSSEEvent(w, "content_block_stop", map[string]interface{}{
						"type":  "content_block_stop",
						"index": currentBlockIndex,
					})
					currentBlockIndex++
					currentBlockType = ""
				}

				// Map stop reason
				stopReason := "end_turn"
				switch finishReason {
				case "stop":
					stopReason = "end_turn"
				case "length":
					stopReason = "max_tokens"
				case "tool_calls":
					stopReason = "tool_use"
				case "content_filter":
					stopReason = "stop_sequence"
				}

				// Update output tokens if available (Usage is a struct, not pointer in v3)
				if openAIChunk.Usage.CompletionTokens > 0 {
					outputTokens = int(openAIChunk.Usage.CompletionTokens)
				}

				// Send message delta
				msgDelta := map[string]interface{}{
					"type": "message_delta",
					"delta": map[string]interface{}{
						"stop_reason":   stopReason,
						"stop_sequence": nil,
					},
					"usage": map[string]interface{}{
						"output_tokens": outputTokens,
					},
				}
				sendSSEEvent(w, "message_delta", msgDelta)

				// Send message stop
				sendSSEEvent(w, "message_stop", map[string]interface{}{
					"type": "message_stop",
				})
			}
		}
	}

	return nil
}

// sendSSEEvent sends a Server-Sent Event
func sendSSEEvent(w http.ResponseWriter, eventType string, data interface{}) error {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, jsonData)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	return nil
}

// HandleAnthropicMessages handles the /v1/messages endpoint
func (s *ServerV2) HandleAnthropicMessages(w http.ResponseWriter, r *http.Request) {
	// Check if streaming is requested (based on Accept header)
	isStream := strings.Contains(r.Header.Get("Accept"), "text/event-stream")

	// Read request body
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		utils.L().Errorf("Failed to read request body: %v", err)
		http.Error(w, "failed to read request body", http.StatusInternalServerError)
		return
	}
	defer r.Body.Close()

	// Parse Anthropic request
	var anthropicReq anthropic.MessageNewParams
	if err := json.Unmarshal(bodyBytes, &anthropicReq); err != nil {
		utils.L().Errorf("Failed to decode Anthropic request: %v", err)
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Validate request
	if anthropicReq.Model == "" {
		http.Error(w, "model is required", http.StatusBadRequest)
		return
	}

	// Convert to OpenAI format
	openAIReq, err := convertAnthropicRequestToOpenAI(anthropicReq)
	if err != nil {
		utils.L().Errorf("Failed to convert Anthropic request to OpenAI: %v", err)
		http.Error(w, "failed to convert request: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Note: In openai-go v3, streaming is handled by using Chat.Completions.NewStreaming()
	// instead of setting a Stream flag on the params. The IsStream field in the router
	// request is used to determine which method to call.

	// Create router request
	routerReq := &router.Request{
		Protocol: provider.ProtocolOpenAI,
		Model:    string(anthropicReq.Model),
		Payload:  openAIReq,
		IsStream: isStream,
		Headers:  make(map[string]string),
	}

	// Copy headers
	for key, values := range r.Header {
		if len(values) > 0 {
			routerReq.Headers[key] = values[0]
		}
	}

	// Execute request
	resp, err := s.sr.Execute(r.Context(), routerReq)
	if err != nil {
		s.handleError(w, err)
		return
	}
	defer resp.Body.Close()

	// Handle response
	if isStream {
		// Streaming response - convert OpenAI stream to Anthropic stream
		if err := handleAnthropicStreaming(w, resp.Body); err != nil {
			utils.L().Errorf("Failed to handle streaming response: %v", err)
			return
		}
	} else {
		// Non-streaming response
		// Parse OpenAI response
		var openAIResp openai.ChatCompletion
		if err := json.NewDecoder(resp.Body).Decode(&openAIResp); err != nil {
			utils.L().Errorf("Failed to decode OpenAI response: %v", err)
			http.Error(w, "failed to decode response", http.StatusInternalServerError)
			return
		}

		// Convert to Anthropic format
		anthropicResp, err := convertOpenAIResponseToAnthropic(&openAIResp)
		if err != nil {
			utils.L().Errorf("Failed to convert OpenAI response to Anthropic: %v", err)
			http.Error(w, "failed to convert response", http.StatusInternalServerError)
			return
		}

		// Send response
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(anthropicResp)
	}
}
