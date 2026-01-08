package api

import (
	"bufio"
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
	if anthropicReq.TopK.Valid() {
		// OpenAI doesn't have top_k, but we can approximate with temperature
		// This is a limitation of the OpenAI API
	}
	// MaxTokens is required in Anthropic
	if anthropicReq.MaxTokens > 0 {
		openAIReq.MaxTokens = param.NewOpt(anthropicReq.MaxTokens)
	}

	// Set stream options to include usage in streaming responses
	// This is needed to get accurate token counts in streaming mode
	openAIReq.StreamOptions.IncludeUsage = param.NewOpt(true)

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
			// Check if this is a tool result message
			hasToolResult := false
			for _, block := range msg.Content {
				if block.OfToolResult != nil {
					hasToolResult = true
					break
				}
			}

			if hasToolResult {
				// Handle tool result messages - convert to OpenAI tool messages
				// In Anthropic, tool results are sent as user messages with tool_result blocks
				// In OpenAI, each tool result needs to be a separate tool message
				// Note: OpenAI tool messages only support text content, not images
				for _, block := range msg.Content {
					if toolResultBlock := block.OfToolResult; toolResultBlock != nil {
						// Convert tool result to OpenAI tool message
						// Only text content is supported in OpenAI tool messages
						var contentParts []openai.ChatCompletionContentPartTextParam

						// Handle content parts
						for _, contentPart := range toolResultBlock.Content {
							if textPart := contentPart.OfText; textPart != nil {
								contentParts = append(contentParts, openai.ChatCompletionContentPartTextParam{
									Type: "text",
									Text: textPart.Text,
								})
							}
							// Note: Images in tool results are not supported by OpenAI API
							// They are silently ignored
						}

						// Create tool message with content parts
						if len(contentParts) == 0 {
							// Empty content - use empty string
							messages = append(messages, openai.ToolMessage("", toolResultBlock.ToolUseID))
						} else if len(contentParts) == 1 {
							// Single content part - can use simplified form
							messages = append(messages, openai.ToolMessage(contentParts[0].Text, toolResultBlock.ToolUseID))
						} else {
							// Multiple content parts - use array form
							messages = append(messages, openai.ToolMessage(contentParts, toolResultBlock.ToolUseID))
						}
					}
				}
			} else {
				// Regular user message
				// Build content parts for user message
				var contentParts []openai.ChatCompletionContentPartUnionParam

				// Handle content blocks
				for _, block := range msg.Content {
					if textBlock := block.OfText; textBlock != nil {
						contentParts = append(contentParts, openai.TextContentPart(textBlock.Text))
					} else if imageBlock := block.OfImage; imageBlock != nil {
						// Convert image to OpenAI format
						imageURL := ""
						if source := imageBlock.Source.OfBase64; source != nil {
							imageURL = fmt.Sprintf("data:%s;base64,%s", source.MediaType, source.Data)
						} else if source := imageBlock.Source.OfURL; source != nil {
							imageURL = source.URL
						}
						contentParts = append(contentParts, openai.ImageContentPart(openai.ChatCompletionContentPartImageImageURLParam{
							URL: imageURL,
						}))
					}
				}

				// Create user message with content parts
				if len(contentParts) == 0 {
					// Empty content - use empty string
					messages = append(messages, openai.UserMessage(""))
				} else if len(contentParts) == 1 {
					// Single content part - can use simplified form
					if textPart := contentParts[0].OfText; textPart != nil {
						messages = append(messages, openai.UserMessage(textPart.Text))
					} else {
						messages = append(messages, openai.UserMessage(contentParts))
					}
				} else {
					// Multiple content parts - use array form
					messages = append(messages, openai.UserMessage(contentParts))
				}
			}
		} else if role == "assistant" {
			// Handle assistant message with potential tool calls
			var contentParts []openai.ChatCompletionAssistantMessageParamContentArrayOfContentPartUnion
			var toolCalls []openai.ChatCompletionMessageToolCallUnionParam

			// Handle content blocks
			for _, block := range msg.Content {
				if textBlock := block.OfText; textBlock != nil {
					contentParts = append(contentParts, openai.ChatCompletionAssistantMessageParamContentArrayOfContentPartUnion{
						OfText: &openai.ChatCompletionContentPartTextParam{
							Type: "text",
							Text: textBlock.Text,
						},
					})
				} else if toolUseBlock := block.OfToolUse; toolUseBlock != nil {
					// Convert tool use to OpenAI tool call
					// toolUseBlock.Input is json.RawMessage (any), need to convert to string
					inputStr := ""
					if toolUseBlock.Input != nil {
						inputBytes, ok := toolUseBlock.Input.([]byte)
						if ok {
							inputStr = string(inputBytes)
						} else {
							// Try to marshal to JSON
							if jsonBytes, err := json.Marshal(toolUseBlock.Input); err == nil {
								inputStr = string(jsonBytes)
							}
						}
					}
					// Create function tool call
					toolCalls = append(toolCalls, openai.ChatCompletionMessageToolCallUnionParam{
						OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
							ID: toolUseBlock.ID,
							Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{
								Name:      toolUseBlock.Name,
								Arguments: inputStr,
							},
						},
					})
				}
			}

			// Create assistant message
			if len(toolCalls) > 0 {
				// Has tool calls - create assistant message with tool calls
				assistantMsg := openai.ChatCompletionAssistantMessageParam{
					Role:      "assistant",
					ToolCalls: toolCalls,
				}
				if len(contentParts) > 0 {
					assistantMsg.Content = openai.ChatCompletionAssistantMessageParamContentUnion{
						OfArrayOfContentParts: contentParts,
					}
				}
				messages = append(messages, openai.ChatCompletionMessageParamUnion{
					OfAssistant: &assistantMsg,
				})
			} else if len(contentParts) > 0 {
				// Only content parts
				if len(contentParts) == 1 && contentParts[0].OfText != nil {
					messages = append(messages, openai.AssistantMessage(contentParts[0].OfText.Text))
				} else {
					messages = append(messages, openai.AssistantMessage(contentParts))
				}
			} else {
				// Empty assistant message
				messages = append(messages, openai.AssistantMessage(""))
			}
		}
	}

	openAIReq.Messages = messages

	// Convert tools
	var openAITools []openai.ChatCompletionToolUnionParam
	if len(anthropicReq.Tools) > 0 {
		openAITools = make([]openai.ChatCompletionToolUnionParam, 0, len(anthropicReq.Tools))
		for _, tool := range anthropicReq.Tools {
			if toolDef := tool.OfTool; toolDef != nil {
				var description param.Opt[string]
				if toolDef.Description.Valid() {
					description = param.NewOpt(toolDef.Description.Value)
				}

				// Convert Anthropic ToolInputSchemaParam to OpenAI FunctionParameters
				// Both are JSON schemas, we need to convert between them
				var parameters openai.FunctionParameters
				if param.IsOmitted(toolDef.InputSchema) == false {
					// Marshal and unmarshal to convert types
					schemaBytes, err := json.Marshal(toolDef.InputSchema)
					if err != nil {
						return nil, fmt.Errorf("failed to marshal tool input schema: %w", err)
					}
					if err := json.Unmarshal(schemaBytes, &parameters); err != nil {
						return nil, fmt.Errorf("failed to unmarshal tool input schema: %w", err)
					}
				}

				openAITools = append(openAITools, openai.ChatCompletionFunctionTool(openai.FunctionDefinitionParam{
					Name:        toolDef.Name,
					Description: description,
					Parameters:  parameters,
				}))
			}
		}
		openAIReq.Tools = openAITools
	}

	// Convert tool choice
	// Check which variant is set
	if anthropicReq.ToolChoice.OfAuto != nil {
		openAIReq.ToolChoice.OfAuto = param.NewOpt("auto")
	} else if anthropicReq.ToolChoice.OfAny != nil {
		// Map "any" to "required" - forces the model to call a tool
		// We need to use OfAllowedTools with Mode: "required" and include all tools
		if len(openAITools) > 0 {
			// Convert tools to the format expected by ChatCompletionAllowedToolsParam
			tools := make([]map[string]any, len(openAITools))
			for i, tool := range openAITools {
				tools[i] = map[string]any{
					"type":     "function",
					"function": tool,
				}
			}
			allowedTools := openai.ChatCompletionAllowedToolsParam{
				Mode:  openai.ChatCompletionAllowedToolsModeRequired,
				Tools: tools,
			}
			openAIReq.ToolChoice = openai.ToolChoiceOptionAllowedTools(allowedTools)
		} else {
			// Fallback to auto if no tools are defined
			openAIReq.ToolChoice.OfAuto = param.NewOpt("auto")
		}
	} else if anthropicReq.ToolChoice.OfTool != nil {
		tool := anthropicReq.ToolChoice.OfTool
		openAIReq.ToolChoice.OfFunctionToolChoice = &openai.ChatCompletionNamedToolChoiceParam{
			Type: "function",
			Function: openai.ChatCompletionNamedToolChoiceFunctionParam{
				Name: tool.Name,
			},
		}
	}

	// Convert stop sequences
	if len(anthropicReq.StopSequences) > 0 {
		openAIReq.Stop.OfStringArray = anthropicReq.StopSequences
	}

	// Note: OpenAI doesn't have direct equivalents for:
	// - Metadata (can be passed in user field)
	// - ServiceTier (OpenAI has different tier system)
	// - Thinking (extended thinking is Anthropic-specific)
	// - TopK (OpenAI uses temperature and top_p)

	return openAIReq, nil
}

