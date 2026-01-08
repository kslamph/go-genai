package kiro

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/sunbankio/omniproxy/internal/provider"
)

// Protocol constants for Kiro
const (
	ProtocolKiro      provider.Protocol = "kiro"
	KiroBaseURL                         = "https://codewhisperer.%s.amazonaws.com"
	GenerateEndpoint                    = "/generateAssistantResponse"
	StreamingEndpoint                   = "/SendMessageStreaming"
)

// Ensure Provider implements provider.BaseProvider
var _ provider.BaseProvider = (*Provider)(nil)

// Provider represents a Kiro Claude API provider
type Provider struct {
	auth   *Authenticator
	name   string
	region string
}

// NewProvider creates a new Kiro provider with the given authenticator
func NewProvider(name string, auth *Authenticator) *Provider {
	return &Provider{
		auth:   auth,
		name:   name,
		region: auth.GetRegion(),
	}
}

// Type returns the provider type
func (p *Provider) Type() provider.ProviderType {
	return provider.ProviderType("kiro")
}

// Name returns the provider name
func (p *Provider) Name() string {
	return p.name
}

// SupportedProtocols returns the list of protocols this provider supports
func (p *Provider) SupportedProtocols() []provider.Protocol {
	return []provider.Protocol{ProtocolKiro}
}

// GetAuth returns the authenticator
func (p *Provider) GetAuth() *Authenticator {
	return p.auth
}

// GetRegion returns the configured region
func (p *Provider) GetRegion() string {
	return p.region
}

// ListModels returns a list of supported models
func (p *Provider) ListModels(ctx context.Context) ([]string, error) {
	return []string{
		"claude-opus-4-5",
		"claude-opus-4-5-20251101",
		"claude-haiku-4-5",
		"claude-sonnet-4-5",
		"claude-sonnet-4-5-20250929",
		"claude-sonnet-4-20250514",
		"claude-3-7-sonnet-20250219",
	}, nil
}

// SupportsModel checks if the provider supports the given model
func (p *Provider) SupportsModel(model string) bool {
	supportedModels, err := p.ListModels(context.Background())
	if err != nil {
		return false
	}

	for _, supported := range supportedModels {
		if supported == model {
			return true
		}
	}
	return false
}

// ChatCompletion sends a chat completion request to Kiro
func (p *Provider) ChatCompletion(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	token, err := p.auth.GetToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get token: %w", err)
	}

	// Serialize request
	reqBody, err := json.Marshal(req.toKiroRequest(p.region))
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Build URL
	url := fmt.Sprintf(KiroBaseURL, p.region) + GenerateEndpoint

	// Create HTTP request
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers (streaming required for EventStream format)
	p.setHeaders(httpReq, token, true)

	// Send request
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("request failed with status %d: %s", resp.StatusCode, string(body))
	}

	// Parse EventStream response
	kiroResp, err := parseEventStreamResponse(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return chatResponseFromKiro(kiroResp), nil
}

