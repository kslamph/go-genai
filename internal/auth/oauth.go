package auth

import (
	"context"

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