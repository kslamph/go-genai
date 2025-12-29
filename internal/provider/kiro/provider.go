package kiro

import (
	"context"

	"github.com/sunbankio/omniproxy/internal/provider"
)

type Provider struct {
	auth   *Authenticator
	name   string
	region string
}

// NewProvider creates a new Kiro provider with auth
func NewProvider(name string, auth *Authenticator) *Provider {
	return &Provider{
		auth:   auth,
		name:   name,
		region: auth.GetRegion(),
	}
}

func (p *Provider) Type() provider.ProviderType {
	return provider.ProviderType("kiro")
}

func (p *Provider) Name() string {
	return p.name
}

func (p *Provider) SupportedProtocols() []provider.Protocol {
	return []provider.Protocol{provider.Protocol("kiro")}
}

func (p *Provider) GetAuth() interface{} {
	return p.auth
}

func (p *Provider) GetRegion() string {
	return p.region
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