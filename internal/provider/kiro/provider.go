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

		token, err := p.auth.GetToken(ctx)
		if err != nil {
			errChan <- err
			return
		}

		kiroReq, err := ToKiroRequest(req, getProfileArn(p.auth))
		if err != nil {
			errChan <- err
			return
		}

		reqBody, err := json.Marshal(kiroReq)
		if err != nil {
			errChan <- err
			return
		}

		url := fmt.Sprintf("https://codewhisperer.%s.amazonaws.com/SendMessageStreaming", p.region)
		httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(reqBody))
		if err != nil {
			errChan <- err
			return
		}

		setHeaders(httpReq, token, true)

		client := &http.Client{Timeout: 120 * time.Second} // Longer timeout for streaming
		resp, err := client.Do(httpReq)
		if err != nil {
			errChan <- err
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			errChan <- fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
			return
		}

		// Stream parsing logic
		reader := bufio.NewReader(resp.Body)
		for {
			// Read total length (4 bytes)
			lenBuf := make([]byte, 4)
			_, err := io.ReadFull(reader, lenBuf)
			if err != nil {
				if err == io.EOF {
					return
				}
				errChan <- err
				return
			}
			totalLen := binary.BigEndian.Uint32(lenBuf)

			// Read header length (4 bytes)
			_, err = io.ReadFull(reader, lenBuf)
			if err != nil {
				errChan <- err
				return
			}
			headerLen := binary.BigEndian.Uint32(lenBuf)

			// Skip prelude CRC (4 bytes)
			reader.Discard(4)

			// Skip headers
			reader.Discard(int(headerLen))

			// Read payload
			payloadLen := int(totalLen) - 16 - int(headerLen)
			payload := make([]byte, payloadLen)
			_, err = io.ReadFull(reader, payload)
			if err != nil {
				errChan <- err
				return
			}

			// Skip message CRC (4 bytes)
			reader.Discard(4)

			// Parse payload
			var event map[string]interface{}
			if err := json.Unmarshal(payload, &event); err == nil {
				// Extract contentDelta
				if delta, ok := event["contentDelta"].(string); ok && delta != "" {
					respChan <- openai.ChatCompletionStreamResponse{
						ID:      "chatcmpl-kiro-stream",
						Object:  "chat.completion.chunk",
						Created: time.Now().Unix(),
						Model:   req.Model,
						Choices: []openai.ChatCompletionStreamChoice{
							{
								Delta: openai.ChatCompletionStreamChoiceDelta{
									Content: delta,
								},
							},
						},
					}
				}
				// Handle invalidStateEvent or others?
			}
		}
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
	offset := 0
	fullContent := ""

	for offset < len(data) {
		if offset+12 > len(data) {
			break
		}
		totalLen := binary.BigEndian.Uint32(data[offset : offset+4])
		headerLen := binary.BigEndian.Uint32(data[offset+4 : offset+8])

		payloadStart := offset + 12 + int(headerLen)
		payloadEnd := offset + int(totalLen) - 4

		if payloadStart < payloadEnd && payloadEnd <= len(data) {
			payload := data[payloadStart:payloadEnd]
			var event map[string]interface{}
			if err := json.Unmarshal(payload, &event); err == nil {
				// Check different event types
				if content, ok := event["content"].(string); ok {
					// For non-streaming generateAssistantResponse, it usually returns one event with 'content'
					fullContent += content
				} else if delta, ok := event["contentDelta"].(string); ok {
					fullContent += delta
				} else if message, ok := event["assistantResponseMessage"].(map[string]interface{}); ok {
					if c, ok := message["content"].(string); ok {
						fullContent += c
					}
				}
			}
		}
		offset += int(totalLen)
	}
	return fullContent, nil
}
