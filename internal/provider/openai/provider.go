package openai

import (
	"context"
	"net/http"

	"github.com/sashabaranov/go-openai"
	"github.com/sunbankio/omniproxy/internal/provider"
)

type Provider struct {
	client *openai.Client
	name   string
}

// Ensure Provider implements provider.OpenAICompatibleProvider
var _ provider.OpenAICompatibleProvider = (*Provider)(nil)

func NewProvider(name string, apiKey string, baseURL string) *Provider {
	config := openai.DefaultConfig(apiKey)
	if baseURL != "" {
		config.BaseURL = baseURL
	}
	client := openai.NewClientWithConfig(config)
	return &Provider{
		client: client,
		name:   name,
	}
}

func (p *Provider) Type() string {
	return "openai"
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

func (p *Provider) StreamChatCompletion(ctx context.Context, req openai.ChatCompletionRequest) (<-chan openai.ChatCompletionStreamResponse, <-chan error) {
	respChan := make(chan openai.ChatCompletionStreamResponse)
	errChan := make(chan error, 1)

	go func() {
		defer close(respChan)
		defer close(errChan)

		stream, err := p.client.CreateChatCompletionStream(ctx, req)
		if err != nil {
			errChan <- p.wrapError(err)
			return
		}
		defer stream.Close()

		for {
			response, err := stream.Recv()
			if err != nil {
				return
			}
			respChan <- response
		}
	}()

	return respChan, errChan
}

func (p *Provider) ListModels(ctx context.Context) ([]string, error) {
	resp, err := p.client.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	var models []string
	for _, m := range resp.Models {
		models = append(models, m.ID)
	}
	return models, nil
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
