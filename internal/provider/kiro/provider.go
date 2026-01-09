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
	"os"
	"strings"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/sunbankio/omniproxy/internal/provider"
)

// Protocol constants for Kiro
const (
	ProtocolKiro      provider.Protocol = "kiro"
	KiroBaseURL                         = "https://codewhisperer.%s.amazonaws.com"
	GenerateEndpoint                    = "/generateAssistantResponse"
	StreamingEndpoint                   = "/SendMessageStreaming"
)

// Ensure Provider implements provider.BaseProvider and provider.OpenAICompatibleProvider
var _ provider.BaseProvider = (*Provider)(nil)
var _ provider.OpenAICompatibleProvider = (*Provider)(nil)

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

// chatCompletionInternal sends a chat completion request to Kiro (internal method)
func (p *Provider) chatCompletionInternal(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	token, err := p.auth.GetToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get token: %w", err)
	}

	// Serialize request
	kiroReq := req.toKiroRequest(p.region)
	reqBody, err := json.Marshal(kiroReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Debug: write comprehensive request/response logging
	timestamp := time.Now().Format("20060102-150405.000")
	logFile := fmt.Sprintf("./logs/kiro-request-%s.log", timestamp)

	// 1. LOG USER'S ORIGINAL REQUEST
	logContent := fmt.Sprintf("=== COMPREHENSIVE KIRO DEBUG LOG at %s ===\n\n", timestamp)
	logContent += fmt.Sprintf("1. USER'S ORIGINAL REQUEST TO OMNIPROXY:\n")
	logContent += fmt.Sprintf("   Model: %s\n", req.Model)
	logContent += fmt.Sprintf("   Stream: %v\n", req.Stream)
	logContent += fmt.Sprintf("   Messages (%d):\n", len(req.Messages))
	for i, msg := range req.Messages {
		msgJSON, _ := json.MarshalIndent(msg, "      ", "  ")
		logContent += fmt.Sprintf("     Message %d: %s\n", i, string(msgJSON))
	}
	if len(req.Tools) > 0 {
		logContent += fmt.Sprintf("   Tools (%d):\n", len(req.Tools))
		for i, tool := range req.Tools {
			toolJSON, _ := json.MarshalIndent(tool, "      ", "  ")
			logContent += fmt.Sprintf("     Tool %d: %s\n", i, string(toolJSON))
		}
	}

	// 2. LOG OUR REQUEST TO KIRO
	reqJSON, _ := json.MarshalIndent(kiroReq, "  ", "  ")
	logContent += fmt.Sprintf("\n2. OUR REQUEST TO KIRO API:\n")
	url := fmt.Sprintf(KiroBaseURL, p.region) + GenerateEndpoint
	logContent += fmt.Sprintf("   URL: %s\n", url)
	logContent += fmt.Sprintf("   Headers: Content-Type=application/json, Accept=application/vnd.amazon.eventstream\n")
	logContent += fmt.Sprintf("   Body:\n%s\n", string(reqJSON))

	if err := os.WriteFile(logFile, []byte(logContent), 0644); err != nil {
		fmt.Printf("[Kiro DEBUG] Failed to write log file: %v\n", err)
	} else {
		fmt.Printf("[Kiro DEBUG] Request logged to: %s\n", logFile)
	}

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

	// 3. LOG KIRO'S RESPONSE
	logContent += fmt.Sprintf("\n3. KIRO'S RESPONSE:\n")
	logContent += fmt.Sprintf("   Status Code: %d\n", resp.StatusCode)
	logContent += fmt.Sprintf("   Headers:\n")
	for k, v := range resp.Header {
		logContent += fmt.Sprintf("     %s: %v\n", k, v)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		logContent += fmt.Sprintf("   Error Body: %s\n", string(body))

		// Write error log
		if err := os.WriteFile(logFile, []byte(logContent), 0644); err != nil {
			fmt.Printf("[Kiro DEBUG] Failed to write error log: %v\n", err)
		}

		return nil, fmt.Errorf("request failed with status %d: %s", resp.StatusCode, string(body))
	}

	// Parse EventStream response
	kiroResp, err := parseEventStreamResponse(resp.Body)
	if err != nil {
		logContent += fmt.Sprintf("   Parse Error: %v\n", err)
		if err := os.WriteFile(logFile, []byte(logContent), 0644); err != nil {
			fmt.Printf("[Kiro DEBUG] Failed to write error log: %v\n", err)
		}
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	// Log parsed response
	logContent += fmt.Sprintf("   Response Body: [Binary EventStream data - parsed successfully]\n")
	logContent += fmt.Sprintf("   Parsed Content: %s\n", kiroResp.Content)
	logContent += fmt.Sprintf("   Tool Uses: %d\n", len(kiroResp.ToolUses))
	for i, toolUse := range kiroResp.ToolUses {
		toolJSON, _ := json.MarshalIndent(toolUse, "      ", "  ")
		logContent += fmt.Sprintf("     Tool Use %d: %s\n", i, string(toolJSON))
	}

	// Convert to OpenAI format
	openaiResp := chatResponseFromKiro(kiroResp)

	// 4. LOG OUR RESPONSE TO CLIENT
	logContent += fmt.Sprintf("\n4. OUR RESPONSE TO CLIENT:\n")
	respJSON, _ := json.MarshalIndent(openaiResp, "  ", "  ")
	logContent += fmt.Sprintf("   Body:\n%s\n", string(respJSON))

	logContent += fmt.Sprintf("\n=== END OF COMPREHENSIVE LOG ===\n")

	// Write final log
	if err := os.WriteFile(logFile, []byte(logContent), 0644); err != nil {
		fmt.Printf("[Kiro DEBUG] Failed to write final log: %v\n", err)
	} else {
		fmt.Printf("[Kiro DEBUG] Comprehensive log written to: %s\n", logFile)
	}

	return openaiResp, nil
}

// ToolCallAccumulator helps accumulate tool call arguments across multiple EventStream chunks
type ToolCallAccumulator struct {
	ID        string
	Name      string
	Arguments strings.Builder
	Complete  bool
}

// StreamingToolCallAccumulator helps accumulate streaming tool call arguments
type StreamingToolCallAccumulator struct {
	toolCalls map[string]*ToolCallAccumulator
}

// NewStreamingToolCallAccumulator creates a new streaming accumulator
func NewStreamingToolCallAccumulator() *StreamingToolCallAccumulator {
	return &StreamingToolCallAccumulator{
		toolCalls: make(map[string]*ToolCallAccumulator),
	}
}

// AccumulateToolCall accumulates tool call data from streaming events
func (s *StreamingToolCallAccumulator) AccumulateToolCall(toolUseID, name, input string) {
	accumulator, exists := s.toolCalls[toolUseID]
	if !exists {
		accumulator = &ToolCallAccumulator{
			ID:   toolUseID,
			Name: name,
		}
		s.toolCalls[toolUseID] = accumulator
		fmt.Printf("[Kiro DEBUG] Created streaming tool call accumulator for ID: %s, Name: %s\n", toolUseID, name)
	}

	if input != "" {
		accumulator.Arguments.WriteString(input)
		fmt.Printf("[Kiro DEBUG] Accumulated %d chars for streaming tool %s (total: %d)\n",
			len(input), toolUseID, accumulator.Arguments.Len())
	}
}

// GetFormattedArguments returns the formatted arguments for a tool call
func (s *StreamingToolCallAccumulator) GetFormattedArguments(toolUseID string) string {
	if accumulator, exists := s.toolCalls[toolUseID]; exists {
		argumentsStr := accumulator.Arguments.String()
		return formatToolArguments(argumentsStr)
	}
	return "{}"
}

// parseEventStreamResponse parses the EventStream response into KiroResponse
func parseEventStreamResponse(reader io.Reader) (*KiroResponse, error) {
	bufReader := bufio.NewReader(reader)
	var content string
	var toolUses []ToolUse
	var usage float64
	// Map to accumulate tool call arguments by ID
	toolCallMap := make(map[string]*ToolCallAccumulator)
	// Track all decoded events for logging
	var decodedEvents []map[string]interface{}

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
			fmt.Printf("[Kiro DEBUG] Failed to parse EventStream payload as JSON: %v\n", err)
			continue
		}

		// Store decoded event for logging
		decodedEvents = append(decodedEvents, event)

		// Extract content (concatenate all content events)
		if c, ok := event["content"].(string); ok {
			content += c
		}

		// Enhanced tool call parsing with proper argument accumulation
		if name, ok := event["name"].(string); ok {
			if toolUseID, ok := event["toolUseId"].(string); ok {
				// Get or create accumulator for this tool call
				accumulator, exists := toolCallMap[toolUseID]
				if !exists {
					accumulator = &ToolCallAccumulator{
						ID:   toolUseID,
						Name: name,
					}
					toolCallMap[toolUseID] = accumulator
					fmt.Printf("[Kiro DEBUG] Created new tool call accumulator for ID: %s, Name: %s\n", toolUseID, name)
				}

				// Accumulate input arguments
				if input, ok := event["input"].(string); ok && input != "" {
					accumulator.Arguments.WriteString(input)
					fmt.Printf("[Kiro DEBUG] Accumulated %d chars for tool %s (total: %d)\n",
						len(input), toolUseID, accumulator.Arguments.Len())
				}

				// Mark complete if this is the final chunk (some EventStream implementations send this)
				if isComplete, ok := event["complete"].(bool); ok && isComplete {
					accumulator.Complete = true
					fmt.Printf("[Kiro DEBUG] Tool call %s marked as complete\n", toolUseID)
				}
			}
		}

		// Extract usage
		if u, ok := event["usage"].(float64); ok {
			usage = u
		}
	}

	// Convert accumulated tool calls to ToolUse format
	toolUses = convertAccumulatedToolCalls(toolCallMap)

	// Log all decoded events to file
	logDecodedEvents(decodedEvents)

	return &KiroResponse{
		Content:  content,
		ToolUses: toolUses,
		Usage:    usage,
	}, nil
}