// convertOpenAIResponseToAnthropic converts an OpenAI ChatCompletion to an Anthropic Message
func convertOpenAIResponseToAnthropic(openAIResp *openai.ChatCompletion) (*anthropic.Message, error) {
	if len(openAIResp.Choices) == 0 {
		return nil, fmt.Errorf("no choices in response")
	}

	choice := openAIResp.Choices[0]

	// Map stop reason - OpenAI uses string values
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

	// Convert content blocks - construct ContentBlockUnion manually
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
		// Convert tool call to Anthropic tool use block
		contentBlocks = append(contentBlocks, anthropic.ContentBlockUnion{
			Type:  "tool_use",
			ID:    toolCall.ID,
			Name:  toolCall.Function.Name,
			Input: json.RawMessage(toolCall.Function.Arguments),
		})
	}

	// Build Anthropic message using official SDK types
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

	// Use bufio to read lines for SSE format handling
	scanner := bufio.NewScanner(openAIStream)
	
	currentBlockIndex := int64(0)
	currentBlockType := ""
	messageID := ""
	model := ""
	inputTokens := int64(0)
	outputTokens := int64(0)
	
	// Track tool calls by index for streaming
	type toolCallState struct {
		ID      string
		Name    string
		Args    strings.Builder
		Started bool
	}
	toolCalls := make(map[int64]*toolCallState)

	for scanner.Scan() {
		line := scanner.Text()
		
		// Skip empty lines
		if line == "" {
			continue
		}
		
		// Handle SSE format: strip "data: " prefix
		if strings.HasPrefix(line, "data: ") {
			line = strings.TrimPrefix(line, "data: ")
		} else if strings.HasPrefix(line, "data:") {
			line = strings.TrimPrefix(line, "data:")
		} else {
			// Skip non-data lines (like comments, event types, etc.)
			continue
		}
		
		// Skip "[DONE]" marker
		if line == "[DONE]" {
			break
		}
		
		// Parse JSON
		var openAIChunk openai.ChatCompletionChunk
		if err := json.Unmarshal([]byte(line), &openAIChunk); err != nil {
			utils.L().Errorf("Failed to decode streaming chunk: %v. Raw data: %s", err, line)
			return err
		}

		// Initialize message on first chunk
		if messageID == "" && openAIChunk.ID != "" {
			messageID = openAIChunk.ID
			model = openAIChunk.Model

			// Send message_start event
			msgStart := anthropic.MessageStreamEventUnion{
				Type: "message_start",
				Message: anthropic.Message{
					ID:           messageID,
					Type:         "message",
					Role:         "assistant",
					Content:      []anthropic.ContentBlockUnion{},
					Model:        anthropic.Model(model),
					StopReason:   anthropic.StopReasonEndTurn,
					StopSequence: "",
					Usage: anthropic.Usage{
						InputTokens:  inputTokens,
						OutputTokens: outputTokens,
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
						blockStop := anthropic.MessageStreamEventUnion{
							Type:  "content_block_stop",
							Index: currentBlockIndex,
						}
						sendSSEEvent(w, "content_block_stop", blockStop)
						currentBlockIndex++
					}

					// Start new text block
					currentBlockType = "text"
					blockStart := anthropic.MessageStreamEventUnion{
						Type:  "content_block_start",
						Index: currentBlockIndex,
						ContentBlock: anthropic.ContentBlockStartEventContentBlockUnion{
							Type: "text",
							Text: "",
						},
					}
					sendSSEEvent(w, "content_block_start", blockStart)
				}

				// Send text delta
				textDelta := anthropic.MessageStreamEventUnion{
					Type:  "content_block_delta",
					Index: currentBlockIndex,
					Delta: anthropic.MessageStreamEventUnionDelta{
						Type: "text_delta",
						Text: delta.Content,
					},
				}
				sendSSEEvent(w, "content_block_delta", textDelta)
			}

			// Tool calls
			if len(delta.ToolCalls) > 0 {
				for _, toolCall := range delta.ToolCalls {
					toolIndex := toolCall.Index
					toolID := toolCall.ID
					toolName := toolCall.Function.Name
					toolArgs := toolCall.Function.Arguments

					// Get or create tool call state
					state, exists := toolCalls[toolIndex]
					if !exists {
						state = &toolCallState{}
						toolCalls[toolIndex] = state
					}

					// Update tool call info if provided
					if toolID != "" {
						state.ID = toolID
					}
					if toolName != "" {
						state.Name = toolName
					}

					// Start tool use block if not yet started and we have ID
					if !state.Started && state.ID != "" {
						// Stop previous block if needed
						if currentBlockType != "" {
							blockStop := anthropic.MessageStreamEventUnion{
								Type:  "content_block_stop",
								Index: currentBlockIndex,
							}
							sendSSEEvent(w, "content_block_stop", blockStop)
							currentBlockIndex++
						}

						currentBlockType = "tool_use_" + state.ID
						state.Started = true

						// Start tool use block
						blockStart := anthropic.MessageStreamEventUnion{
							Type:  "content_block_start",
							Index: currentBlockIndex,
							ContentBlock: anthropic.ContentBlockStartEventContentBlockUnion{
								Type:  "tool_use",
								ID:    state.ID,
								Name:  state.Name,
								Input: json.RawMessage("{}"),
							},
						}
						sendSSEEvent(w, "content_block_start", blockStart)
					}

					// Send input JSON delta
					if toolArgs != "" {
						inputDelta := anthropic.MessageStreamEventUnion{
							Type:  "content_block_delta",
							Index: currentBlockIndex,
							Delta: anthropic.MessageStreamEventUnionDelta{
								Type:        "input_json_delta",
								PartialJSON: toolArgs,
							},
						}
						sendSSEEvent(w, "content_block_delta", inputDelta)
					}
				}
			}

			// Handle finish reason - OpenAI uses string values
			finishReason := openAIChunk.Choices[0].FinishReason
			if finishReason != "" {
				// Stop current block
				if currentBlockType != "" {
					blockStop := anthropic.MessageStreamEventUnion{
						Type:  "content_block_stop",
						Index: currentBlockIndex,
					}
					sendSSEEvent(w, "content_block_stop", blockStop)
					currentBlockIndex++
					currentBlockType = ""
				}

				// Map stop reason
				stopReason := anthropic.StopReasonEndTurn
				switch finishReason {
				case "stop":
					stopReason = anthropic.StopReasonEndTurn
				case "length":
					stopReason = anthropic.StopReasonMaxTokens
				case "tool_calls":
					stopReason = anthropic.StopReasonToolUse
				case "content_filter":
					stopReason = anthropic.StopReasonStopSequence
				}

				// Update usage if available (from stream_options: {"include_usage": true})
				if openAIChunk.Usage.PromptTokens > 0 {
					inputTokens = openAIChunk.Usage.PromptTokens
				}
				if openAIChunk.Usage.CompletionTokens > 0 {
					outputTokens = openAIChunk.Usage.CompletionTokens
				}

				// Send message delta
				msgDelta := anthropic.MessageStreamEventUnion{
					Type: "message_delta",
					Delta: anthropic.MessageStreamEventUnionDelta{
						StopReason:   stopReason,
						StopSequence: "",
					},
					Usage: anthropic.MessageDeltaUsage{
						OutputTokens: outputTokens,
					},
				}
				sendSSEEvent(w, "message_delta", msgDelta)

				// Send message stop
				msgStop := anthropic.MessageStreamEventUnion{
					Type: "message_stop",
				}
				sendSSEEvent(w, "message_stop", msgStop)
			}
		}
	}

	// Check for scanner errors
	if err := scanner.Err(); err != nil {
		utils.L().Errorf("Error reading stream: %v", err)
		return err
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
	// Read request body
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		utils.L().Errorf("Failed to read request body: %v", err)
		http.Error(w, "failed to read request body", http.StatusInternalServerError)
		return
	}
	defer r.Body.Close()

	// Parse stream parameter from request body first (following Anthropic API standard)
	// stream=true means explicit request for streaming
	var rawReq map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &rawReq); err != nil {
		utils.L().Errorf("Failed to decode Anthropic request: %v", err)
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	isStream := false
	if stream, ok := rawReq["stream"].(bool); ok {
		isStream = stream
	}

	// Parse Anthropic request into SDK type
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
