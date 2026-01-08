package auth

import (
	"context"
	"sync"
	"time"

	"github.com/sunbankio/omniproxy/internal/provider"
	"github.com/sunbankio/omniproxy/pkg/utils"
)

// ProviderType represents the type of AI provider
type ProviderType int

const (
	ProviderTypeOpenAI ProviderType = iota
	ProviderTypeGemini
	ProviderTypeAntigravity
	ProviderTypeQwen
	ProviderTypeIFlow
	ProviderTypeKiro
)

// String returns the string representation of ProviderType
func (p ProviderType) String() string {
	switch p {
	case ProviderTypeOpenAI:
		return "OpenAI"
	case ProviderTypeGemini:
		return "Gemini"
	case ProviderTypeAntigravity:
		return "Antigravity"
	case ProviderTypeQwen:
		return "Qwen"
	case ProviderTypeIFlow:
		return "IFlow"
	case ProviderTypeKiro:
		return "Kiro"
	default:
		return "Unknown"
	}
}

// CredentialState represents the state of a credential
type CredentialState int

const (
	CredentialStateActive CredentialState = iota
	CredentialStateDead
)

// String returns the string representation of CredentialState
func (s CredentialState) String() string {
	switch s {
	case CredentialStateActive:
		return "Active"
	case CredentialStateDead:
		return "Dead"
	default:
		return "Unknown"
	}
}

// Credential represents authentication credentials for a provider
type Credential struct {
	ID           string       `json:"id"`
	ProviderType ProviderType `json:"provider_type"`

	// Authentication data
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	Expiry       time.Time `json:"expiry"`
	ProjectID    string    `json:"project_id,omitempty"`

	// State data
	State        CredentialState `json:"state"`
	FailureCount int             `json:"failure_count"`
	AvailableAt  time.Time       `json:"rate_limit_reset_time,omitempty"` // When rate limit will reset (from 429 error)
	LastUsedAt   time.Time       `json:"last_used_at,omitempty"`          // Last time this credential was used (for round-robin)

	// Client pool for this credential
	clientPool *ClientPool `json:"-"`

	// Provider instance for request execution
	// This is the actual provider object that can execute requests
	provider provider.BaseProvider `json:"-"`

	// Authenticator instance for token refresh operations
	// This stores the provider-specific authenticator with the correct credential path
	authenticator interface{} `json:"-"`

	// Mutex for thread-safe access
	mu sync.RWMutex `json:"-"`
}

// Name returns the unique name/ID of this credential
// This method provides compatibility with the BaseProvider interface
func (c *Credential) Name() string {
	return c.ID
}

// Type returns the provider type for this credential
// This method provides compatibility with the BaseProvider interface
func (c *Credential) Type() provider.ProviderType {
	// Convert auth.ProviderType to provider.ProviderType
	switch c.ProviderType {
	case ProviderTypeOpenAI:
		return provider.ProviderOpenAI
	case ProviderTypeGemini:
		return provider.ProviderGemini
	case ProviderTypeAntigravity:
		return provider.ProviderAntigravity
	case ProviderTypeQwen:
		return provider.ProviderQwen
	case ProviderTypeIFlow:
		return provider.ProviderIFlow
	case ProviderTypeKiro:
		return provider.ProviderKiro
	default:
		return provider.ProviderOpenAI // fallback
	}
}

// NewCredential creates a new Credential instance
func NewCredential(id string, providerType ProviderType) *Credential {
	return &Credential{
		ID:           id,
		ProviderType: providerType,
		State:        CredentialStateActive,
		clientPool:   NewClientPool(),
	}
}

// SupportedProtocols returns the list of protocols this credential supports
// This method provides compatibility with the BaseProvider interface
func (c *Credential) SupportedProtocols() []provider.Protocol {
	switch c.ProviderType {
	case ProviderTypeOpenAI, ProviderTypeQwen, ProviderTypeIFlow, ProviderTypeKiro:
		return []provider.Protocol{provider.ProtocolOpenAI}
	case ProviderTypeGemini, ProviderTypeAntigravity:
		return []provider.Protocol{provider.ProtocolGemini}
	default:
		return nil
	}
}

// ListModels returns a list of models supported by the provider
// Delegates to the provider's ListModels method if a provider instance is available
func (c *Credential) ListModels(ctx context.Context) ([]string, error) {
	// If we have a provider instance, delegate to it
	if provider := c.GetProvider(); provider != nil {
		utils.L().Infow("Credential.ListModels delegating to provider",
			"credential_id", c.ID,
			"provider_type", c.ProviderType,
			"provider_name", provider.Name())
		models, err := provider.ListModels(ctx)
		utils.L().Infow("Provider.ListModels result",
			"credential_id", c.ID,
			"models_count", len(models),
			"error", err)
		return models, err
	}

	// If no provider is available, return empty list
	// This could happen if the credential hasn't been fully initialized
	utils.L().Infow("Credential.ListModels no provider available",
		"credential_id", c.ID,
		"provider_type", c.ProviderType)
	return []string{}, nil
}

// SupportsModel checks if the credential supports the given model
// Delegates to the provider's SupportsModel method if a provider instance is available
func (c *Credential) SupportsModel(model string) bool {
	// If we have a provider instance, delegate to it
	if provider := c.GetProvider(); provider != nil {
		return provider.SupportsModel(model)
	}

	// In V2, providers should always be set. If provider is nil, return false.
	utils.L().Warnw("SupportsModel called but no provider available",
		"credential_id", c.ID,
		"provider_type", c.ProviderType,
		"model", model)
	return false
}

// GetClient returns a client from the credential's client pool
// This provides access to the underlying client for protocol-specific operations
func (c *Credential) GetClient() interface{} {
	if c.clientPool == nil {
		return nil
	}
	client := c.clientPool.GetClient()
	// Fix for "nil interface" gotcha:
	// If the typed pointer is nil, return explicit nil interface
	if client == nil {
		return nil
	}
	return client
}

// Cleanup removes old clients from the credential's client pool
// This delegates to the underlying ClientPool's Cleanup method
func (c *Credential) Cleanup() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.clientPool != nil {
		c.clientPool.Cleanup()
	}
}

// GetProvider returns the provider instance for this credential
func (c *Credential) GetProvider() provider.BaseProvider {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.provider
}

// SetProvider sets the provider instance for this credential
func (c *Credential) SetProvider(p provider.BaseProvider) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.provider = p
}

// GetAuthenticator returns the authenticator instance for this credential
func (c *Credential) GetAuthenticator() interface{} {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.authenticator
}

// SetAuthenticator sets the authenticator instance for this credential
func (c *Credential) SetAuthenticator(auth interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.authenticator = auth
}

// ClientPool represents a pool of clients for a credential
// This is defined in client_pool.go

// AuthManager interface defines authentication management operations
type AuthManager interface {
	// ForceRefresh forces a refresh of the credential's token and client
	ForceRefresh(ctx context.Context, cred *Credential) error

	// EnsureValidToken ensures the credential has a valid token, refreshing if necessary
	EnsureValidToken(ctx context.Context, cred *Credential) error

	// RefreshClient refreshes the client associated with the credential
	RefreshClient(ctx context.Context, cred *Credential) error

	// Validate checks if the credential is valid and usable
	Validate(cred *Credential) error
}