// logDecodedEvents logs all decoded EventStream events to a file
func logDecodedEvents(events []map[string]interface{}) {
	timestamp := time.Now().Format("20060102-150405.000")
	logFile := fmt.Sprintf("./logs/kiro-decoded-events-%s.log", timestamp)
	
	var logContent strings.Builder
	logContent.WriteString(fmt.Sprintf("=== Kiro Decoded EventStream Events at %s ===\n\n", timestamp))
	logContent.WriteString(fmt.Sprintf("Total Events: %d\n\n", len(events)))
	
	for i, event := range events {
		eventJSON, _ := json.MarshalIndent(event, "", "  ")
		logContent.WriteString(fmt.Sprintf("Event %d:\n%s\n\n", i, string(eventJSON)))
	}
	
	logContent.WriteString("=== End of Decoded Events ===\n")
	
	if err := os.WriteFile(logFile, []byte(logContent.String()), 0644); err != nil {
		fmt.Printf("[Kiro DEBUG] Failed to write decoded events log: %v\n", err)
	} else {
		fmt.Printf("[Kiro DEBUG] Decoded events logged to: %s\n", logFile)
	}
}

// convertAccumulatedToolCalls converts the accumulated tool calls to ToolUse format
func convertAccumulatedToolCalls(toolCallMap map[string]*ToolCallAccumulator) []ToolUse {
	var toolUses []ToolUse

	for _, accumulator := range toolCallMap {
		argumentsStr := accumulator.Arguments.String()

		// Format and validate the arguments as JSON
		formattedArgs := formatToolArguments(argumentsStr)

		fmt.Printf("[Kiro DEBUG] Converting tool call %s: raw args (%d chars) -> formatted args (%d chars)\n",
			accumulator.ID, len(argumentsStr), len(formattedArgs))

		toolUses = append(toolUses, ToolUse{
			Name:      accumulator.Name,
			ToolUseID: accumulator.ID,
			Input:     json.RawMessage(formattedArgs),
		})
	}

	return toolUses
}

// formatToolArguments ensures tool arguments are properly formatted as JSON strings
func formatToolArguments(input string) string {
	// Handle empty input
	if input == "" {
		return "{}"
	}

	// Try to parse as JSON to validate structure
	var temp interface{}
	if err := json.Unmarshal([]byte(input), &temp); err != nil {
		fmt.Printf("[Kiro DEBUG] Invalid JSON in tool arguments, using empty object: %v\n", err)
		return "{}"
	}

	// Return the validated JSON string
	return input
}

