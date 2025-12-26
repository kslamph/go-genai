package gemini

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/sashabaranov/go-openai"
	"github.com/sunbankio/omniproxy/pkg/utils"
	"google.golang.org/genai"
)

// CustomToolCall extends openai.ToolCall to support extra_content for thought signatures
type CustomToolCall struct {
	openai.ToolCall
	ExtraContent map[string]interface{} `json:"extra_content,omitempty"`
}

// CustomToolCallExtraContent represents the extra_content structure for thought signatures
type CustomToolCallExtraContent struct {
	Google GoogleExtraContent `json:"google"`
}

// GoogleExtraContent represents Google-specific extra content
type GoogleExtraContent struct {
	ThoughtSignature []byte `json:"thought_signature,omitempty"`
}

// CustomMessage extends openai.ChatCompletionMessage to use CustomToolCall
type CustomMessage struct {
	Role      string           `json:"role"`
	Content   string           `json:"content,omitempty"`
	ToolCalls []CustomToolCall `json:"tool_calls,omitempty"`
}

// CustomChoice extends openai.ChatCompletionChoice to use CustomMessage
type CustomChoice struct {
	Index        int           `json:"index"`
	Message      CustomMessage `json:"message"`
	FinishReason string        `json:"finish_reason"`
}

// CustomChatCompletionResponse extends openai.ChatCompletionResponse to use CustomChoice
type CustomChatCompletionResponse struct {
	ID      string                `json:"id"`
	Object  string                `json:"object"`
	Created int64                 `json:"created"`
	Model   string                `json:"model"`
	Choices []CustomChoice        `json:"choices"`
	Usage   openai.Usage          `json:"usage"`
	SystemFingerprint string      `json:"system_fingerprint,omitempty"`
}

