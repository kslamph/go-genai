package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"google.golang.org/genai"

	"github.com/sunbankio/omniproxy/internal/provider/antigravity"
	"github.com/sunbankio/omniproxy/internal/provider/gemini"
	"github.com/sunbankio/omniproxy/internal/provider/iflow"
	"github.com/sunbankio/omniproxy/internal/provider/qwen"
	"github.com/sunbankio/omniproxy/pkg/utils"
)

// Manager implements the AuthManager interface
type Manager struct {
	// OAuth configs for different providers
	geminiConfig      *gemini.OAuthConfig
	iflowConfig       *iflow.OAuthConfig
	qwenConfig        *qwen.OAuthConfig
	antigravityConfig *antigravity.OAuthConfig
}

// NewManager creates a new AuthManager instance
func NewManager() *Manager {
	return &Manager{
		geminiConfig:      gemini.DefaultOAuthConfig(),
		iflowConfig:       iflow.DefaultOAuthConfig(),
		qwenConfig:        qwen.DefaultOAuthConfig(),
		antigravityConfig: antigravity.DefaultOAuthConfig(),
	}
}

// NewManagerWithConfigs creates a new AuthManager with custom OAuth configs
func NewManagerWithConfigs(
	geminiConfig *gemini.OAuthConfig,
	iflowConfig *iflow.OAuthConfig,
	qwenConfig *qwen.OAuthConfig,
	antigravityConfig *antigravity.OAuthConfig,
) *Manager {
	return &Manager{
		geminiConfig:      geminiConfig,
		iflowConfig:       iflowConfig,
		qwenConfig:        qwenConfig,
		antigravityConfig: antigravityConfig,
	}
}

// Validate checks if the credential is valid and usable
func (m *Manager) Validate(cred *Credential) error {
	if cred == nil {
		return fmt.Errorf("credential is nil")
	}

	cred.mu.RLock()
	defer cred.mu.RUnlock()

	// Check if access token is present
	if cred.AccessToken == "" {
		return fmt.Errorf("access token is empty")
	}

	// Check if credential is in penalty box or dead
	if cred.State == CredentialStateDead {
		return fmt.Errorf("credential is dead")
	}

	return nil
}

// EnsureValidToken ensures the credential has a valid token, refreshing if necessary
func (m *Manager) EnsureValidToken(ctx context.Context, cred *Credential) error {
	if err := m.Validate(cred); err != nil {
		return err
	}

	cred.mu.RLock()
	expiry := cred.Expiry
	cred.mu.RUnlock()

	// Check if token is expiring within 1 minute
	if time.Until(expiry) < 1*time.Minute {
		// Token is expiring soon or already expired, force refresh
		return m.ForceRefresh(ctx, cred)
	}

	return nil
}

// ForceRefresh forces a refresh of the credential's token and client
func (m *Manager) ForceRefresh(ctx context.Context, cred *Credential) error {
	if cred == nil {
		return fmt.Errorf("credential is nil")
	}

	// Switch on provider type to call appropriate refresh logic
	switch cred.ProviderType {
	case ProviderTypeGemini:
		return m.refreshGeminiToken(ctx, cred)
	case ProviderTypeIFlow:
		return m.refreshIFlowToken(ctx, cred)
	case ProviderTypeQwen:
		return m.refreshQwenToken(ctx, cred)
	case ProviderTypeAntigravity:
		return m.refreshAntigravityToken(ctx, cred)
	case ProviderTypeOpenAI:
		// OpenAI doesn't use OAuth, no refresh needed
		return nil
	default:
		return fmt.Errorf("unsupported provider type: %v", cred.ProviderType)
	}
}

// RefreshClient refreshes the client associated with the credential
func (m *Manager) RefreshClient(ctx context.Context, cred *Credential) error {
	if cred == nil {
		return fmt.Errorf("credential is nil")
	}

	// First ensure we have a valid token
	if err := m.EnsureValidToken(ctx, cred); err != nil {
		return err
	}

	// Switch on provider type to refresh the appropriate client
	switch cred.ProviderType {
	case ProviderTypeGemini:
		return m.refreshGeminiClient(ctx, cred)
	case ProviderTypeIFlow:
		return m.refreshIFlowClient(cred)
	case ProviderTypeQwen:
		return m.refreshQwenClient(cred)
	case ProviderTypeAntigravity:
		return m.refreshAntigravityClient(ctx, cred)
	case ProviderTypeOpenAI:
		// OpenAI client recreation logic would go here
		return nil
	default:
		return fmt.Errorf("unsupported provider type: %v", cred.ProviderType)
	}
}

