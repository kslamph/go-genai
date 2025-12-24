package qwen

import (
	"context"
	"net/http"

	"github.com/sashabaranov/go-openai"
	"github.com/sunbankio/omniproxy/internal/provider"
	"github.com/sunbankio/omniproxy/internal/provider/common"
)

type Provider struct {
	client *openai.Client
	name   string
}

// Ensure Provider implements provider.Provider
var _ provider.Provider = (*Provider)(nil)

func NewProvider(name string, auth *Authenticator) *Provider {
	config := openai.DefaultConfig("") // No static token, injected via transport
	config.BaseURL = DefaultBaseURL
	config.HTTPClient = &http.Client{
		Transport: &common.TokenTransport{
			TokenGetter: auth,
		},
	}
	client := openai.NewClientWithConfig(config)
	return &Provider{
		client: client,
		name:   name,
	}
}

func (p *Provider) Type() string {
	return "qwen"
}

func (p *Provider) Name() string {
	return p.name
}

func (p *Provider) ChatCompletion(ctx context.Context, req openai.ChatCompletionRequest) (*openai.ChatCompletionResponse, error) {
	resp, err := p.client.CreateChatCompletion(ctx, req)
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

func (p *Provider) StreamChatCompletion(ctx context.Context, req openai.ChatCompletionRequest) (<-chan openai.ChatCompletionStreamResponse, <-chan error) {
	respChan := make(chan openai.ChatCompletionStreamResponse)
	errChan := make(chan error, 1)

	go func() {
		defer close(respChan)
		defer close(errChan)

		stream, err := p.client.CreateChatCompletionStream(ctx, req)
		if err != nil {
			errChan <- err
			return
		}
		defer stream.Close()

		for {
			response, err := stream.Recv()
			if err != nil {
				return // Stream finished or error
			}
			respChan <- response
		}
	}()

	return respChan, errChan
}