// streamChatCompletionInternal sends a streaming chat completion request to Kiro (internal method)
func (p *Provider) streamChatCompletionInternal(ctx context.Context, req *ChatRequest) (<-chan StreamEvent, <-chan error, error) {
	token, err := p.auth.GetToken(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get token: %w", err)
	}

	// Serialize request
	kiroReq := req.toKiroRequest(p.region)
	reqBody, err := json.Marshal(kiroReq)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Debug: write the full request to a log file
	reqJSON, _ := json.MarshalIndent(kiroReq, "  ", "  ")
	timestamp := time.Now().Format("20060102-150405.000")
	logFile := fmt.Sprintf("./logs/kiro-request-%s.log", timestamp)
	logContent := fmt.Sprintf("=== Kiro Request at %s ===\n\n", timestamp)
	logContent += fmt.Sprintf("Full Kiro Request:\n%s\n\n", string(reqJSON))
	logContent += fmt.Sprintf("Original OpenAI Request Messages:\n")
	for i, msg := range req.Messages {
		msgJSON, _ := json.MarshalIndent(msg, "    ", "  ")
		logContent += fmt.Sprintf("  Message %d: %s\n", i, string(msgJSON))
	}
	logContent += fmt.Sprintf("\n=== End of Request ===\n")

	if err := os.WriteFile(logFile, []byte(logContent), 0644); err != nil {
		fmt.Printf("[Kiro DEBUG] Failed to write log file: %v\n", err)
	} else {
		fmt.Printf("[Kiro DEBUG] Request logged to: %s\n", logFile)
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

	// Check HTTP status code BEFORE reading body
	// IMPORTANT: Kiro returns JSON error responses for 4xx/5xx, NOT EventStream
	if resp.StatusCode != http.StatusOK {
		// Read error body (JSON format)
		errorBody, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, nil, fmt.Errorf("request failed with status %d: failed to read error body", resp.StatusCode)
		}
		
		// Log the error response
		logContent += fmt.Sprintf("\n=== Kiro Response ===\n")
		logContent += fmt.Sprintf("Status Code: %d\n", resp.StatusCode)
		logContent += fmt.Sprintf("Headers:\n")
		for k, v := range resp.Header {
			logContent += fmt.Sprintf("  %s: %v\n", k, v)
		}
		logContent += fmt.Sprintf("\nError Body (JSON): %s\n", string(errorBody))
		
		// Write error log
		if err := os.WriteFile(logFile, []byte(logContent), 0644); err != nil {
			fmt.Printf("[Kiro DEBUG] Failed to write error log: %v\n", err)
		}
		
		return nil, nil, fmt.Errorf("request failed with status %d: %s", resp.StatusCode, string(errorBody))
	}

	// Read response body for logging (only for successful responses)
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		resp.Body.Close()
		return nil, nil, fmt.Errorf("failed to read response body: %w", err)
	}
	resp.Body.Close()

	// Log Kiro's response
	logContent += fmt.Sprintf("\n=== Kiro Response ===\n")
	logContent += fmt.Sprintf("Status Code: %d\n", resp.StatusCode)
	logContent += fmt.Sprintf("Headers:\n")
	for k, v := range resp.Header {
		logContent += fmt.Sprintf("  %s: %v\n", k, v)
	}
	logContent += fmt.Sprintf("\nResponse Body (raw binary EventStream, %d bytes):\n", len(respBody))
	logContent += "[Binary EventStream data - not human-readable]\n"

	eventChan := make(chan StreamEvent)
	errChan := make(chan error, 1)

	// Capture stream events for logging
	var capturedEvents []StreamEvent

	go func() {
		defer close(eventChan)

		// Re-create reader for parsing
		reader := bytes.NewReader(respBody)
		if err := p.parseEventStream(reader, eventChan); err != nil && err != io.EOF {
			logContent += fmt.Sprintf("\nError parsing event stream: %v\n", err)
			errChan <- err
		}
	}()

	// Capture all events for logging
	go func() {
		for event := range eventChan {
			capturedEvents = append(capturedEvents, event)
		}
	}()

	// Wait for events to be processed and then log them
	go func() {
		time.Sleep(2 * time.Second) // Give time to capture all events

		// Log captured events from Kiro
		logContent += "\n=== Captured Stream Events from Kiro ===\n"
		for i, event := range capturedEvents {
			eventJSON, _ := json.MarshalIndent(event, "  ", "  ")
			logContent += fmt.Sprintf("Event %d (Type: %s):\n%s\n\n", i, event.Type, string(eventJSON))
		}

		// Write to file
		if err := os.WriteFile(logFile, []byte(logContent), 0644); err != nil {
			fmt.Printf("[Kiro DEBUG] Failed to write log file: %v\n", err)
		} else {
			fmt.Printf("[Kiro DEBUG] Request logged to: %s\n", logFile)
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

// ChatCompletion implements the OpenAICompatibleProvider interface
func (p *Provider) ChatCompletion(ctx context.Context, req openai.ChatCompletionNewParams) (interface{}, error) {
	// Convert OpenAI request to internal ChatRequest
	chatReq, err := convertOpenAIToChatRequest(req)
	if err != nil {
		return nil, p.wrapError(fmt.Errorf("failed to convert OpenAI request: %w", err))
	}

	// Call internal chatCompletionInternal
	resp, err := p.chatCompletionInternal(ctx, chatReq)
	if err != nil {
		return nil, p.wrapError(err)
	}

	// Convert internal ChatResponse to OpenAI format
	openaiResp, err := convertChatResponseToOpenAI(resp)
	if err != nil {
		return nil, p.wrapError(fmt.Errorf("failed to convert response: %w", err))
	}

	return openaiResp, nil
}

// StreamChatCompletion implements the OpenAICompatibleProvider interface
func (p *Provider) StreamChatCompletion(ctx context.Context, req openai.ChatCompletionNewParams) (<-chan openai.ChatCompletionChunk, <-chan error, error) {
	// Convert OpenAI request to internal ChatRequest
	chatReq, err := convertOpenAIToChatRequest(req)
	if err != nil {
		return nil, nil, p.wrapError(fmt.Errorf("failed to convert OpenAI request: %w", err))
	}

	// Call internal streamChatCompletionInternal
	eventChan, errChan, err := p.streamChatCompletionInternal(ctx, chatReq)
	if err != nil {
		return nil, nil, p.wrapError(err)
	}

	// Convert StreamEvent to OpenAI ChatCompletionChunk
	openaiChunkChan := make(chan openai.ChatCompletionChunk)
	openaiErrChan := make(chan error, 1)

	// Capture response chunks for logging
	var capturedChunks []openai.ChatCompletionChunk
	var logFile string

	go func() {
		defer close(openaiChunkChan)
		defer close(openaiErrChan)

		state := newKiroStreamState()

		for event := range eventChan {
			chunk, shouldSend, err := convertStreamEventToOpenAIChunk(event, state)
			if err != nil {
				openaiErrChan <- p.wrapError(fmt.Errorf("failed to convert stream event: %w", err))
				return
			}
			if !shouldSend {
				continue
			}
			capturedChunks = append(capturedChunks, chunk)
			openaiChunkChan <- chunk
		}

		// Send final chunk with finish reason
		finishReason := "stop"
		if state.nextToolIndex > 0 {
			finishReason = "tool_calls"
		}

		finalChunk := openai.ChatCompletionChunk{
			ID:      generateUUID(),
			Object:  "chat.completion.chunk",
			Created: time.Now().Unix(),
			Model:   "claude-sonnet-4-5",
			Choices: []openai.ChatCompletionChunkChoice{
				{
					Index:        0,
					Delta:        openai.ChatCompletionChunkChoiceDelta{},
					FinishReason: finishReason,
				},
			},
		}
		openaiChunkChan <- finalChunk

		// Check for errors
		select {
		case err := <-errChan:
			if err != nil && err.Error() != "EOF" {
				openaiErrChan <- p.wrapError(err)
			}
		default:
		}

		// Append response to log file
		if len(capturedChunks) > 0 {
			// Find the log file that was created
			files, _ := os.ReadDir("/tmp")
			var latestFile string
			var latestTime time.Time

			for _, f := range files {
				if strings.HasPrefix(f.Name(), "kiro-request-") && strings.HasSuffix(f.Name(), ".log") {
					info, _ := f.Info()
					if info.ModTime().After(latestTime) {
						latestTime = info.ModTime()
						latestFile = f.Name()
					}
				}
			}

			if latestFile != "" {
				logFile = "./logs/" + latestFile

				// Read existing log
				existingContent, err := os.ReadFile(logFile)
				if err == nil {
					// Append response section
					responseContent := "\n=== OpenAI Chunks Sent to Client ===\n"
					responseContent += fmt.Sprintf("Total chunks: %d\n\n", len(capturedChunks))

					for i, chunk := range capturedChunks {
						chunkJSON, _ := json.MarshalIndent(chunk, "  ", "  ")
						responseContent += fmt.Sprintf("Chunk %d:\n%s\n", i, string(chunkJSON))
					}

					responseContent += "\n=== End of Debug Log ===\n"

					// Write back
					finalContent := string(existingContent)
					finalContent += responseContent

					os.WriteFile(logFile, []byte(finalContent), 0644)
					fmt.Printf("[Kiro DEBUG] Response appended to: %s\n", logFile)
				}
			}
		}
	}()

	return openaiChunkChan, openaiErrChan, nil
}

// wrapError converts errors to ProviderError with proper status codes
func (p *Provider) wrapError(err error) error {
	if err == nil {
		return nil
	}

	// Check if it's already a ProviderError
	if pErr, ok := err.(*provider.ProviderError); ok {
		return pErr
	}

	// Check for HTTP errors
	if strings.Contains(err.Error(), "request failed with status 400") {
		return provider.NewProviderError(400, err.Error(), p.name, nil)
	}
	if strings.Contains(err.Error(), "request failed with status 401") {
		return provider.NewProviderError(401, err.Error(), p.name, nil)
	}
	if strings.Contains(err.Error(), "request failed with status 403") {
		return provider.NewProviderError(403, err.Error(), p.name, nil)
	}
	if strings.Contains(err.Error(), "request failed with status 429") {
		return provider.NewProviderError(429, err.Error(), p.name, nil)
	}
	if strings.Contains(err.Error(), "request failed with status 5") {
		return provider.NewProviderError(500, err.Error(), p.name, nil)
	}

	// Default to 500 error
	return provider.NewProviderError(500, err.Error(), p.name, nil)
}

// parseEventStream parses the AWS EventStream response
func (p *Provider) parseEventStream(reader io.Reader, eventChan chan<- StreamEvent) error {
	bufReader := bufio.NewReader(reader)
	// Track all decoded events for logging
	var decodedEvents []map[string]interface{}

	for {
		// Read total length (4 bytes)
		lenBuf := make([]byte, 4)
		_, err := io.ReadFull(bufReader, lenBuf)
		if err != nil {
			if err == io.EOF {
				break
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
				break
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
				decodedEvents = append(decodedEvents, sseEvent.Content)
			}
			continue
		}

		// Store decoded event for logging
		decodedEvents = append(decodedEvents, event)

		eventChan <- StreamEvent{
			Type:    getEventType(event),
			Content: event,
		}
	}

	// Log all decoded events for streaming
	if len(decodedEvents) > 0 {
		logStreamingDecodedEvents(decodedEvents)
	}

	return nil
}

// logStreamingDecodedEvents logs all decoded EventStream events for streaming to a file
func logStreamingDecodedEvents(events []map[string]interface{}) {
	timestamp := time.Now().Format("20060102-150405.000")
	logFile := fmt.Sprintf("./logs/kiro-streaming-events-%s.log", timestamp)
	
	var logContent strings.Builder
	logContent.WriteString(fmt.Sprintf("=== Kiro Streaming Decoded EventStream Events at %s ===\n\n", timestamp))
	logContent.WriteString(fmt.Sprintf("Total Events: %d\n\n", len(events)))
	
	for i, event := range events {
		eventType := getEventType(event)
		eventJSON, _ := json.MarshalIndent(event, "", "  ")
		logContent.WriteString(fmt.Sprintf("Event %d (Type: %s):\n%s\n\n", i, eventType, string(eventJSON)))
	}
	
	logContent.WriteString("=== End of Streaming Decoded Events ===\n")
	
	if err := os.WriteFile(logFile, []byte(logContent.String()), 0644); err != nil {
		fmt.Printf("[Kiro DEBUG] Failed to write streaming decoded events log: %v\n", err)
	} else {
		fmt.Printf("[Kiro DEBUG] Streaming decoded events logged to: %s\n", logFile)
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
	if _, ok := event["input"].(string); ok {
		return "tool_use_input"
	}
	if _, ok := event["stop"]; ok {
		return "tool_use_stop"
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

	// Build conversation history and current message
	history, currentMessages, toolResults := buildConversationHistory(r.Messages, r.Model)

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

	// Handle boundary case: content cannot be empty when there are tool results
	if currentMessages == "" && len(toolResults) > 0 {
		currentMessages = "Tool results provided."
	} else if currentMessages == "" {
		currentMessages = "Continue"
	}

	// Build context - only include if has content
	userInputMessageContext := &UserInputMessageContext{}
	if len(toolResults) > 0 {
		userInputMessageContext.ToolResults = toolResults
	}
	if len(kiroTools) > 0 {
		userInputMessageContext.Tools = kiroTools
	}

	// Only set context if it has content
	hasContext := len(toolResults) > 0 || len(kiroTools) > 0

	// Build the request
	conversationState := ConversationState{
		ChatTriggerType: "MANUAL",
		ConversationID:  conversationID,
		CurrentMessage: CurrentMessage{
			UserInputMessage: UserInputMessage{
				Content: currentMessages,
				ModelID: kiroModel,
				Origin:  "AI_EDITOR",
			},
		},
	}

	if hasContext {
		conversationState.CurrentMessage.UserInputMessage.UserInputMessageContext = userInputMessageContext
	}

	// Only include history if it's not empty
	if len(history) > 0 {
		conversationState.History = history
	}

	return &KiroRequest{
		ConversationState: conversationState,
		ProfileArn:        getProfileArn(r.Model),
	}
}

// buildConversationHistory builds the conversation history from messages
// Kiro API requires history to be separate items, not paired user+assistant
// Each message becomes its own history item
func buildConversationHistory(msgs []ChatMessage, model string) ([]HistoryItem, string, []ToolResult) {
	kiroModel := getKiroModel(model)

	// Intermediate struct to hold merged messages
	type processedMsg struct {
		Role        string
		Content     string
		ToolResults []ToolResult
		ToolUses    []ToolUse
	}

	var mergedMsgs []processedMsg

	// 1. Preprocess: Convert and Merge Messages
	for _, msg := range msgs {
		role := msg.Role
		content := ""
		var toolResults []ToolResult
		var toolUses []ToolUse

		switch role {
		case "system":
			// Extract system content
			if str, ok := msg.Content.(string); ok {
				content = str
			}
		case "user":
			content = extractUserContent(msg)
			toolResults = extractToolResultsFromMessage(msg)
		case "assistant":
			content, toolUses = extractAssistantContent(msg)
		case "tool":
			// Should be handled by convertOpenAIMessage, but for safety:
			// Treat as user message with potential tool results?
			// Assuming convertOpenAIMessage already converted "tool" messages to "user" role
			// with tool_result content. If we see "tool" here, it might be raw text?
			if str, ok := msg.Content.(string); ok {
				content = str
			}
			role = "user"
		}

		if len(mergedMsgs) > 0 {
			last := &mergedMsgs[len(mergedMsgs)-1]
			if last.Role == role {
				// Merge content
				if content != "" {
					if last.Content != "" {
						last.Content += "\n\n" + content
					} else {
						last.Content = content
					}
				}
				// Merge lists
				if len(toolResults) > 0 {
					last.ToolResults = append(last.ToolResults, toolResults...)
				}
				if len(toolUses) > 0 {
					last.ToolUses = append(last.ToolUses, toolUses...)
				}
				continue
			}
		}

		mergedMsgs = append(mergedMsgs, processedMsg{
			Role:        role,
			Content:     content,
			ToolResults: toolResults,
			ToolUses:    toolUses,
		})
	}

	// 2. Handle System Prompt
	// Extract system prompt from "system" role messages and remove them from the list
	var systemPrompt string
	var chatMsgs []processedMsg

	for _, pm := range mergedMsgs {
		if pm.Role == "system" {
			if systemPrompt != "" {
				systemPrompt += "\n\n" + pm.Content
			} else {
				systemPrompt = pm.Content
			}
		} else {
			chatMsgs = append(chatMsgs, pm)
		}
	}

	// Prepend System Prompt to the first User message, or insert new one
	if systemPrompt != "" {
		if len(chatMsgs) > 0 && chatMsgs[0].Role == "user" {
			if chatMsgs[0].Content != "" {
				chatMsgs[0].Content = systemPrompt + "\n\n" + chatMsgs[0].Content
			} else {
				chatMsgs[0].Content = systemPrompt
			}
		} else {
			// Insert new user message at the beginning
			newMsg := processedMsg{
				Role:    "user",
				Content: systemPrompt,
			}
			chatMsgs = append([]processedMsg{newMsg}, chatMsgs...)
		}
	}

	// 3. Construct History
	var history []HistoryItem
	var currentMessages string
	var currentToolResults []ToolResult

	if len(chatMsgs) == 0 {
		return nil, "Continue", nil
	}

	// Process all but the last message into History
	for i := 0; i < len(chatMsgs)-1; i++ {
		pm := chatMsgs[i]
		if pm.Role == "user" {
			// Ensure content is not empty for history items
			finalContent := pm.Content
			if finalContent == "" {
				if len(pm.ToolResults) > 0 {
					finalContent = "Tool results provided."
				} else {
					finalContent = "Continue"
				}
			}

			item := HistoryItem{
				UserInputMessage: &UserInputMessage{
					Content: finalContent,
					ModelID: kiroModel,
					Origin:  "AI_EDITOR",
				},
			}
			if len(pm.ToolResults) > 0 {
				// Deduplicate tool results
				seen := make(map[string]bool)
				var uniqueRes []ToolResult
				for _, tr := range pm.ToolResults {
					if !seen[tr.ToolUseID] {
						seen[tr.ToolUseID] = true
						uniqueRes = append(uniqueRes, tr)
					}
				}
				item.UserInputMessage.UserInputMessageContext = &UserInputMessageContext{
					ToolResults: uniqueRes,
				}
			}
			history = append(history, item)

		} else if pm.Role == "assistant" {
			item := HistoryItem{
				AssistantResponseMessage: &AssistantResponseMessage{
					Content: pm.Content,
				},
			}
			if len(pm.ToolUses) > 0 {
				item.AssistantResponseMessage.ToolUses = pm.ToolUses
			}
			history = append(history, item)
		}
	}

	// 4. Handle Current Message (Last Message)
	last := chatMsgs[len(chatMsgs)-1]

	if last.Role == "assistant" {
		// Move assistant to history
		item := HistoryItem{
			AssistantResponseMessage: &AssistantResponseMessage{
				Content: last.Content,
			},
		}
		if len(last.ToolUses) > 0 {
			item.AssistantResponseMessage.ToolUses = last.ToolUses
		}
		history = append(history, item)

		// Set current message to dummy "Continue"
		currentMessages = "Continue"
	} else {
		// Last is User
		// Ensure history ends with assistant
		if len(history) > 0 {
			lastItem := history[len(history)-1]
			if lastItem.AssistantResponseMessage == nil {
				// Append empty assistant response
				history = append(history, HistoryItem{
					AssistantResponseMessage: &AssistantResponseMessage{
						Content: "Continue",
					},
				})
			}
		}

		currentMessages = last.Content
		// Deduplicate tool results for current message
		if len(last.ToolResults) > 0 {
			seen := make(map[string]bool)
			for _, tr := range last.ToolResults {
				if !seen[tr.ToolUseID] {
					seen[tr.ToolUseID] = true
					currentToolResults = append(currentToolResults, tr)
				}
			}
		}

		if currentMessages == "" {
			if len(currentToolResults) > 0 {
				currentMessages = "Tool results provided."
			} else {
				currentMessages = "Continue"
			}
		}
	}

	return history, currentMessages, currentToolResults
}

// extractUserContent extracts text content from a user message
func extractUserContent(msg ChatMessage) string {
	switch c := msg.Content.(type) {
	case string:
		return c
	case []interface{}:
		var parts []string
		for _, part := range c {
			if m, ok := part.(map[string]interface{}); ok {
				if t, ok := m["type"].(string); ok && t == "text" {
					if text, ok := m["text"].(string); ok {
						parts = append(parts, text)
					}
				}
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "\n\n")
		}
	}
	return ""
}

// extractToolResultsFromMessage extracts tool results from a user message
func extractToolResultsFromMessage(msg ChatMessage) []ToolResult {
	var results []ToolResult

	switch c := msg.Content.(type) {
	case []interface{}:
		for _, part := range c {
			if m, ok := part.(map[string]interface{}); ok {
				if t, ok := m["type"].(string); ok && t == "tool_result" {
					toolUseID, _ := m["tool_use_id"].(string)

					// Handle content - can be string or other format
					var contentStr string
					switch contentVal := m["content"].(type) {
					case string:
						contentStr = contentVal
					case []interface{}:
						// Extract text from array content
						for _, arrPart := range contentVal {
							if arrMap, ok := arrPart.(map[string]interface{}); ok {
								if arrType, ok := arrMap["type"].(string); ok && arrType == "text" {
									if arrText, ok := arrMap["text"].(string); ok {
										contentStr += arrText
									}
								}
							}
						}
					default:
						// For other types (like map), convert to JSON string
						if contentVal != nil {
							if jsonBytes, err := json.Marshal(contentVal); err == nil {
								contentStr = string(jsonBytes)
							}
						}
					}

					results = append(results, ToolResult{
						Content: []TextContent{
							{Text: contentStr},
						},
						Status:    "success",
						ToolUseID: toolUseID,
					})
				}
			}
		}
	case string:
		// Simple string content, no tool results
	}

	return results
}

// extractContentFromInterface extracts string content from various types
func extractContentFromInterface(content interface{}) string {
	switch c := content.(type) {
	case string:
		return c
	case []interface{}:
		var result string
		for _, part := range c {
			if m, ok := part.(map[string]interface{}); ok {
				if t, ok := m["type"].(string); ok && t == "text" {
					if text, ok := m["text"].(string); ok {
						result += text
					}
				}
			}
		}
		return result
	}
	return ""
}

// extractAssistantContent extracts content and tool uses from an assistant message
func extractAssistantContent(msg ChatMessage) (string, []ToolUse) {
	var content string
	var toolUses []ToolUse

	// First, extract tool uses from ToolCalls field (OpenAI format)
	for _, tc := range msg.ToolCalls {
		toolUses = append(toolUses, ToolUse{
			Name:      tc.Function.Name,
			ToolUseID: tc.ID,
			Input:     json.RawMessage(tc.Function.Arguments),
		})
	}

	switch c := msg.Content.(type) {
	case string:
		content = c
	case []interface{}:
		for _, part := range c {
			if m, ok := part.(map[string]interface{}); ok {
				if t, ok := m["type"].(string); ok {
					switch t {
					case "text":
						if text, ok := m["text"].(string); ok {
							content += text
						}
					case "tool_use":
						name, _ := m["name"].(string)
						toolUseID, _ := m["id"].(string)
						input, _ := json.Marshal(m["input"])

						toolUses = append(toolUses, ToolUse{
							Name:      name,
							ToolUseID: toolUseID,
							Input:     input,
						})
					}
				}
			}
		}
	}

	return content, toolUses
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
	// Build message with tool calls if present
	message := ChatMessage{
		Role:    "assistant",
		Content: resp.Content,
	}

	// Convert ToolUses to ToolCalls with proper argument formatting
	if len(resp.ToolUses) > 0 {
		toolCalls := make([]ToolCall, len(resp.ToolUses))
		for i, toolUse := range resp.ToolUses {
			// Ensure arguments are properly formatted as JSON strings
			arguments := formatToolArgumentsFromRawMessage(toolUse.Input)

			fmt.Printf("[Kiro DEBUG] Converting ToolUse to ToolCall - ID: %s, Name: %s, Args: %s\n",
				toolUse.ToolUseID, toolUse.Name, arguments)

			toolCalls[i] = ToolCall{
				ID:   toolUse.ToolUseID,
				Type: "function",
				Function: ToolCallFunction{
					Name:      toolUse.Name,
					Arguments: arguments,
				},
			}
		}
		message.ToolCalls = toolCalls
	}

	choices := []Choice{
		{
			Index:        0,
			Message:      message,
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

// formatToolArgumentsFromRawMessage formats tool arguments from json.RawMessage to proper JSON string
func formatToolArgumentsFromRawMessage(input json.RawMessage) string {
	if len(input) == 0 {
		return "{}"
	}

	// Validate that it's proper JSON
	var temp interface{}
	if err := json.Unmarshal(input, &temp); err != nil {
		fmt.Printf("[Kiro DEBUG] Invalid JSON in tool arguments RawMessage, using empty object: %v\n", err)
		return "{}"
	}

	// Return as string - input is already valid JSON bytes
	return string(input)
}

// ============ OpenAI Conversion Helpers ============

// convertOpenAIToChatRequest converts an OpenAI ChatCompletionNewParams to internal ChatRequest
func convertOpenAIToChatRequest(req openai.ChatCompletionNewParams) (*ChatRequest, error) {
	// Marshal to JSON and unmarshal to a map for easier access
	reqJSON, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	var reqMap map[string]interface{}
	if err := json.Unmarshal(reqJSON, &reqMap); err != nil {
		return nil, fmt.Errorf("failed to unmarshal request: %w", err)
	}

	chatReq := &ChatRequest{
		Model:  reqMap["model"].(string),
		Stream: false, // Stream is handled separately
	}

	// Convert messages
	if messages, ok := reqMap["messages"].([]interface{}); ok {
		chatReq.Messages = make([]ChatMessage, len(messages))
		for i, msg := range messages {
			converted, err := convertOpenAIMessage(msg)
			if err != nil {
				return nil, fmt.Errorf("failed to convert message at index %d: %w", i, err)
			}
			chatReq.Messages[i] = converted
		}
	}

	// Convert tools
	if tools, ok := reqMap["tools"].([]interface{}); ok && len(tools) > 0 {
		chatReq.Tools = make([]Tool, len(tools))
		for i, tool := range tools {
			converted, err := convertOpenAITool(tool)
			if err != nil {
				return nil, fmt.Errorf("failed to convert tool at index %d: %w", i, err)
			}
			chatReq.Tools[i] = converted
		}
	}

	// Convert optional parameters
	if maxTokens, ok := reqMap["max_tokens"].(float64); ok {
		mt := int(maxTokens)
		chatReq.MaxTokens = &mt
	}
	if temperature, ok := reqMap["temperature"].(float64); ok {
		chatReq.Temperature = &temperature
	}

	return chatReq, nil
}

// convertOpenAIMessage converts an OpenAI message to internal ChatMessage
func convertOpenAIMessage(msg interface{}) (ChatMessage, error) {
	chatMsg := ChatMessage{}

	// Use JSON marshaling to inspect the message type
	msgJSON, err := json.Marshal(msg)
	if err != nil {
		return chatMsg, fmt.Errorf("failed to marshal message: %w", err)
	}

	var msgMap map[string]interface{}
	if err := json.Unmarshal(msgJSON, &msgMap); err != nil {
		return chatMsg, fmt.Errorf("failed to unmarshal message: %w", err)
	}

	role, _ := msgMap["role"].(string)
	chatMsg.Role = role

	switch role {
	case "system":
		chatMsg.Content = msgMap["content"]

	case "user":
		switch content := msgMap["content"].(type) {
		case string:
			chatMsg.Content = content
		case []interface{}:
			// Already in the right format
			chatMsg.Content = content
		default:
			chatMsg.Content = fmt.Sprintf("%v", msgMap["content"])
		}

	case "assistant":
		chatMsg.Content = msgMap["content"]

		// Convert tool calls
		if toolCalls, ok := msgMap["tool_calls"].([]interface{}); ok && len(toolCalls) > 0 {
			chatMsg.ToolCalls = make([]ToolCall, len(toolCalls))
			for i, tc := range toolCalls {
				if tcMap, ok := tc.(map[string]interface{}); ok {
					chatMsg.ToolCalls[i] = ToolCall{
						ID:   tcMap["id"].(string),
						Type: tcMap["type"].(string),
					}
					if fn, ok := tcMap["function"].(map[string]interface{}); ok {
						chatMsg.ToolCalls[i].Function = ToolCallFunction{
							Name:      fn["name"].(string),
							Arguments: fn["arguments"].(string),
						}
					}
				}
			}
		}

	case "tool":
		// OpenAI tool messages need to be converted to Kiro format
		// Kiro expects tool results as user messages with tool_result blocks
		toolCallID, _ := msgMap["tool_call_id"].(string)
		content := msgMap["content"]

		// Create a user message with tool_result content
		chatMsg.Role = "user"
		chatMsg.Content = []interface{}{
			map[string]interface{}{
				"type":        "tool_result",
				"tool_use_id": toolCallID,
				"content":     content,
			},
		}

	default:
		return chatMsg, fmt.Errorf("unsupported message role: %s", role)
	}

	return chatMsg, nil
}

// convertOpenAITool converts an OpenAI tool to internal Tool
func convertOpenAITool(tool interface{}) (Tool, error) {
	toolJSON, err := json.Marshal(tool)
	if err != nil {
		return Tool{}, fmt.Errorf("failed to marshal tool: %w", err)
	}

	var toolMap map[string]interface{}
	if err := json.Unmarshal(toolJSON, &toolMap); err != nil {
		return Tool{}, fmt.Errorf("failed to unmarshal tool: %w", err)
	}

	return Tool{
		Type: toolMap["type"].(string),
		Function: ToolFunctionSpec{
			Name:        toolMap["function"].(map[string]interface{})["name"].(string),
			Description: toolMap["function"].(map[string]interface{})["description"].(string),
			Parameters:  toolMap["function"].(map[string]interface{})["parameters"].(map[string]interface{}),
		},
	}, nil
}

// convertChatResponseToOpenAI converts internal ChatResponse to OpenAI format
func convertChatResponseToOpenAI(resp *ChatResponse) (*openai.ChatCompletion, error) {
	// Build response as map for JSON marshaling
	respMap := map[string]interface{}{
		"id":      resp.ID,
		"object":  resp.Object,
		"created": resp.Created,
		"model":   resp.Model,
		"choices": make([]map[string]interface{}, len(resp.Choices)),
	}

	for i, choice := range resp.Choices {
		choiceMap := map[string]interface{}{
			"index":         choice.Index,
			"finish_reason": choice.FinishReason,
			"message": map[string]interface{}{
				"role":    "assistant",
				"content": choice.Message.Content,
			},
		}

		// Convert tool calls
		if len(choice.Message.ToolCalls) > 0 {
			toolCalls := make([]map[string]interface{}, len(choice.Message.ToolCalls))
			for j, tc := range choice.Message.ToolCalls {
				toolCalls[j] = map[string]interface{}{
					"id":   tc.ID,
					"type": tc.Type,
					"function": map[string]interface{}{
						"name":      tc.Function.Name,
						"arguments": tc.Function.Arguments,
					},
				}
			}
			choiceMap["message"].(map[string]interface{})["tool_calls"] = toolCalls
		}

		respMap["choices"].([]map[string]interface{})[i] = choiceMap
	}

	// Convert usage
	if resp.Usage != nil {
		respMap["usage"] = map[string]interface{}{
			"prompt_tokens":     resp.Usage.PromptTokens,
			"completion_tokens": resp.Usage.CompletionTokens,
			"total_tokens":      resp.Usage.TotalTokens,
		}
	}

	// Marshal to JSON and unmarshal to openai.ChatCompletion
	respJSON, err := json.Marshal(respMap)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal response: %w", err)
	}

	var openaiResp openai.ChatCompletion
	if err := json.Unmarshal(respJSON, &openaiResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return &openaiResp, nil
}

// kiroStreamState maintains state for streaming response conversion
type kiroStreamState struct {
	roleSent        bool
	sentToolCallIDs map[string]bool
	toolCallIndexes map[string]int
	currentToolID   string
	nextToolIndex   int
}

func newKiroStreamState() *kiroStreamState {
	return &kiroStreamState{
		sentToolCallIDs: make(map[string]bool),
		toolCallIndexes: make(map[string]int),
	}
}

// convertStreamEventToOpenAIChunk converts a StreamEvent to OpenAI ChatCompletionChunk
func convertStreamEventToOpenAIChunk(event StreamEvent, state *kiroStreamState) (openai.ChatCompletionChunk, bool, error) {
	shouldSend := false

	// Build chunk as map for JSON marshaling
	chunkMap := map[string]interface{}{
		"id":      generateUUID(),
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   "claude-sonnet-4-5",
		"choices": []map[string]interface{}{
			{
				"index": 0,
				"delta": map[string]interface{}{},
			},
		},
	}

	// Helper to ensure role is sent once
	ensureRole := func() {
		if !state.roleSent {
			chunkMap["choices"].([]map[string]interface{})[0]["delta"].(map[string]interface{})["role"] = "assistant"
			state.roleSent = true
		}
	}

	switch event.Type {
	case "content":
		if content, ok := event.Content["content"].(string); ok {
			ensureRole()
			chunkMap["choices"].([]map[string]interface{})[0]["delta"].(map[string]interface{})["content"] = content
			shouldSend = true
		}

	case "tool_use":
		// Tool Use Start (contains name, id, and optionally initial input)
		if name, ok := event.Content["name"].(string); ok {
			if toolUseID, ok := event.Content["toolUseId"].(string); ok {
				ensureRole()
				state.currentToolID = toolUseID

				// Assign index
				if _, exists := state.toolCallIndexes[toolUseID]; !exists {
					state.toolCallIndexes[toolUseID] = state.nextToolIndex
					state.nextToolIndex++
				}
				idx := state.toolCallIndexes[toolUseID]

				toolCall := map[string]interface{}{
					"index": idx,
				}

				// If not sent header yet, send id, type, name
				if !state.sentToolCallIDs[toolUseID] {
					toolCall["id"] = toolUseID
					toolCall["type"] = "function"
					toolCall["function"] = map[string]interface{}{
						"name": name,
					}
					state.sentToolCallIDs[toolUseID] = true
				} else {
					// Prepare function object for arguments
					toolCall["function"] = map[string]interface{}{}
				}

				// Handle initial input if present - only include arguments when there's content
				if input, ok := event.Content["input"].(string); ok && input != "" {
					toolCall["function"].(map[string]interface{})["arguments"] = input
				}
				// Note: Do NOT include empty arguments field - per reference spec

				chunkMap["choices"].([]map[string]interface{})[0]["delta"].(map[string]interface{})["tool_calls"] = []map[string]interface{}{toolCall}
				shouldSend = true
			}
		}

	case "tool_use_input":
		// Tool Use Input Delta (only input)
		if state.currentToolID != "" {
			if input, ok := event.Content["input"].(string); ok && input != "" {
				ensureRole()
				idx, exists := state.toolCallIndexes[state.currentToolID]
				if exists {
					toolCall := map[string]interface{}{
						"index": idx,
						"function": map[string]interface{}{
							"arguments": input,
						},
					}
					chunkMap["choices"].([]map[string]interface{})[0]["delta"].(map[string]interface{})["tool_calls"] = []map[string]interface{}{toolCall}
					shouldSend = true
				}
			}
		}

	case "tool_use_stop":
		// Stop event - clear current tool ID
		state.currentToolID = ""
		// Don't send a chunk for this, just update state
		shouldSend = false

	case "usage":
		if usage, ok := event.Content["usage"].(float64); ok {
			chunkMap["usage"] = map[string]interface{}{
				"total_tokens": usage, // Keep as float64
			}
			shouldSend = true
		}
	}

	// Marshal to JSON and unmarshal to openai.ChatCompletionChunk
	chunkJSON, err := json.Marshal(chunkMap)
	if err != nil {
		return openai.ChatCompletionChunk{}, false, fmt.Errorf("failed to marshal chunk: %w", err)
	}

	var openaiChunk openai.ChatCompletionChunk
	if err := json.Unmarshal(chunkJSON, &openaiChunk); err != nil {
		return openai.ChatCompletionChunk{}, false, fmt.Errorf("failed to unmarshal chunk: %w", err)
	}

	return openaiChunk, shouldSend, nil
}
