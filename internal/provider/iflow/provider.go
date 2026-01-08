package iflow

import (
	"context"
	"net/http"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/sunbankio/omniproxy/internal/provider"
	"github.com/sunbankio/omniproxy/internal/provider/common"
)

const BaseURL = "https://apis.iflow.cn/v1"

type Provider struct {
	client openai.Client
	name   string
}

// Ensure Provider implements provider.OpenAICompatibleProvider
var _ provider.OpenAICompatibleProvider = (*Provider)(nil)

func NewProvider(name string, auth *Authenticator) *Provider {
	opts := []option.RequestOption{
		option.WithAPIKey(""), // Token injected via transport
		option.WithBaseURL(BaseURL),
		option.WithHTTPClient(&http.Client{
			Transport: &common.TokenTransport{
				TokenGetter: auth,
				UserAgent:   "iFlow-Cli",
			},
		}),
	}
	client := openai.NewClient(opts...)
	return &Provider{
		client: client,
		name:   name,
	}
}

func (p *Provider) Type() provider.ProviderType {
	return provider.ProviderIFlow
}

func (p *Provider) Name() string {
	return p.name
}

func (p *Provider) SupportedProtocols() []provider.Protocol {
	return []provider.Protocol{provider.ProtocolOpenAI}
}

func (p *Provider) ChatCompletion(ctx context.Context, req openai.ChatCompletionNewParams) (interface{}, error) {
	resp, err := p.client.Chat.Completions.New(ctx, req)
	if err != nil {
		return nil, p.wrapError(err)
	}
	return resp, nil
}

func (p *Provider) StreamChatCompletion(ctx context.Context, req openai.ChatCompletionNewParams) (<-chan openai.ChatCompletionChunk, <-chan error, error) {
	stream := p.client.Chat.Completions.NewStreaming(ctx, req)

	respChan := make(chan openai.ChatCompletionChunk)
	errChan := make(chan error, 1)

	go func() {
		defer close(respChan)
		defer close(errChan)

		for stream.Next() {
			select {
			case <-ctx.Done():
				return
			default:
				respChan <- stream.Current()
			}
		}

		if err := stream.Err(); err != nil && err.Error() != "EOF" {
			errChan <- err
		}
	}()

	return respChan, errChan, nil
}

// wrapError converts openai-go errors to ProviderError with proper status codes
func (p *Provider) wrapError(err error) error {
	// Check if it's an APIError from openai-go
	if apiErr, ok := err.(*openai.Error); ok {
		return provider.NewProviderError(int(apiErr.StatusCode), apiErr.Message, p.name, map[string]interface{}{
			"type": apiErr.Type,
			"code": apiErr.Code,
		})
	}

	// Fallback to generic 500 error
	return provider.NewProviderError(http.StatusInternalServerError, err.Error(), p.name, nil)
}

func (p *Provider) ListModels(ctx context.Context) ([]string, error) {
	return []string{
		"iflow-rome-30ba3b",
		"qwen3-coder-plus",
		"qwen3-max",
		"qwen3-vl-plus",
		"qwen3-max-preview",
		"qwen3-32b",
		"qwen3-235b-a22b-thinking-2507",
		"qwen3-235b-a22b-instruct",
		"qwen3-235b",
		"kimi-k2-0905",
		"kimi-k2",
		"glm-4.6",
		"glm-4.7",
		"minimax-m2.1",
		"deepseek-v3.2",
		"deepseek-r1",
		"deepseek-v3",
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
