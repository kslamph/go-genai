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
	"time"

	"github.com/sashabaranov/go-openai"
	"github.com/sunbankio/omniproxy/internal/provider"
	"github.com/sunbankio/omniproxy/pkg/utils"
)

type Provider struct {
	auth   *Authenticator
	name   string
	region string
}

// Ensure Provider implements provider.Provider
var _ provider.Provider = (*Provider)(nil)

func NewProvider(name string, auth *Authenticator) *Provider {
	return &Provider{
		auth:   auth,
		name:   name,
		region: auth.GetRegion(),
	}
}

func (p *Provider) Type() string {
	return "kiro"
}

func (p *Provider) Name() string {
	return p.name
}

func (p *Provider) ChatCompletion(ctx context.Context, req openai.ChatCompletionRequest) (*openai.ChatCompletionResponse, error) {
	token, err := p.auth.GetToken(ctx)
	if err != nil {
		return nil, err
	}

	kiroReq, err := ToKiroRequest(req, getProfileArn(p.auth))
	if err != nil {
		return nil, err
	}

	reqBody, err := json.Marshal(kiroReq)
	if err != nil {
		return nil, err
	}

	url := fmt.Sprintf("https://codewhisperer.%s.amazonaws.com/generateAssistantResponse", p.region)
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}

	setHeaders(httpReq, token, false)

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	// Parse EventStream to get content
	content, err := parseEventStream(data)
	if err != nil {
		return nil, err
	}

	return &openai.ChatCompletionResponse{
		ID:      "chatcmpl-kiro",
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   req.Model,
		Choices: []openai.ChatCompletionChoice{
			{
				Message: openai.ChatCompletionMessage{
					Role:    openai.ChatMessageRoleAssistant,
					Content: content,
				},
				FinishReason: openai.FinishReasonStop,
			},
		},
	}, nil
}

func (p *Provider) StreamChatCompletion(ctx context.Context, req openai.ChatCompletionRequest) (<-chan openai.ChatCompletionStreamResponse, <-chan error) {
	respChan := make(chan openai.ChatCompletionStreamResponse)
	errChan := make(chan error, 1)

	go func() {
		defer close(respChan)
		defer close(errChan)

		httpReq, err := p.createStreamRequest(ctx, req)
		if err != nil {
			errChan <- err
			return
		}

		client := &http.Client{Timeout: 120 * time.Second}
		resp, err := client.Do(httpReq)
		if err != nil {
			errChan <- err
			return
		}
		defer resp.Body.Close()

		if err := p.handleStreamResponse(resp); err != nil {
			errChan <- err
			return
		}

		p.processEventStream(resp.Body, req.Model, respChan, errChan)
	}()

	return respChan, errChan
}

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

// StreamChatCompletion helper functions

func (p *Provider) createStreamRequest(ctx context.Context, req openai.ChatCompletionRequest) (*http.Request, error) {
	token, err := p.auth.GetToken(ctx)
	if err != nil {
		return nil, err
	}

	kiroReq, err := ToKiroRequest(req, getProfileArn(p.auth))
	if err != nil {
		return nil, err
	}

	reqBody, err := json.Marshal(kiroReq)
	if err != nil {
		return nil, err
	}

	url := fmt.Sprintf("https://codewhisperer.%s.amazonaws.com/SendMessageStreaming", p.region)
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}

	setHeaders(httpReq, token, true)
	return httpReq, nil
}

func (p *Provider) handleStreamResponse(resp *http.Response) error {
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

func (p *Provider) processEventStream(body io.Reader, model string, respChan chan<- openai.ChatCompletionStreamResponse, errChan chan<- error) {
	reader := bufio.NewReader(body)
	messageCount := 0

	for {
		payload, err := p.readEventStreamMessage(reader)
		if err != nil {
			if err == io.EOF {
				utils.L().Debugf("Kiro stream ended after %d messages", messageCount)
				return
			}
			utils.L().Errorf("Kiro stream error reading message: %v", err)
			errChan <- err
			return
		}

		messageCount++
		utils.L().Debugf("Kiro stream message #%d, payload length: %d", messageCount, len(payload))

		if err := p.handleStreamPayload(payload, model, respChan); err != nil {
			utils.L().Errorf("Kiro stream error handling payload: %v", err)
		}
	}
}

func (p *Provider) readEventStreamMessage(reader *bufio.Reader) ([]byte, error) {
	// Read total length (4 bytes)
	var lenBuf [4]byte
	if _, err := io.ReadFull(reader, lenBuf[:]); err != nil {
		return nil, err
	}
	totalLen := binary.BigEndian.Uint32(lenBuf[:])

	// Read header length (4 bytes)
	if _, err := io.ReadFull(reader, lenBuf[:]); err != nil {
		return nil, err
	}
	headerLen := binary.BigEndian.Uint32(lenBuf[:])

	// Skip prelude CRC (4 bytes)
	if _, err := reader.Discard(4); err != nil {
		return nil, err
	}

	// Skip headers
	if _, err := reader.Discard(int(headerLen)); err != nil {
		return nil, err
	}

	// Read payload
	payloadLen := int(totalLen) - 16 - int(headerLen)
	payload := make([]byte, payloadLen)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return nil, err
	}

	// Skip message CRC (4 bytes)
	if _, err := reader.Discard(4); err != nil {
		return nil, err
	}

	return payload, nil
}