// parseEventStreamResponse parses the EventStream response into KiroResponse
func parseEventStreamResponse(reader io.Reader) (*KiroResponse, error) {
	bufReader := bufio.NewReader(reader)
	var content string
	var toolUses []ToolUse
	var usage float64

	for {
		// Read total length (4 bytes)
		lenBuf := make([]byte, 4)
		_, err := io.ReadFull(bufReader, lenBuf)
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("failed to read length: %w", err)
		}
		totalLen := binary.BigEndian.Uint32(lenBuf)

		// Read header length (4 bytes)
		_, err = io.ReadFull(bufReader, lenBuf)
		if err != nil {
			return nil, fmt.Errorf("failed to read header length: %w", err)
		}
		headerLen := binary.BigEndian.Uint32(lenBuf)

		// Skip prelude CRC (4 bytes)
		_, err = bufReader.Discard(4)
		if err != nil {
			return nil, fmt.Errorf("failed to skip prelude CRC: %w", err)
		}

		// Skip header (headerLen bytes)
		_, err = bufReader.Discard(int(headerLen))
		if err != nil {
			return nil, fmt.Errorf("failed to skip header: %w", err)
		}

		// Read payload
		payloadLen := int(totalLen) - 16 - int(headerLen)
		if payloadLen < 0 {
			return nil, fmt.Errorf("invalid payload length: %d", payloadLen)
		}
		payload := make([]byte, payloadLen)
		_, err = io.ReadFull(bufReader, payload)
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("failed to read payload: %w", err)
		}

		// Skip message CRC (4 bytes)
		_, err = bufReader.Discard(4)
		if err != nil {
			return nil, fmt.Errorf("failed to skip message CRC: %w", err)
		}

		// Parse payload as JSON
		var event map[string]interface{}
		if err := json.Unmarshal(payload, &event); err != nil {
			continue
		}

		// Extract content (concatenate all content events)
		if c, ok := event["content"].(string); ok {
			content += c
		}

		// Extract tool uses
		if name, ok := event["name"].(string); ok {
			if toolUseID, ok := event["toolUseId"].(string); ok {
				var input json.RawMessage
				if i, ok := event["input"].(string); ok {
					input = json.RawMessage(i)
				}
				toolUses = append(toolUses, ToolUse{
					Name:      name,
					ToolUseID: toolUseID,
					Input:     input,
				})
			}
		}

		// Extract usage
		if u, ok := event["usage"].(float64); ok {
			usage = u
		}
	}

	return &KiroResponse{
		Content:  content,
		ToolUses: toolUses,
		Usage:    usage,
	}, nil
}

// StreamChatCompletion sends a streaming chat completion request to Kiro
func (p *Provider) StreamChatCompletion(ctx context.Context, req *ChatRequest) (<-chan StreamEvent, <-chan error, error) {
	token, err := p.auth.GetToken(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get token: %w", err)
	}

	// Serialize request
	reqBody, err := json.Marshal(req.toKiroRequest(p.region))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Build URL
	url := fmt.Sprintf(KiroBaseURL, p.region) + StreamingEndpoint

	// Create HTTP request
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(reqBody))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers for streaming
	p.setHeaders(httpReq, token, true)

	// Send request
	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to send request: %w", err)
	}

	eventChan := make(chan StreamEvent)
	errChan := make(chan error, 1)

	go func() {
		defer close(eventChan)
		defer resp.Body.Close()

		if err := p.parseEventStream(resp.Body, eventChan); err != nil && err != io.EOF {
			errChan <- err
		}
	}()

	return eventChan, errChan, nil
}

// setHeaders sets the required HTTP headers for Kiro requests
func (p *Provider) setHeaders(req *http.Request, token string, isStream bool) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("amz-sdk-request", "attempt=1; max=1")
	req.Header.Set("amz-sdk-invocation-id", generateUUID())
	req.Header.Set("x-amzn-kiro-agent-mode", "vibe")
	req.Header.Set("x-amz-user-agent", "aws-sdk-js/1.0.0 KiroIDE-0.7.5-"+p.auth.GenerateMachineID())
	req.Header.Set("user-agent", "aws-sdk-js/1.0.0 ua/2.1 os/linux lang/go api/codewhispererruntime#1.0.0 m/E")
	req.Header.Set("Connection", "close")

	if isStream {
		req.Header.Set("Accept", "application/vnd.amazon.eventstream")
	} else {
		req.Header.Set("Accept", "application/json")
	}
}