// Provider-specific refresh methods (skeleton implementations)

func (m *Manager) refreshGeminiToken(ctx context.Context, cred *Credential) error {
	// Get the stored authenticator from the credential
	authInterface := cred.GetAuthenticator()
	if authInterface == nil {
		return fmt.Errorf("no authenticator stored in credential %s", cred.ID)
	}

	auth, ok := authInterface.(*gemini.Authenticator)
	if !ok {
		return fmt.Errorf("invalid authenticator type for credential %s", cred.ID)
	}

	// Force refresh the token
	if err := auth.ForceRefresh(ctx); err != nil {
		return fmt.Errorf("failed to refresh Gemini token: %w", err)
	}

	// Update the credential with the refreshed token information
	cred.mu.Lock()
	defer cred.mu.Unlock()

	// Get the refreshed token
	token, err := auth.GetToken(ctx)
	if err != nil {
		return fmt.Errorf("failed to get refreshed Gemini token: %w", err)
	}

	// Update credential fields
	cred.AccessToken = token

	// Read actual expiry from the authenticator's credentials
	// The authenticator stores the expiry date in its credentials file
	authCredsPath := auth.GetCredentialsPath()
	if authCredsPath != "" {
		// Read the credentials file to get the actual expiry
		data, readErr := os.ReadFile(authCredsPath)
		if readErr == nil {
			var geminiCreds struct {
				ExpiryDate int64 `json:"expiry_date"`
			}
			if jsonErr := json.Unmarshal(data, &geminiCreds); jsonErr == nil && geminiCreds.ExpiryDate > 0 {
				cred.Expiry = time.Unix(geminiCreds.ExpiryDate, 0)
			} else {
				// Fallback to 1 hour if we can't read the expiry
				cred.Expiry = time.Now().Add(1 * time.Hour)
			}
		} else {
			// Fallback to 1 hour if we can't read the file
			cred.Expiry = time.Now().Add(1 * time.Hour)
		}
	} else {
		// Fallback to 1 hour if we can't get the credentials path
		cred.Expiry = time.Now().Add(1 * time.Hour)
	}

	utils.L().Infow("Successfully refreshed Gemini token",
		"credential_id", cred.ID,
		"expiry", cred.Expiry.Format(time.RFC3339))

	return nil
}

func (m *Manager) refreshIFlowToken(ctx context.Context, cred *Credential) error {
	// Get the stored authenticator from the credential
	authInterface := cred.GetAuthenticator()
	if authInterface == nil {
		return fmt.Errorf("no authenticator stored in credential %s", cred.ID)
	}

	auth, ok := authInterface.(*iflow.Authenticator)
	if !ok {
		return fmt.Errorf("invalid authenticator type for credential %s", cred.ID)
	}

	// Get token which handles refresh internally
	token, err := auth.GetToken(ctx)
	if err != nil {
		utils.L().Errorw("Failed to refresh iFlow token",
			"credential_id", cred.ID,
			"error", err)
		return fmt.Errorf("failed to refresh iFlow token: %w", err)
	}

	// Update the credential with the refreshed token information
	cred.mu.Lock()
	defer cred.mu.Unlock()

	// Update credential fields
	cred.AccessToken = token
	// Note: iFlow uses a different expiry format, but we'll update it here
	// The exact expiry handling might need adjustment based on iFlow's response format

	utils.L().Infow("Successfully refreshed iFlow token",
		"credential_id", cred.ID)

	return nil
}

func (m *Manager) refreshQwenToken(ctx context.Context, cred *Credential) error {
	// Get the stored authenticator from the credential
	authInterface := cred.GetAuthenticator()
	if authInterface == nil {
		return fmt.Errorf("no authenticator stored in credential %s", cred.ID)
	}

	auth, ok := authInterface.(*qwen.Authenticator)
	if !ok {
		return fmt.Errorf("invalid authenticator type for credential %s", cred.ID)
	}

	// Get token which handles refresh internally
	token, err := auth.GetToken(ctx)
	if err != nil {
		utils.L().Errorw("Failed to refresh Qwen token",
			"credential_id", cred.ID,
			"error", err)
		return fmt.Errorf("failed to refresh Qwen token: %w", err)
	}

	// Update the credential with the refreshed token information
	cred.mu.Lock()
	defer cred.mu.Unlock()

	// Update credential fields
	cred.AccessToken = token
	// Note: Qwen expiry handling would need to be extracted from the authenticator
	// For now, the token is updated and expiry would be handled on next access

	utils.L().Infow("Successfully refreshed Qwen token",
		"credential_id", cred.ID)

	return nil
}

