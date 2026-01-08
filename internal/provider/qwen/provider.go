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

// Ensure Provider implements provider.OpenAICompatibleProvider
var _ provider.OpenAICompatibleProvider = (*Provider)(nil)

func NewProvider(name string, auth *Authenticator) *Provider {
	config := openai.DefaultConfig("") // No static token, injected via transport
	config.BaseURL = DefaultBaseURL
	config.HTTPClient = &http.Client{
		Transport: &common.TokenTransport{
			TokenGetter: auth,
			UserAgent:   "QwenCode/0.6.0 (linux; x64)",
		},
	}
	client := openai.NewClientWithConfig(config)
	return &Provider{
		client: client,
		name:   name,
	}
}

func (p *Provider) Type() provider.ProviderType {
	return provider.ProviderQwen
}

func (p *Provider) Name() string {
	return p.name
}

func (p *Provider) SupportedProtocols() []provider.Protocol {
	return []provider.Protocol{provider.ProtocolOpenAI}
}

func (p *Provider) ChatCompletion(ctx context.Context, req openai.ChatCompletionRequest) (interface{}, error) {
	resp, err := p.client.CreateChatCompletion(ctx, req)
	if err != nil {
		return nil, p.wrapError(err)
	}
	return &resp, nil
}

func (p *Provider) StreamChatCompletion(ctx context.Context, req openai.ChatCompletionRequest) (<-chan openai.ChatCompletionStreamResponse, <-chan error, error) {
	stream, err := p.client.CreateChatCompletionStream(ctx, req)
	if err != nil {
		return nil, nil, p.wrapError(err)
	}

	respChan := make(chan openai.ChatCompletionStreamResponse)
	errChan := make(chan error, 1)

	go func() {
		defer close(respChan)
		defer close(errChan)
		defer stream.Close()

		for {
			select {
			case <-ctx.Done():
				// Client disconnected, clean up
				return
			default:
				response, err := stream.Recv()
				if err != nil {
					if err.Error() != "EOF" {
						errChan <- err
					}
					return // Stream finished or error
				}
				respChan <- response
			}
		}
	}()

	return respChan, errChan, nil
}

func (p *Provider) ListModels(ctx context.Context) ([]string, error) {
	return []string{
		"qwen3-coder-plus",
		"qwen3-coder-flash",
	}, nil
}

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

// wrapError converts go-openai errors to ProviderError with proper status codes
func (p *Provider) wrapError(err error) error {
	// Check if it's an APIError from go-openai
	if apiErr, ok := err.(*openai.APIError); ok {
		return provider.NewProviderError(apiErr.HTTPStatusCode, apiErr.Message, p.name, map[string]interface{}{
			"type": apiErr.Type,
			"code": apiErr.Code,
		})
	}

	// Check if it's a RequestError
	if reqErr, ok := err.(*openai.RequestError); ok {
		return provider.NewProviderError(reqErr.HTTPStatusCode, reqErr.Err.Error(), p.name, nil)
	}

	// Fallback to generic 500 error
	return provider.NewProviderError(http.StatusInternalServerError, err.Error(), p.name, nil)
}