// parseEventStream parses the AWS EventStream response
func (p *Provider) parseEventStream(reader io.Reader, eventChan chan<- StreamEvent) error {
	bufReader := bufio.NewReader(reader)

	for {
		// Read total length (4 bytes)
		lenBuf := make([]byte, 4)
		_, err := io.ReadFull(bufReader, lenBuf)
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("failed to read length: %w", err)
		}
		totalLen := binary.BigEndian.Uint32(lenBuf)

		// Read header length (4 bytes)
		_, err = io.ReadFull(bufReader, lenBuf)
		if err != nil {
			return fmt.Errorf("failed to read header length: %w", err)
		}
		headerLen := binary.BigEndian.Uint32(lenBuf)

		// Skip prelude CRC (4 bytes)
		_, err = bufReader.Discard(4)
		if err != nil {
			return fmt.Errorf("failed to skip prelude CRC: %w", err)
		}

		// Skip header (headerLen bytes)
		_, err = bufReader.Discard(int(headerLen))
		if err != nil {
			return fmt.Errorf("failed to skip header: %w", err)
		}

		// Read payload
		payloadLen := int(totalLen) - 16 - int(headerLen)
		if payloadLen < 0 {
			return fmt.Errorf("invalid payload length: %d", payloadLen)
		}
		payload := make([]byte, payloadLen)
		_, err = io.ReadFull(bufReader, payload)
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("failed to read payload: %w", err)
		}

		// Skip message CRC (4 bytes)
		_, err = bufReader.Discard(4)
		if err != nil {
			return fmt.Errorf("failed to skip message CRC: %w", err)
		}

		// Parse payload as JSON
		var event map[string]interface{}
		if err := json.Unmarshal(payload, &event); err != nil {
			// Try to parse as SSE format
			if sseEvent := parseSSEEvent(payload); sseEvent != nil {
				eventChan <- *sseEvent
			}
			continue
		}

		eventChan <- StreamEvent{
			Type:    getEventType(event),
			Content: event,
		}
	}
}

// parseSSEEvent attempts to parse an SSE-style event from the payload
func parseSSEEvent(payload []byte) *StreamEvent {
	// Try to find JSON after :message-typeevent
	str := string(payload)
	if idx := strings.Index(str, ":message-typeevent"); idx != -1 {
		jsonStart := idx + len(":message-typeevent")
		var jsonData []byte
		for jsonStart < len(str) && (str[jsonStart] == ' ' || str[jsonStart] == '\n' || str[jsonStart] == '\t') {
			jsonStart++
		}
		jsonData = []byte(str[jsonStart:])
		if len(jsonData) > 0 {
			var event map[string]interface{}
			if err := json.Unmarshal(jsonData, &event); err == nil {
				return &StreamEvent{
					Type:    getEventType(event),
					Content: event,
				}
			}
		}
	}
	return nil
}

// getEventType determines the event type from the content
func getEventType(event map[string]interface{}) string {
	if _, ok := event["content"].(string); ok {
		return "content"
	}
	if _, ok := event["name"].(string); ok {
		if _, hasToolUseID := event["toolUseId"]; hasToolUseID {
			return "tool_use"
		}
		return "name"
	}
	if _, ok := event["unit"]; ok {
		return "usage"
	}
	if _, ok := event["contextUsagePercentage"]; ok {
		return "context_usage"
	}
	return "unknown"
}

// getBaseURL returns the base URL for the given region
func getBaseURL(region string) string {
	return fmt.Sprintf(KiroBaseURL, region)
}

// generateUUID generates a simple UUID-like string
func generateUUID() string {
	return fmt.Sprintf("%x", time.Now().UnixNano())
}

// ============ Chat Request/Response Types ============

// ChatRequest represents a chat completion request in OpenAI-like format
type ChatRequest struct {
	Messages    []ChatMessage `json:"messages"`
	Model       string        `json:"model"`
	Stream      bool          `json:"stream,omitempty"`
	Tools       []Tool        `json:"tools,omitempty"`
	MaxTokens   *int          `json:"max_tokens,omitempty"`
	Temperature *float64      `json:"temperature,omitempty"`
}

// ChatMessage represents a chat message
type ChatMessage struct {
	Role       string      `json:"role"`
	Content    interface{} `json:"content"`
	ToolCalls  []ToolCall  `json:"tool_calls,omitempty"`
	ToolCallID string      `json:"tool_call_id,omitempty"`
}

// ToolCall represents a tool call from the model
type ToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function ToolCallFunction `json:"function"`
}

// ToolCallFunction represents the function part of a tool call
type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Tool represents a tool definition
type Tool struct {
	Type     string           `json:"type"`
	Function ToolFunctionSpec `json:"function"`
}

// ToolFunctionSpec represents a function tool specification
type ToolFunctionSpec struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"`
}

// ChatResponse represents a chat completion response in OpenAI-like format
type ChatResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   *Usage   `json:"usage,omitempty"`
}

// Choice represents a choice in the chat response
type Choice struct {
	Index        int         `json:"index"`
	Message      ChatMessage `json:"message"`
	FinishReason string      `json:"finish_reason,omitempty"`
}

