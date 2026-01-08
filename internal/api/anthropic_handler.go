package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/sashabaranov/go-openai"
	"github.com/sunbankio/omniproxy/internal/provider"
	"github.com/sunbankio/omniproxy/internal/router"
	"github.com/sunbankio/omniproxy/pkg/utils"
)

// convertAnthropicRequestToOpenAI converts an Anthropic MessageNewParams to an OpenAI ChatCompletionRequest
func convertAnthropicRequestToOpenAI(anthropicReq anthropic.MessageNewParams) (*openai.ChatCompletionRequest, error) {
	openAIReq := &openai.ChatCompletionRequest{
		Model:     string(anthropicReq.Model),
		MaxTokens: int(anthropicReq.MaxTokens),
	}

	// Handle optional parameters
	if anthropicReq.Temperature.Valid() {
		openAIReq.Temperature = float32(anthropicReq.Temperature.Value)
	}
	if anthropicReq.TopP.Valid() {
		openAIReq.TopP = float32(anthropicReq.TopP.Value)
	}
	if anthropicReq.StopSequences != nil {
		openAIReq.Stop = anthropicReq.StopSequences
	}

	// Convert messages
	messages := []openai.ChatCompletionMessage{}

	// Handle system message (Anthropic has a separate system field)
	if len(anthropicReq.System) > 0 {
		var systemContent string
		// Concatenate text blocks
		var sb strings.Builder
		for _, block := range anthropicReq.System {
			sb.WriteString(block.Text)
		}
		systemContent = sb.String()

		if systemContent != "" {
			messages = append(messages, openai.ChatCompletionMessage{
				Role:    openai.ChatMessageRoleSystem,
				Content: systemContent,
			})
		}
	}

	// Convert user and assistant messages
	for _, msg := range anthropicReq.Messages {
		role := string(msg.Role)
		if role == "user" {
			role = openai.ChatMessageRoleUser
		} else if role == "assistant" {
			role = openai.ChatMessageRoleAssistant
		}

		// Handle content blocks
		var content string
		var multiContent []openai.ChatMessagePart

		for _, block := range msg.Content {
			if textBlock := block.OfText; textBlock != nil {
				if len(multiContent) == 0 && content == "" {
					// First text block, use simple content
					content = textBlock.Text
				} else {
					// Already have content or multi-content, switch to multi-content
					if content != "" {
						multiContent = append(multiContent, openai.ChatMessagePart{
							Type: openai.ChatMessagePartTypeText,
							Text: content,
						})
						content = ""
					}
					multiContent = append(multiContent, openai.ChatMessagePart{
						Type: openai.ChatMessagePartTypeText,
						Text: textBlock.Text,
					})
				}
			} else if imageBlock := block.OfImage; imageBlock != nil {
				// Convert image to OpenAI format
				imageURL := ""
				if source := imageBlock.Source.OfBase64; source != nil {
					imageURL = fmt.Sprintf("data:%s;base64,%s", source.MediaType, source.Data)
				} else if source := imageBlock.Source.OfURL; source != nil {
					imageURL = source.URL
				}

				if len(multiContent) == 0 && content == "" {
					// First block, initialize multi-content
					multiContent = []openai.ChatMessagePart{}
				}
				if content != "" {
					multiContent = append(multiContent, openai.ChatMessagePart{
						Type: openai.ChatMessagePartTypeText,
						Text: content,
					})
					content = ""
				}
				multiContent = append(multiContent, openai.ChatMessagePart{
					Type: openai.ChatMessagePartTypeImageURL,
					ImageURL: &openai.ChatMessageImageURL{
						URL: imageURL,
					},
				})
			}
		}

		chatMsg := openai.ChatCompletionMessage{
			Role: role,
		}

		if len(multiContent) > 0 {
			chatMsg.MultiContent = multiContent
		} else {
			chatMsg.Content = content
		}

		messages = append(messages, chatMsg)
	}

	openAIReq.Messages = messages

	// Convert tools
	if anthropicReq.Tools != nil {
		tools := []openai.Tool{}
		for _, tool := range anthropicReq.Tools {
			if toolParam := tool.OfTool; toolParam != nil {
				inputSchema := map[string]interface{}{}
				// Marshal the ToolInputSchemaParam to JSON and unmarshal to map
				schemaJSON, err := json.Marshal(toolParam.InputSchema)
				if err != nil {
					utils.L().Warnf("Failed to marshal tool input schema: %v", err)
				} else {
					if err := json.Unmarshal(schemaJSON, &inputSchema); err != nil {
						utils.L().Warnf("Failed to unmarshal tool input schema: %v", err)
					}
				}
				description := ""
				if toolParam.Description.Valid() {
					description = toolParam.Description.Value
				}
				tools = append(tools, openai.Tool{
					Type: openai.ToolTypeFunction,
					Function: &openai.FunctionDefinition{
						Name:        toolParam.Name,
						Description: description,
						Parameters:  inputSchema,
					},
				})
			}
		}
		openAIReq.Tools = tools
	}

	// Convert tool choice
	toolChoice := anthropicReq.ToolChoice
	if toolChoice.OfAuto != nil {
		openAIReq.ToolChoice = "auto"
	} else if toolChoice.OfAny != nil {
		openAIReq.ToolChoice = "required"
	} else if toolChoice.OfTool != nil {
		openAIReq.ToolChoice = openai.ToolChoice{
			Type: openai.ToolTypeFunction,
			Function: openai.ToolFunction{
				Name: toolChoice.OfTool.Name,
			},
		}
	}

	return openAIReq, nil
}