func (p *Provider) handleStreamPayload(payload []byte, model string, respChan chan<- openai.ChatCompletionStreamResponse) error {
	var event map[string]interface{}
	if err := json.Unmarshal(payload, &event); err != nil {
		return fmt.Errorf("failed to parse payload: %v, payload: %s", err, string(payload))
	}

	utils.L().Debugf("Kiro stream event: %+v", event)

	content, hasContent := p.extractContentFromEvent(event)
	if hasContent && content != "" {
		utils.L().Debugf("Kiro stream sending content: %s", content)
		respChan <- p.createStreamResponse(content, model)
	} else {
		utils.L().Debugf("Kiro stream no content found in event")
	}

	return nil
}

func (p *Provider) extractContentFromEvent(event map[string]interface{}) (string, bool) {
	if content, ok := event["content"].(string); ok {
		return content, true
	}
	return "", false
}

func (p *Provider) createStreamResponse(content, model string) openai.ChatCompletionStreamResponse {
	return openai.ChatCompletionStreamResponse{
		ID:      "chatcmpl-kiro-stream",
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []openai.ChatCompletionStreamChoice{
			{
				Delta: openai.ChatCompletionStreamChoiceDelta{
					Content: content,
				},
			},
		},
	}
}

// Helpers

func setHeaders(req *http.Request, token string, isStream bool) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("amz-sdk-request", "attempt=1; max=1")
	req.Header.Set("amz-sdk-invocation-id", fmt.Sprintf("%x", time.Now().UnixNano()))
	req.Header.Set("x-amzn-kiro-agent-mode", "vibe")
	req.Header.Set("user-agent", "aws-sdk-js/1.0.0 ua/2.1 os/linux lang/go api/codewhispererruntime#1.0.0 m/E")
	req.Header.Set("Connection", "close")

	if isStream {
		req.Header.Set("Accept", "application/vnd.amazon.eventstream")
	} else {
		req.Header.Set("Accept", "application/json")
	}
}

func extractEventPayload(data []byte, offset int) ([]byte, int, error) {
	// Check if we have enough data for the header
	if offset+12 > len(data) {
		return nil, 0, fmt.Errorf("insufficient data for event header")
	}

	totalLen := binary.BigEndian.Uint32(data[offset : offset+4])
	headerLen := binary.BigEndian.Uint32(data[offset+4 : offset+8])

	payloadStart := offset + 12 + int(headerLen)
	payloadEnd := offset + int(totalLen) - 4

	// Validate payload bounds
	if payloadStart >= payloadEnd || payloadEnd > len(data) {
		return nil, int(totalLen), fmt.Errorf("invalid payload bounds")
	}

	payload := data[payloadStart:payloadEnd]
	return payload, offset + int(totalLen), nil
}

func extractContentFromPayload(payload []byte) string {
	var event map[string]interface{}
	if err := json.Unmarshal(payload, &event); err != nil {
		return ""
	}

	// Check different event types for content
	if content, ok := event["content"].(string); ok {
		// For non-streaming generateAssistantResponse, it usually returns one event with 'content'
		return content
	}

	if delta, ok := event["contentDelta"].(string); ok {
		return delta
	}

	if message, ok := event["assistantResponseMessage"].(map[string]interface{}); ok {
		if content, ok := message["content"].(string); ok {
			return content
		}
	}

	return ""
}

func getProfileArn(auth *Authenticator) *string {
	if auth.GetAuthMethod() == "social" {
		if arn := auth.GetProfileArn(); arn != "" {
			return &arn
		}
	}
	return nil
}

// parseEventStream parses the binary event stream from Kiro
func parseEventStream(data []byte) (string, error) {
	var fullContent string
	offset := 0

	for offset < len(data) {
		payload, nextOffset, err := extractEventPayload(data, offset)
		if err != nil {
			break
		}

		if content := extractContentFromPayload(payload); content != "" {
			fullContent += content
		}

		offset = nextOffset
	}

	return fullContent, nil
}
