package auth

import (
	"context"

	"github.com/sunbankio/omniproxy/internal/provider/iflow"
)

// IFlowAuthenticator is a wrapper around the internal iflow.Authenticator
// to provide a public interface for external packages
type IFlowAuthenticator struct {
	auth *iflow.Authenticator
}

// NewIFlowAuthenticator creates a new IFlow authenticator
func NewIFlowAuthenticator(config *iflow.OAuthConfig) *IFlowAuthenticator {
	return &IFlowAuthenticator{
		auth: iflow.NewAuthenticator(config),
	}
}

// IsAuthenticated checks if valid credentials exist
func (a *IFlowAuthenticator) IsAuthenticated() bool {
	return a.auth.IsAuthenticated()
}

// Authenticate performs the OAuth web flow authentication
func (a *IFlowAuthenticator) Authenticate(ctx context.Context) error {
	return a.auth.Authenticate(ctx)
}

// GetAPIKey returns a valid API key for LLM calls
func (a *IFlowAuthenticator) GetAPIKey() string {
	ctx := context.Background()
	token, err := a.auth.GetToken(ctx)
	if err != nil {
		return ""
	}
	return token
}

// GetCredentialsPath returns the path to stored credentials
func (a *IFlowAuthenticator) GetCredentialsPath() string {
	return a.auth.GetCredentialsPath()
}