// min returns the minimum of two integers
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ToGeminiRequest converts OpenAI request to Gemini request parts
// It returns system instruction (if any), a list of contents, and config
func ToGeminiRequest(req openai.ChatCompletionRequest) (*genai.Content, []*genai.Content, *genai.GenerateContentConfig, error) {
	var systemInstruction *genai.Content
	var contents []*genai.Content

	// Debug: Log the entire incoming request
	reqJSON, _ := json.MarshalIndent(req, "", "  ")
	utils.L().Debugw("=== ToGeminiRequest: Incoming OpenAI Request ===",
		"request", string(reqJSON))

	// Map to track tool call IDs to function names for later reference
	toolCallIDToFunctionName := make(map[string]string)

	// Debug: Log tool information
	if len(req.Tools) > 0 {
		utils.L().Infow("Converting OpenAI request with tools",
			"num_tools", len(req.Tools),
			"tool_choice", req.ToolChoice)
		for i, tool := range req.Tools {
			utils.L().Debugw("Tool definition",
				"index", i,
				"type", tool.Type,
				"function_name", tool.Function.Name,
				"function_description", tool.Function.Description)
		}
	}

	// Convert messages
	for i, msg := range req.Messages {
		// Extract text from either Content (string) or MultiContent (array)
		text := msg.Content
		if text == "" && len(msg.MultiContent) > 0 {
			// Concatenate all text parts from MultiContent
			for _, part := range msg.MultiContent {
				if part.Type == "text" {
					text += part.Text
				}
			}
		}

		// Debug: Log message details
		utils.L().Debugw("Converting message",
			"index", i,
			"role", msg.Role,
			"content_length", len(text),
			"has_tool_calls", len(msg.ToolCalls) > 0,
			"tool_call_id", msg.ToolCallID)

		switch msg.Role {
		case openai.ChatMessageRoleSystem:
			// Combine multiple system messages
			if systemInstruction == nil {
				systemInstruction = &genai.Content{
					Parts: []*genai.Part{{Text: text}},
				}
			} else {
				// Append to existing system instruction
				systemInstruction.Parts = append(systemInstruction.Parts, &genai.Part{Text: text})
			}

		case openai.ChatMessageRoleUser:
			if text == "" && msg.ToolCallID == "" {
				utils.L().Warnw("Empty user message content", "index", i)
			}
			contents = append(contents, &genai.Content{
				Role:  "user",
				Parts: []*genai.Part{{Text: text}},
			})

		case openai.ChatMessageRoleAssistant:
			// Assistant messages can contain text and/or tool calls
			var parts []*genai.Part

			// Add text content if present
			if text != "" {
				parts = append(parts, &genai.Part{Text: text})
			}

			// Convert tool calls to function calls
			if len(msg.ToolCalls) > 0 {
				utils.L().Infow("Converting assistant tool calls",
					"num_tool_calls", len(msg.ToolCalls))

				for j, toolCall := range msg.ToolCalls {
					if toolCall.Type != openai.ToolTypeFunction {
						utils.L().Warnw("Unsupported tool call type",
							"type", toolCall.Type,
							"index", j)
						continue
					}

					// Store mapping of tool call ID to function name for later use
					toolCallIDToFunctionName[toolCall.ID] = toolCall.Function.Name

					// Parse arguments from JSON string to map
					var args map[string]any
					if toolCall.Function.Arguments != "" {
						if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &args); err != nil {
							utils.L().Errorw("Failed to parse tool call arguments",
								"error", err,
								"arguments", toolCall.Function.Arguments)
							return nil, nil, nil, fmt.Errorf("failed to parse tool call arguments: %w", err)
						}
					}

					// Extract thought_signature from extra_content if present
					var thoughtSignature []byte
					toolCallJSON, err := json.Marshal(toolCall)
					if err == nil {
						var toolCallMap map[string]interface{}
						if err := json.Unmarshal(toolCallJSON, &toolCallMap); err == nil {
							utils.L().Debugw("Tool call as map",
								"tool_call_id", toolCall.ID,
								"tool_call_map", toolCallMap)

							if extraContent, ok := toolCallMap["extra_content"].(map[string]interface{}); ok {
								utils.L().Debugw("Found extra_content in tool call",
									"tool_call_id", toolCall.ID,
									"extra_content", extraContent)

								if google, ok := extraContent["google"].(map[string]interface{}); ok {
									utils.L().Debugw("Found google field in extra_content",
										"tool_call_id", toolCall.ID,
										"google", google)

									if sig, ok := google["thought_signature"].(string); ok {
										thoughtSignature = []byte(sig)
										sigPreview := sig
										if len(sig) > 50 {
											sigPreview = sig[:50]
										}
										utils.L().Infow("✓ Extracted thought_signature from extra_content",
											"tool_call_id", toolCall.ID,
											"signature_length", len(thoughtSignature),
											"signature_preview", sigPreview)
									} else {
										utils.L().Warnw("thought_signature field not found or wrong type in google",
											"tool_call_id", toolCall.ID)
									}
								} else {
									utils.L().Warnw("google field not found or wrong type in extra_content",
										"tool_call_id", toolCall.ID)
								}
							} else {
								utils.L().Debugw("No extra_content in tool call",
									"tool_call_id", toolCall.ID)
							}
						}
					}

					utils.L().Debugw("Converting tool call",
						"index", j,
						"id", toolCall.ID,
						"function_name", toolCall.Function.Name,
						"args", args,
						"has_thought_signature", len(thoughtSignature) > 0)

					part := &genai.Part{
						FunctionCall: &genai.FunctionCall{
							ID:   toolCall.ID,
							Name: toolCall.Function.Name,
							Args: args,
						},
					}

					// Add thought_signature if present
					if len(thoughtSignature) > 0 {
						part.ThoughtSignature = thoughtSignature
					}

					parts = append(parts, part)
				}
			}

			if len(parts) > 0 {
				contents = append(contents, &genai.Content{
					Role:  "model",
					Parts: parts,
				})
			}

		case openai.ChatMessageRoleTool:
			// Tool response messages
			utils.L().Infow("Converting tool response",
				"tool_call_id", msg.ToolCallID,
				"content_length", len(text))

			// Parse the tool response content
			var response map[string]any
			if text != "" {
				// Try to parse as JSON first
				if err := json.Unmarshal([]byte(text), &response); err != nil {
					// If not JSON, wrap in a response object
					response = map[string]any{"output": text}
				}
			} else {
				response = map[string]any{}
			}

			// Look up the function name from our mapping
			functionName, found := toolCallIDToFunctionName[msg.ToolCallID]
			if !found {
				utils.L().Errorw("Tool call ID not found in mapping",
					"tool_call_id", msg.ToolCallID,
					"available_ids", toolCallIDToFunctionName)
				return nil, nil, nil, fmt.Errorf("tool call ID %s not found in previous tool calls", msg.ToolCallID)
			}

			utils.L().Debugw("Found function name for tool response",
				"tool_call_id", msg.ToolCallID,
				"function_name", functionName)

			contents = append(contents, &genai.Content{
				Role: "user", // Tool responses come from user in Gemini
				Parts: []*genai.Part{{
					FunctionResponse: &genai.FunctionResponse{
						ID:       msg.ToolCallID,
						Name:     functionName, // Use the looked-up function name
						Response: response,
					},
				}},
			})

		default:
			utils.L().Warnw("Unknown message role", "role", msg.Role, "index", i)
		}
	}

	// Build config
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

	// Convert tools to function declarations
	if len(req.Tools) > 0 {
		var tools []*genai.Tool
		var functionDeclarations []*genai.FunctionDeclaration

		for _, tool := range req.Tools {
			if tool.Type != openai.ToolTypeFunction {
				utils.L().Warnw("Unsupported tool type", "type", tool.Type)
				continue
			}

			// Convert parameters to Schema
			var schema *genai.Schema
			if tool.Function.Parameters != nil {
				// Parameters can be json.RawMessage or a struct
				var paramsMap map[string]any

				switch params := tool.Function.Parameters.(type) {
				case json.RawMessage:
					if err := json.Unmarshal(params, &paramsMap); err != nil {
						utils.L().Errorw("Failed to parse function parameters",
							"error", err,
							"function", tool.Function.Name)
						return nil, nil, nil, fmt.Errorf("failed to parse function parameters: %w", err)
					}
				case map[string]any:
					paramsMap = params
				case string:
					if err := json.Unmarshal([]byte(params), &paramsMap); err != nil {
						utils.L().Errorw("Failed to parse function parameters from string",
							"error", err,
							"function", tool.Function.Name)
						return nil, nil, nil, fmt.Errorf("failed to parse function parameters: %w", err)
					}
				default:
					// Try to marshal and unmarshal
					jsonBytes, err := json.Marshal(params)
					if err != nil {
						utils.L().Errorw("Failed to marshal function parameters",
							"error", err,
							"function", tool.Function.Name)
						return nil, nil, nil, fmt.Errorf("failed to marshal function parameters: %w", err)
					}
					if err := json.Unmarshal(jsonBytes, &paramsMap); err != nil {
						utils.L().Errorw("Failed to unmarshal function parameters",
							"error", err,
							"function", tool.Function.Name)
						return nil, nil, nil, fmt.Errorf("failed to unmarshal function parameters: %w", err)
					}
				}

				schema = convertJSONSchemaToGenaiSchema(paramsMap)
			}

			funcDecl := &genai.FunctionDeclaration{
				Name:        tool.Function.Name,
				Description: tool.Function.Description,
				Parameters:  schema,
			}

			utils.L().Debugw("Converted function declaration",
				"name", funcDecl.Name,
				"description", funcDecl.Description,
				"has_parameters", schema != nil)

			functionDeclarations = append(functionDeclarations, funcDecl)
		}

		if len(functionDeclarations) > 0 {
			tools = append(tools, &genai.Tool{
				FunctionDeclarations: functionDeclarations,
			})
			config.Tools = tools

			utils.L().Infow("Added function declarations to config",
				"num_functions", len(functionDeclarations))
		}

		// Convert tool_choice to function calling config
		if req.ToolChoice != nil {
			toolConfig := &genai.ToolConfig{
				FunctionCallingConfig: &genai.FunctionCallingConfig{},
			}

			switch choice := req.ToolChoice.(type) {
			case string:
				switch choice {
				case "none":
					toolConfig.FunctionCallingConfig.Mode = genai.FunctionCallingConfigModeNone
				case "auto":
					toolConfig.FunctionCallingConfig.Mode = genai.FunctionCallingConfigModeAuto
				case "required":
					toolConfig.FunctionCallingConfig.Mode = genai.FunctionCallingConfigModeAny
				default:
					utils.L().Warnw("Unknown tool_choice string", "choice", choice)
				}
			case map[string]any:
				// Specific function choice: {"type": "function", "function": {"name": "..."}}
				if funcMap, ok := choice["function"].(map[string]any); ok {
					if funcName, ok := funcMap["name"].(string); ok {
						toolConfig.FunctionCallingConfig.Mode = genai.FunctionCallingConfigModeAny
						toolConfig.FunctionCallingConfig.AllowedFunctionNames = []string{funcName}
						utils.L().Infow("Tool choice set to specific function", "function", funcName)
					}
				}
			}

			config.ToolConfig = toolConfig
			utils.L().Debugw("Set tool config",
				"mode", toolConfig.FunctionCallingConfig.Mode,
				"allowed_functions", toolConfig.FunctionCallingConfig.AllowedFunctionNames)
		}
	}

	// Debug: Log what we're sending to Gemini
	utils.L().Debugw("=== ToGeminiRequest: Sending to Gemini ===",
		"num_contents", len(contents),
		"has_system_instruction", systemInstruction != nil)

	for i, content := range contents {
		utils.L().Debugw("Gemini Content",
			"index", i,
			"role", content.Role,
			"num_parts", len(content.Parts))

		for j, part := range content.Parts {
			partInfo := map[string]interface{}{
				"index":                 j,
				"has_text":              part.Text != "",
				"has_function_call":     part.FunctionCall != nil,
				"has_function_response": part.FunctionResponse != nil,
				"has_thought_signature": len(part.ThoughtSignature) > 0,
			}

			if part.FunctionCall != nil {
				partInfo["function_call_id"] = part.FunctionCall.ID
				partInfo["function_call_name"] = part.FunctionCall.Name
			}

			if part.FunctionResponse != nil {
				partInfo["function_response_id"] = part.FunctionResponse.ID
				partInfo["function_response_name"] = part.FunctionResponse.Name
			}

			if len(part.ThoughtSignature) > 0 {
				sigPreview := string(part.ThoughtSignature)
				if len(sigPreview) > 50 {
					sigPreview = sigPreview[:50]
				}
				partInfo["thought_signature_length"] = len(part.ThoughtSignature)
				partInfo["thought_signature_preview"] = sigPreview
			}

			utils.L().Debugw("  Part", "info", partInfo)
		}
	}

	return systemInstruction, contents, config, nil
}