// convertOpenAIResponseToAnthropic converts an OpenAI ChatCompletionResponse to an Anthropic Message
func convertOpenAIResponseToAnthropic(openAIResp *openai.ChatCompletionResponse) (*anthropic.Message, error) {
	// Map stop reason
	stopReason := anthropic.StopReasonEndTurn
	switch openAIResp.Choices[0].FinishReason {
	case openai.FinishReasonStop:
		stopReason = anthropic.StopReasonEndTurn
	case openai.FinishReasonLength:
		stopReason = anthropic.StopReasonMaxTokens
	case openai.FinishReasonToolCalls:
		stopReason = anthropic.StopReasonToolUse
	case openai.FinishReasonContentFilter:
		stopReason = anthropic.StopReasonStopSequence
	}

	// Convert content blocks
	contentBlocks := []anthropic.ContentBlockUnion{}

	// Handle text content
	if openAIResp.Choices[0].Message.Content != "" {
		contentBlocks = append(contentBlocks, anthropic.ContentBlockUnion{
			Type: "text",
			Text: openAIResp.Choices[0].Message.Content,
		})
	}

	// Handle tool calls
	for _, toolCall := range openAIResp.Choices[0].Message.ToolCalls {
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
			InputTokens:  int64(openAIResp.Usage.PromptTokens),
			OutputTokens: int64(openAIResp.Usage.CompletionTokens),
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
		var openAIChunk openai.ChatCompletionStreamResponse
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

			// Handle finish reason
			if openAIChunk.Choices[0].FinishReason != "" {
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
				switch openAIChunk.Choices[0].FinishReason {
				case openai.FinishReasonStop:
					stopReason = "end_turn"
				case openai.FinishReasonLength:
					stopReason = "max_tokens"
				case openai.FinishReasonToolCalls:
					stopReason = "tool_use"
				case openai.FinishReasonContentFilter:
					stopReason = "stop_sequence"
				}

				// Update output tokens if available
				if openAIChunk.Usage != nil {
					outputTokens = openAIChunk.Usage.CompletionTokens
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

	// Set stream flag
	openAIReq.Stream = isStream

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
		var openAIResp openai.ChatCompletionResponse
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