// Usage represents token usage information
type Usage struct {
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	TotalTokens      int     `json:"total_tokens"`
	CreditUsage      float64 `json:"credit_usage,omitempty"`
}

// StreamEvent represents a streaming event
type StreamEvent struct {
	Type    string
	Content map[string]interface{}
}

// toKiroRequest converts a ChatRequest to Kiro's request format
func (r *ChatRequest) toKiroRequest(region string) *KiroRequest {
	conversationID := generateUUID()
	kiroModel := getKiroModel(r.Model)

	// Build messages (merging system messages with first user message)
	messages := buildMessages(r.Messages)

	// Build tools
	kiroTools := make([]KiroTool, len(r.Tools))
	for i, t := range r.Tools {
		kiroTools[i] = KiroTool{
			ToolSpecification: ToolSpecification{
				Name:        t.Function.Name,
				Description: t.Function.Description,
				InputSchema: InputSchema{
					JSON: json.RawMessage(marshalParams(t.Function.Parameters)),
				},
			},
		}
	}

	return &KiroRequest{
		ConversationState: ConversationState{
			ChatTriggerType: "MANUAL",
			ConversationID:  conversationID,
			CurrentMessage: CurrentMessage{
				UserInputMessage: UserInputMessage{
					Content: messages,
					ModelID: kiroModel,
					Origin:  "AI_EDITOR",
					UserInputMessageContext: &UserInputMessageContext{
						Tools: kiroTools,
					},
				},
			},
		},
		ProfileArn: getProfileArn(r.Model),
	}
}

// buildMessages combines system message with user content
func buildMessages(msgs []ChatMessage) string {
	var systemContent string
	var userContent string

	for _, msg := range msgs {
		switch msg.Role {
		case "system":
			systemContent += msg.Content.(string) + "\n"
		case "user":
			if str, ok := msg.Content.(string); ok {
				userContent = str
			} else if arr, ok := msg.Content.([]interface{}); ok {
				// Handle array content
				for _, item := range arr {
					if m, ok := item.(map[string]interface{}); ok {
						if t, ok := m["type"].(string); ok && t == "text" {
							if c, ok := m["text"].(string); ok {
								userContent += c
							}
						}
					}
				}
			}
		}
	}

	// Combine system and user messages
	if systemContent != "" {
		return strings.TrimSpace(systemContent) + "\n\n" + strings.TrimSpace(userContent)
	}
	return strings.TrimSpace(userContent)
}

// marshalParams converts parameters map to JSON bytes
func marshalParams(params map[string]interface{}) []byte {
	if params == nil {
		return []byte(`{"type": "object", "properties": {}}`)
	}
	data, _ := json.Marshal(params)
	return data
}

// getProfileArn returns the profile ARN if using social auth
func getProfileArn(model string) *string {
	// Social auth requires profile ARN - this is set during authentication
	// For now, return nil as it should be loaded from credentials
	return nil
}

// getKiroModel maps OpenAI model names to Kiro model names
func getKiroModel(openaiModel string) string {
	modelMap := map[string]string{
		"claude-opus-4-5":            "claude-opus-4.5",
		"claude-opus-4-5-20251101":   "claude-opus-4.5",
		"claude-haiku-4-5":           "claude-haiku-4.5",
		"claude-haiku-4-5-20251001":  "claude-haiku-4.5",
		"claude-sonnet-4-5":          "CLAUDE_SONNET_4_5_20250929_V1_0",
		"claude-sonnet-4-5-20250929": "CLAUDE_SONNET_4_5_20250929_V1_0",
		"claude-sonnet-4-20250514":   "CLAUDE_SONNET_4_20250514_V1_0",
		"claude-3-7-sonnet-20250219": "CLAUDE_3_7_SONNET_20250219_V1_0",
		"claude-3-5-sonnet-20241022": "CLAUDE_3_7_SONNET_20250219_V1_0",
		"claude-3-5-sonnet-latest":   "CLAUDE_3_7_SONNET_20250219_V1_0",
	}

	if kiroModel, exists := modelMap[openaiModel]; exists {
		return kiroModel
	}
	// Default to Sonnet 4.5
	return "CLAUDE_SONNET_4_5_20250929_V1_0"
}

// ============ Kiro Native Types ============

