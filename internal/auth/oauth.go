package auth

import (
	"context"
	"time"

	cloudauth "cloud.google.com/go/auth"
)

// TokenProviderAdapter adapts our Credential and AuthManager to cloudauth.TokenProvider
// This is used for providers that need cloud.google.com/go/auth compatibility (Gemini, Antigravity)
type TokenProviderAdapter struct {
	cred   *Credential
	manager AuthManager
}

// NewTokenProviderAdapter creates a new TokenProviderAdapter
func NewTokenProviderAdapter(cred *Credential, manager AuthManager) *TokenProviderAdapter {
	return &TokenProviderAdapter{
		cred:    cred,
		manager: manager,
	}
}

// Token returns a valid cloudauth.Token, refreshing if necessary
func (tpa *TokenProviderAdapter) Token(ctx context.Context) (*cloudauth.Token, error) {
	// Ensure we have a valid token
	if err := tpa.manager.EnsureValidToken(ctx, tpa.cred); err != nil {
		return nil, err
	}

	// Read token data from credential
	tpa.cred.mu.RLock()
	accessToken := tpa.cred.AccessToken
	expiry := tpa.cred.Expiry
	tpa.cred.mu.RUnlock()

	return &cloudauth.Token{
		Value:  accessToken,
		Expiry: expiry,
	}, nil
}

// OAuthTokenProvider is a generic interface for token providers that can be used across different OAuth implementations
type OAuthTokenProvider interface {
	GetToken(ctx context.Context) (string, error)
	ForceRefresh(ctx context.Context) error
	IsAuthenticated() bool
	IsValid() bool
}

// GenericOAuthConfig holds common OAuth configuration
type GenericOAuthConfig struct {
	ClientID     string
	ClientSecret string
	Scope        string
	TokenURL     string
	AuthURL      string
	RedirectPort int
	CredsPath    string
}

// OAuthHelper provides common OAuth utilities
type OAuthHelper struct {
	config *GenericOAuthConfig
}

// NewOAuthHelper creates a new OAuthHelper
func NewOAuthHelper(config *GenericOAuthConfig) *OAuthHelper {
	return &OAuthHelper{
		config: config,
	}
}

// IsTokenExpiringSoon checks if a token is expiring within the specified duration
func IsTokenExpiringSoon(expiry time.Time, buffer time.Duration) bool {
	return time.Until(expiry) < buffer
}

// DefaultTokenRefreshBuffer returns the default 30-minute buffer for token refresh
func DefaultTokenRefreshBuffer() time.Duration {
	return 30 * time.Minute
}