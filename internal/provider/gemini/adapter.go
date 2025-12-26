package gemini

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/sashabaranov/go-openai"
	"github.com/sunbankio/omniproxy/pkg/utils"
	"google.golang.org/genai"
)

// ToGeminiRequest converts OpenAI request to Gemini request parts
// It returns system instruction (if any), a list of contents, and config
func ToGeminiRequest(req openai.ChatCompletionRequest) (*genai.Content, []*genai.Content, *genai.GenerateContentConfig, error) {
	var systemInstruction *genai.Content
	var contents []*genai.Content

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

					utils.L().Debugw("Converting tool call",
						"index", j,
						"id", toolCall.ID,
						"function_name", toolCall.Function.Name,
						"args", args)

					parts = append(parts, &genai.Part{
						FunctionCall: &genai.FunctionCall{
							ID:   toolCall.ID,
							Name: toolCall.Function.Name,
							Args: args,
						},
					})
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
func FromGeminiResponse(resp *genai.GenerateContentResponse, model string) *openai.ChatCompletionResponse {
	created := time.Now().Unix()
	if !resp.CreateTime.IsZero() {
		created = resp.CreateTime.Unix()
	}

	var content string
	var toolCalls []openai.ToolCall
	var finishReason openai.FinishReason = openai.FinishReasonStop

	if len(resp.Candidates) > 0 && resp.Candidates[0].Content != nil {
		parts := resp.Candidates[0].Content.Parts

		utils.L().Debugw("Processing Gemini response parts",
			"num_parts", len(parts),
			"model", model)

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
					"args", p.FunctionCall.Args)

				// Convert args map to JSON string
				argsJSON, err := json.Marshal(p.FunctionCall.Args)
				if err != nil {
					utils.L().Errorw("Failed to marshal function call args",
						"error", err,
						"args", p.FunctionCall.Args)
					argsJSON = []byte("{}")
				}

				toolCall := openai.ToolCall{
					ID:   p.FunctionCall.ID,
					Type: openai.ToolTypeFunction,
					Function: openai.FunctionCall{
						Name:      p.FunctionCall.Name,
						Arguments: string(argsJSON),
					},
				}

				// If ID is empty, generate one
				if toolCall.ID == "" {
					toolCall.ID = fmt.Sprintf("call_%d", i)
					utils.L().Debugw("Generated tool call ID", "id", toolCall.ID)
				}

				toolCalls = append(toolCalls, toolCall)
			}
		}

		// Get finish reason from candidate
		if resp.Candidates[0].FinishReason != "" {
			if mapped, ok := finishReasonMap[resp.Candidates[0].FinishReason]; ok {
				finishReason = mapped
			}
			utils.L().Debugw("Mapped finish reason",
				"gemini_reason", resp.Candidates[0].FinishReason,
				"openai_reason", finishReason)
		}

		// If we have tool calls, set finish reason to tool_calls
		if len(toolCalls) > 0 {
			finishReason = openai.FinishReasonToolCalls
			utils.L().Infow("Response contains tool calls",
				"num_tool_calls", len(toolCalls),
				"finish_reason", finishReason)
		}
	}

	responseID := resp.ResponseID
	if responseID == "" {
		responseID = "chatcmpl-gemini"
	}

	message := openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleAssistant,
		Content: content,
	}

	// Add tool calls if present
	if len(toolCalls) > 0 {
		message.ToolCalls = toolCalls
	}

	choices := []openai.ChatCompletionChoice{
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
					"args", p.FunctionCall.Args)

				// Convert args map to JSON string
				argsJSON, err := json.Marshal(p.FunctionCall.Args)
				if err != nil {
					utils.L().Errorw("Failed to marshal function call args in stream",
						"error", err,
						"args", p.FunctionCall.Args)
					argsJSON = []byte("{}")
				}

				toolCall := openai.ToolCall{
					ID:   p.FunctionCall.ID,
					Type: openai.ToolTypeFunction,
					Function: openai.FunctionCall{
						Name:      p.FunctionCall.Name,
						Arguments: string(argsJSON),
					},
				}

				// If ID is empty, generate one
				if toolCall.ID == "" {
					toolCall.ID = fmt.Sprintf("call_%d", i)
					utils.L().Debugw("Generated tool call ID in stream", "id", toolCall.ID)
				}

				// For streaming, we need to set the index
				idx := i
				toolCall.Index = &idx

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