// convertJSONSchemaToGenaiSchema converts a JSON Schema map to genai.Schema
func convertJSONSchemaToGenaiSchema(schemaMap map[string]any) *genai.Schema {
	schema := &genai.Schema{}

	if typeVal, ok := schemaMap["type"].(string); ok {
		schema.Type = genai.Type(typeVal)
	}

	if desc, ok := schemaMap["description"].(string); ok {
		schema.Description = desc
	}

	if enum, ok := schemaMap["enum"].([]any); ok {
		for _, e := range enum {
			if str, ok := e.(string); ok {
				schema.Enum = append(schema.Enum, str)
			}
		}
	}

	if format, ok := schemaMap["format"].(string); ok {
		schema.Format = format
	}

	// Handle properties (for object type)
	if props, ok := schemaMap["properties"].(map[string]any); ok {
		schema.Properties = make(map[string]*genai.Schema)
		for propName, propVal := range props {
			if propMap, ok := propVal.(map[string]any); ok {
				schema.Properties[propName] = convertJSONSchemaToGenaiSchema(propMap)
			}
		}
	}

	// Handle required fields
	if required, ok := schemaMap["required"].([]any); ok {
		for _, r := range required {
			if str, ok := r.(string); ok {
				schema.Required = append(schema.Required, str)
			}
		}
	}

	// Handle items (for array type)
	if items, ok := schemaMap["items"].(map[string]any); ok {
		schema.Items = convertJSONSchemaToGenaiSchema(items)
	}

	return schema
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
// Returns CustomChatCompletionResponse to preserve extra_content with thought_signature
func FromGeminiResponse(resp *genai.GenerateContentResponse, model string) *CustomChatCompletionResponse {
	utils.L().Debugw("=== FromGeminiResponse: Received from Gemini ===",
		"model", model,
		"num_candidates", len(resp.Candidates))

	created := time.Now().Unix()
	if !resp.CreateTime.IsZero() {
		created = resp.CreateTime.Unix()
	}

	var content string
	var toolCalls []CustomToolCall
	var finishReason string = "stop"

	if len(resp.Candidates) > 0 && resp.Candidates[0].Content != nil {
		parts := resp.Candidates[0].Content.Parts

		utils.L().Debugw("Processing Gemini response parts",
			"num_parts", len(parts),
			"model", model)

		// Debug: Log each part from Gemini
		for i, p := range parts {
			utils.L().Debugw("Gemini Response Part",
				"index", i,
				"has_text", p.Text != "",
				"has_function_call", p.FunctionCall != nil,
				"has_thought_signature", len(p.ThoughtSignature) > 0,
				"thought_signature_length", len(p.ThoughtSignature))
		}

		for i, p := range parts {
			// Handle text content
			if p.Text != "" {
				content += p.Text
				utils.L().Debugw("Found text part",
					"index", i,
					"text_length", len(p.Text))
			}

			// Handle function calls
			if p.FunctionCall != nil {
				utils.L().Infow("Found function call in response",
					"index", i,
					"function_name", p.FunctionCall.Name,
					"function_id", p.FunctionCall.ID,
					"args", p.FunctionCall.Args,
					"has_thought_signature", len(p.ThoughtSignature) > 0)

				// Log raw thought_signature details from genai library
				if len(p.ThoughtSignature) > 0 {
					sigPreview := string(p.ThoughtSignature)
					if len(sigPreview) > 50 {
						sigPreview = sigPreview[:50]
					}
					utils.L().Debugw("Raw thought_signature from genai library",
						"tool_call_id", p.FunctionCall.ID,
						"signature_length", len(p.ThoughtSignature),
						"signature_preview_bytes", fmt.Sprintf("%v", p.ThoughtSignature[:min(20, len(p.ThoughtSignature))]),
						"signature_preview_string", sigPreview)
				}

				// Convert args map to JSON string
				argsJSON, err := json.Marshal(p.FunctionCall.Args)
				if err != nil {
					utils.L().Errorw("Failed to marshal function call args",
						"error", err,
						"args", p.FunctionCall.Args)
					argsJSON = []byte("{}")
				}

				// If ID is empty, generate one
				toolCallID := p.FunctionCall.ID
				if toolCallID == "" {
					toolCallID = fmt.Sprintf("call_%d", i)
					utils.L().Debugw("Generated tool call ID", "id", toolCallID)
				}

				// Create CustomToolCall with base ToolCall fields
				toolCall := CustomToolCall{
					ToolCall: openai.ToolCall{
						ID:   toolCallID,
						Type: openai.ToolTypeFunction,
						Function: openai.FunctionCall{
							Name:      p.FunctionCall.Name,
							Arguments: string(argsJSON),
						},
					},
				}

				// Add thought_signature to extra_content following Gemini's OpenAI compatibility format
				if len(p.ThoughtSignature) > 0 {
					toolCall.ExtraContent = map[string]interface{}{
						"google": GoogleExtraContent{
							ThoughtSignature: p.ThoughtSignature,
						},
					}
					utils.L().Infow("Preserved thought_signature in extra_content",
						"tool_call_id", toolCallID,
						"signature_length", len(p.ThoughtSignature),
						"signature_type", "[]byte (will be base64-encoded in JSON)")
				}

				toolCalls = append(toolCalls, toolCall)
			}
		}

		// Get finish reason from candidate
		if resp.Candidates[0].FinishReason != "" {
			if mapped, ok := finishReasonMap[resp.Candidates[0].FinishReason]; ok {
				finishReason = string(mapped)
			}
			utils.L().Debugw("Mapped finish reason",
				"gemini_reason", resp.Candidates[0].FinishReason,
				"openai_reason", finishReason)
		}

		// If we have tool calls, set finish reason to tool_calls
		if len(toolCalls) > 0 {
			finishReason = "tool_calls"
			utils.L().Infow("Response contains tool calls",
				"num_tool_calls", len(toolCalls),
				"finish_reason", finishReason)
		}
	}

	responseID := resp.ResponseID
	if responseID == "" {
		responseID = "chatcmpl-gemini"
	}

	// Build CustomMessage with tool calls
	message := CustomMessage{
		Role:    "assistant",
		Content: content,
	}

	// Add tool calls if present
	if len(toolCalls) > 0 {
		message.ToolCalls = toolCalls
	}

	choices := []CustomChoice{
		{
			Index:        0,
			Message:      message,
			FinishReason: finishReason,
		},
	}

	usage := openai.Usage{}
	if resp.UsageMetadata != nil {
		usage.PromptTokens = int(resp.UsageMetadata.PromptTokenCount)
		usage.CompletionTokens = int(resp.UsageMetadata.CandidatesTokenCount)
		usage.TotalTokens = int(resp.UsageMetadata.PromptTokenCount + resp.UsageMetadata.CandidatesTokenCount)
	}

	customResp := &CustomChatCompletionResponse{
		ID:      responseID,
		Object:  "chat.completion",
		Created: created,
		Model:   model,
		Choices: choices,
		Usage:   usage,
	}

	// Debug: Log what we're sending to client
	respJSON, _ := json.MarshalIndent(customResp, "", "  ")
	utils.L().Debugw("=== FromGeminiResponse: Sending to Client ===",
		"response", string(respJSON))

	return customResp
}

// FromGeminiChunk converts Gemini stream chunk to OpenAI chunk
func FromGeminiChunk(resp *genai.GenerateContentResponse, model string) *openai.ChatCompletionStreamResponse {
	created := time.Now().Unix()
	if !resp.CreateTime.IsZero() {
		created = resp.CreateTime.Unix()
	}

	var content string
	var toolCalls []openai.ToolCall
	var finishReason openai.FinishReason = openai.FinishReasonNull

	if len(resp.Candidates) > 0 && resp.Candidates[0].Content != nil {
		parts := resp.Candidates[0].Content.Parts

		utils.L().Debugw("Processing Gemini stream chunk parts",
			"num_parts", len(parts),
			"model", model)

		for i, p := range parts {
			// Handle text content
			if p.Text != "" {
				content += p.Text
				utils.L().Debugw("Found text part in stream",
					"index", i,
					"text_length", len(p.Text))
			}

			// Handle function calls in stream
			if p.FunctionCall != nil {
				utils.L().Infow("Found function call in stream chunk",
					"index", i,
					"function_name", p.FunctionCall.Name,
					"function_id", p.FunctionCall.ID,
					"args", p.FunctionCall.Args,
					"has_thought_signature", len(p.ThoughtSignature) > 0)

				// Convert args map to JSON string
				argsJSON, err := json.Marshal(p.FunctionCall.Args)
				if err != nil {
					utils.L().Errorw("Failed to marshal function call args in stream",
						"error", err,
						"args", p.FunctionCall.Args)
					argsJSON = []byte("{}")
				}

				// If ID is empty, generate one
				toolCallID := p.FunctionCall.ID
				if toolCallID == "" {
					toolCallID = fmt.Sprintf("call_%d", i)
					utils.L().Debugw("Generated tool call ID in stream", "id", toolCallID)
				}

				// Build tool call with thought_signature in extra_content if present
				toolCallMap := map[string]interface{}{
					"id":   toolCallID,
					"type": string(openai.ToolTypeFunction),
					"function": map[string]interface{}{
						"name":      p.FunctionCall.Name,
						"arguments": string(argsJSON),
					},
				}

				// Add thought_signature to extra_content following Gemini's OpenAI compatibility format
				if len(p.ThoughtSignature) > 0 {
					toolCallMap["extra_content"] = map[string]interface{}{
						"google": GoogleExtraContent{
							ThoughtSignature: p.ThoughtSignature,
						},
					}
					utils.L().Infow("Preserved thought_signature in extra_content (stream)",
						"tool_call_id", toolCallID,
						"signature_length", len(p.ThoughtSignature))
				}

				// For streaming, we need to set the index
				idx := i
				toolCallMap["index"] = idx

				// Marshal to JSON and unmarshal to openai.ToolCall
				// Note: For streaming, we use the standard openai.ToolCall type
				toolCallJSON, _ := json.Marshal(toolCallMap)
				var toolCall openai.ToolCall
				if err := json.Unmarshal(toolCallJSON, &toolCall); err != nil {
					utils.L().Errorw("Failed to unmarshal tool call in stream",
						"error", err,
						"tool_call_map", toolCallMap)
					// Fallback to basic tool call
					toolCall = openai.ToolCall{
						ID:    toolCallID,
						Type:  openai.ToolTypeFunction,
						Index: &idx,
						Function: openai.FunctionCall{
							Name:      p.FunctionCall.Name,
							Arguments: string(argsJSON),
						},
					}
				}

				toolCalls = append(toolCalls, toolCall)
			}
		}

		// Only set finish reason if we have one (non-null)
		if resp.Candidates[0].FinishReason != "" && resp.Candidates[0].FinishReason != genai.FinishReasonUnspecified {
			if mapped, ok := finishReasonMap[resp.Candidates[0].FinishReason]; ok {
				finishReason = mapped
			}
			utils.L().Debugw("Mapped finish reason in stream",
				"gemini_reason", resp.Candidates[0].FinishReason,
				"openai_reason", finishReason)
		}

		// If we have tool calls, set finish reason to tool_calls
		if len(toolCalls) > 0 && finishReason != openai.FinishReasonNull {
			finishReason = openai.FinishReasonToolCalls
			utils.L().Infow("Stream chunk contains tool calls",
				"num_tool_calls", len(toolCalls),
				"finish_reason", finishReason)
		}
	}

	responseID := resp.ResponseID
	if responseID == "" {
		responseID = "chatcmpl-gemini"
	}

	delta := openai.ChatCompletionStreamChoiceDelta{
		Content: content,
	}

	// Add tool calls to delta if present
	if len(toolCalls) > 0 {
		delta.ToolCalls = toolCalls
	}

	return &openai.ChatCompletionStreamResponse{
		ID:      responseID,
		Object:  "chat.completion.chunk",
		Created: created,
		Model:   model,
		Choices: []openai.ChatCompletionStreamChoice{
			{
				Index:        0,
				Delta:        delta,
				FinishReason: finishReason,
			},
		},
	}
}
