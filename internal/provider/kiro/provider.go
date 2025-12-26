package kiro

import (
	"context"
	"fmt"

	"github.com/sashabaranov/go-openai"
)

type Provider struct {
	auth   *Authenticator
	name   string
	region string
}

// NewProvider creates a new Kiro provider with auth
// Note: This provider is NOT available for OpenAI-compatible API requests.
// It's kept for future native protocol implementation.
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

// GetAuth returns the authenticator for native protocol access
func (p *Provider) GetAuth() *Authenticator {
	return p.auth
}

// GetRegion returns the AWS region for this provider
func (p *Provider) GetRegion() string {
	return p.region
}

// ListModels returns a list of models supported by the provider
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

// ChatCompletion is not supported for OpenAI-compatible API
// This provider is kept for future native protocol implementation
func (p *Provider) ChatCompletion(ctx context.Context, req openai.ChatCompletionRequest) (interface{}, error) {
	return nil, fmt.Errorf("provider 'kiro' does not support OpenAI-compatible API. Use native protocol access instead")
}

// StreamChatCompletion is not supported for OpenAI-compatible API
// This provider is kept for future native protocol implementation
func (p *Provider) StreamChatCompletion(ctx context.Context, req openai.ChatCompletionRequest) (<-chan openai.ChatCompletionStreamResponse, <-chan error) {
	errChan := make(chan error, 1)
	errChan <- fmt.Errorf("provider 'kiro' does not support OpenAI-compatible API. Use native protocol access instead")
	close(errChan)
	return make(chan openai.ChatCompletionStreamResponse), errChan
}