// KiroRequest is the native Kiro API request format
type KiroRequest struct {
	ConversationState ConversationState `json:"conversationState"`
	ProfileArn        *string           `json:"profileArn,omitempty"`
}

// ConversationState represents the conversation state
type ConversationState struct {
	ChatTriggerType string         `json:"chatTriggerType"`
	ConversationID  string         `json:"conversationId"`
	CurrentMessage  CurrentMessage `json:"currentMessage"`
	History         []HistoryItem  `json:"history,omitempty"`
}

// CurrentMessage represents the current message in the conversation
type CurrentMessage struct {
	UserInputMessage UserInputMessage `json:"userInputMessage"`
}

// UserInputMessage represents a user input message
type UserInputMessage struct {
	Content                 string                   `json:"content"`
	ModelID                 string                   `json:"modelId"`
	Origin                  string                   `json:"origin"`
	UserInputMessageContext *UserInputMessageContext `json:"userInputMessageContext,omitempty"`
}

// UserInputMessageContext contains tools and tool results
type UserInputMessageContext struct {
	Tools       []KiroTool   `json:"tools,omitempty"`
	ToolResults []ToolResult `json:"toolResults,omitempty"`
}

// KiroTool represents a tool definition in Kiro format
type KiroTool struct {
	ToolSpecification ToolSpecification `json:"toolSpecification"`
}

// ToolSpecification represents a tool specification
type ToolSpecification struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema InputSchema `json:"inputSchema"`
}

// InputSchema represents the input schema for a tool
type InputSchema struct {
	JSON json.RawMessage `json:"json"`
}

// ToolResult represents a tool execution result
type ToolResult struct {
	Content   []TextContent `json:"content"`
	Status    string        `json:"status"`
	ToolUseID string        `json:"toolUseId"`
}

// TextContent represents text content
type TextContent struct {
	Text string `json:"text"`
}

// HistoryItem represents a historical message
type HistoryItem struct {
	UserInputMessage         *UserInputMessage         `json:"userInputMessage,omitempty"`
	AssistantResponseMessage *AssistantResponseMessage `json:"assistantResponseMessage,omitempty"`
}

// AssistantResponseMessage represents an assistant response
type AssistantResponseMessage struct {
	Content  string    `json:"content"`
	ToolUses []ToolUse `json:"toolUses,omitempty"`
}

// ToolUse represents a tool usage in the response
type ToolUse struct {
	Input     json.RawMessage `json:"input"`
	Name      string          `json:"name"`
	ToolUseID string          `json:"toolUseId"`
}

// KiroResponse is the native Kiro API response format
type KiroResponse struct {
	ConversationID         string             `json:"conversationId"`
	Content                string             `json:"content,omitempty"`
	ContentPartition       []ContentPartition `json:"contentPartition,omitempty"`
	FollowupPrompt         *FollowupPrompt    `json:"followupPrompt,omitempty"`
	SupplementaryWebLinks  []interface{}      `json:"supplementaryWebLinks,omitempty"`
	ContextUsagePercentage float64            `json:"contextUsagePercentage,omitempty"`
	Unit                   string             `json:"unit,omitempty"`
	UnitPlural             string             `json:"unitPlural,omitempty"`
	Usage                  float64            `json:"usage,omitempty"`
	ToolUses               []ToolUse          `json:"toolUses,omitempty"`
	// Additional fields may be present
}

// ContentPartition represents a partition of content
type ContentPartition struct {
	Text string `json:"text"`
}

// FollowupPrompt represents a suggested follow-up prompt
type FollowupPrompt struct {
	Content string `json:"content"`
}

// chatResponseFromKiro converts a Kiro response to OpenAI-like format
func chatResponseFromKiro(resp *KiroResponse) *ChatResponse {
	choices := []Choice{
		{
			Index: 0,
			Message: ChatMessage{
				Role:    "assistant",
				Content: resp.Content,
			},
			FinishReason: "stop",
		},
	}

	usage := &Usage{}
	if resp.Usage > 0 {
		usage.CreditUsage = resp.Usage
	}

	return &ChatResponse{
		ID:      resp.ConversationID,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   "claude-sonnet-4-5",
		Choices: choices,
		Usage:   usage,
	}
}