func (m *Manager) refreshAntigravityToken(ctx context.Context, cred *Credential) error {
	// Get the stored authenticator from the credential
	authInterface := cred.GetAuthenticator()
	if authInterface == nil {
		return fmt.Errorf("no authenticator stored in credential %s", cred.ID)
	}

	auth, ok := authInterface.(*antigravity.Authenticator)
	if !ok {
		return fmt.Errorf("invalid authenticator type for credential %s", cred.ID)
	}

	// Force refresh the token
	if err := auth.ForceRefresh(ctx); err != nil {
		utils.L().Errorw("Failed to refresh Antigravity token",
			"credential_id", cred.ID,
			"error", err)
		return fmt.Errorf("failed to refresh Antigravity token: %w", err)
	}

	// Update the credential with the refreshed token information
	cred.mu.Lock()
	defer cred.mu.Unlock()

	// Get the refreshed token
	token, err := auth.GetToken(ctx)
	if err != nil {
		utils.L().Errorw("Failed to get refreshed Antigravity token",
			"credential_id", cred.ID,
			"error", err)
		return fmt.Errorf("failed to get refreshed Antigravity token: %w", err)
	}

	// Update credential fields
	cred.AccessToken = token

	// Read actual expiry from the authenticator
	expiryDate := auth.GetExpiryDate()
	if expiryDate > 0 {
		cred.Expiry = time.Unix(expiryDate, 0)
	} else {
		// Fallback to 1 hour if we can't get the expiry
		cred.Expiry = time.Now().Add(1 * time.Hour)
	}

	utils.L().Infow("Successfully refreshed Antigravity token",
		"credential_id", cred.ID,
		"expiry", cred.Expiry.Format(time.RFC3339))

	return nil
}

func (m *Manager) refreshGeminiClient(ctx context.Context, cred *Credential) error {
	// Ensure client pool exists
	if cred.clientPool == nil {
		cred.clientPool = NewClientPool()
	}

	// Create TokenProviderAdapter
	tokenProvider := NewTokenProviderAdapter(cred, m)

	// Use project ID from credential or default
	projectID := cred.ProjectID
	if projectID == "" {
		projectID = "genai-genesis" // Default for Gemini
	}

	// Refresh the client with new token
	err := cred.clientPool.RefreshClient(
		ctx,
		tokenProvider,
		projectID,
		genai.BackendGeminiCLI, // Use GeminiCLI backend as seen in provider
		"",                     // No custom base URL for Gemini
	)

	if err != nil {
		return fmt.Errorf("failed to refresh Gemini client: %w", err)
	}

	utils.L().Infow("Successfully refreshed Gemini client",
		"credential_id", cred.ID,
		"project_id", projectID)

	return nil
}

func (m *Manager) refreshIFlowClient(cred *Credential) error {
	// iFlow uses OpenAI-compatible client with TokenTransport
	// The TokenTransport automatically picks up the refreshed token from the credential
	// No explicit client refresh needed
	utils.L().Infow("iFlow client refresh not required (uses TokenTransport)",
		"credential_id", cred.ID)
	return nil
}

func (m *Manager) refreshQwenClient(cred *Credential) error {
	// Qwen uses OpenAI-compatible client with TokenTransport
	// The TokenTransport automatically picks up the refreshed token from the credential
	// No explicit client refresh needed
	utils.L().Infow("Qwen client refresh not required (uses TokenTransport)",
		"credential_id", cred.ID)
	return nil
}

func (m *Manager) refreshAntigravityClient(ctx context.Context, cred *Credential) error {
	// Ensure client pool exists
	if cred.clientPool == nil {
		cred.clientPool = NewClientPool()
	}

	// Create TokenProviderAdapter
	tokenProvider := NewTokenProviderAdapter(cred, m)

	// Use project ID from credential or default
	projectID := cred.ProjectID
	if projectID == "" {
		projectID = "antigravity-test-project" // Default fallback for Antigravity
	}

	// Refresh the client with new token
	// Note: Don't set BaseURL explicitly - let the genai library handle it automatically
	// This matches the POC behavior where BaseURL is not set
	err := cred.clientPool.RefreshClient(
		ctx,
		tokenProvider,
		projectID,
		genai.BackendAntigravity, // Use Antigravity backend
		"",                       // Empty BaseURL - let library auto-detect
	)

	if err != nil {
		return fmt.Errorf("failed to refresh Antigravity client: %w", err)
	}

	utils.L().Infow("Successfully refreshed Antigravity client",
		"credential_id", cred.ID,
		"project_id", projectID)

	return nil
}
