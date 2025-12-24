package kiro

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"go.uber.org/zap"
)

const (
	TokenRefreshBufferMs = 1800 * 1000 // 30 minutes
)

// OAuthConfig holds the OAuth configuration for Kiro
type OAuthConfig struct {
	Region        string
	RefreshURL    string
	RefreshIDCURL string
	BaseURL       string
	CredsPath     string
}

// DefaultOAuthConfig returns the default Kiro OAuth configuration
func DefaultOAuthConfig() *OAuthConfig {
	homeDir, _ := os.UserHomeDir()
	return &OAuthConfig{
		Region:        "us-east-1",
		RefreshURL:    "https://prod.{{region}}.auth.desktop.kiro.dev/refreshToken",
		RefreshIDCURL: "https://oidc.{{region}}.amazonaws.com/token",
		BaseURL:       "https://codewhisperer.{{region}}.amazonaws.com",
		CredsPath:     filepath.Join(homeDir, ".aws", "sso", "cache", "kiro-auth-token.json"),
	}
}

// Credentials represents the stored Kiro credentials
type Credentials struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken,omitempty"`
	ExpiresAt    string `json:"expiresAt,omitempty"`
	ClientID     string `json:"clientId,omitempty"`
	ClientSecret string `json:"clientSecret,omitempty"`
	AuthMethod   string `json:"authMethod,omitempty"`
	Region       string `json:"region,omitempty"`
	ProfileArn   string `json:"profileArn,omitempty"`
}

// Authenticator implements the Authenticator interface for Kiro
type Authenticator struct {
	config      *OAuthConfig
	credentials *Credentials
	mu          sync.RWMutex
	logger      *zap.SugaredLogger
	httpClient  *http.Client
}

// NewAuthenticator creates a new Kiro authenticator
func NewAuthenticator(config *OAuthConfig) *Authenticator {
	if config == nil {
		config = DefaultOAuthConfig()
	}
	return &Authenticator{
		config:     config,
		logger:     zap.NewExample().Sugar(),
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// GetCredentialsPath returns the path to the credentials file
func (a *Authenticator) GetCredentialsPath() string {
	return a.config.CredsPath
}

// IsAuthenticated checks if valid credentials exist
func (a *Authenticator) IsAuthenticated() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.credentials == nil {
		creds, err := a.loadCredentials()
		if err != nil {
			return false
		}
		a.mu.RUnlock()
		a.mu.Lock()
		a.credentials = creds
		a.mu.Unlock()
		a.mu.RLock()
	}

	// Check if token is still valid
	if a.credentials.ExpiresAt != "" {
		expiresAt, err := time.Parse(time.RFC3339, a.credentials.ExpiresAt)
		// Check against 30 minute buffer
		buffer := time.Duration(TokenRefreshBufferMs) * time.Millisecond
		if err == nil && expiresAt.Before(time.Now().Add(buffer)) {
			return false
		}
	}

	return a.credentials != nil && a.credentials.AccessToken != ""
}

// loadCredentials loads credentials from file
func (a *Authenticator) loadCredentials() (*Credentials, error) {
	credsPath := a.GetCredentialsPath()

	// First try to load the main credentials file
	data, err := os.ReadFile(credsPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read credentials file: %w", err)
	}

	var creds Credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, fmt.Errorf("failed to parse credentials: %w", err)
	}

	// Also try to load additional credentials from the same directory
	dir := filepath.Dir(credsPath)
	files, err := os.ReadDir(dir)
	if err == nil {
		for _, file := range files {
			if file.IsDir() || !strings.HasSuffix(file.Name(), ".json") {
				continue
			}
			if file.Name() == filepath.Base(credsPath) {
				continue
			}

			filePath := filepath.Join(dir, file.Name())
			fileData, err := os.ReadFile(filePath)
			if err != nil {
				continue
			}

			var additionalCreds Credentials
			if err := json.Unmarshal(fileData, &additionalCreds); err != nil {
				continue
			}

			// Merge additional credentials (client info)
			if additionalCreds.ClientID != "" && creds.ClientID == "" {
				creds.ClientID = additionalCreds.ClientID
			}
			if additionalCreds.ClientSecret != "" && creds.ClientSecret == "" {
				creds.ClientSecret = additionalCreds.ClientSecret
			}
		}
	}

	// Set region from credentials or use default
	if creds.Region == "" {
		creds.Region = a.config.Region
	}

	return &creds, nil
}

// saveCredentials saves credentials to file
func (a *Authenticator) saveCredentials(creds *Credentials) error {
	credsPath := a.GetCredentialsPath()

	// Create directory if it doesn't exist
	dir := filepath.Dir(credsPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create credentials directory: %w", err)
	}

	// Load existing credentials to merge
	existingData, _ := os.ReadFile(credsPath)
	var existing Credentials
	if len(existingData) > 0 {
		json.Unmarshal(existingData, &existing)
	}

	// Merge new credentials with existing
	if creds.AccessToken != "" {
		existing.AccessToken = creds.AccessToken
	}
	if creds.RefreshToken != "" {
		existing.RefreshToken = creds.RefreshToken
	}
	if creds.ExpiresAt != "" {
		existing.ExpiresAt = creds.ExpiresAt
	}

	data, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal credentials: %w", err)
	}

	if err := os.WriteFile(credsPath, data, 0600); err != nil {
		return fmt.Errorf("failed to write credentials file: %w", err)
	}

	return nil
}

// kiroTokenRefresher implements oauth2.TokenSource for Kiro's custom protocol
type kiroTokenRefresher struct {
	auth *Authenticator
	ctx  context.Context
}

func (k *kiroTokenRefresher) Token() (*oauth2.Token, error) {
	// Call internal refresh logic
	return k.auth.performRefresh(k.ctx)
}

// GetToken returns a valid access token, refreshing if necessary
func (a *Authenticator) GetToken(ctx context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	// Load credentials if not in memory
	if a.credentials == nil {
		creds, err := a.loadCredentials()
		if err != nil {
			return "", fmt.Errorf("credentials not found: %w", err)
		}
		a.credentials = creds
	}

	if a.credentials.AccessToken == "" {
		return "", fmt.Errorf("no access token available")
	}

	// Construct oauth2.Token
	var expiry time.Time
	if a.credentials.ExpiresAt != "" {
		if t, err := time.Parse(time.RFC3339, a.credentials.ExpiresAt); err == nil {
			expiry = t
		}
	}

	token := &oauth2.Token{
		AccessToken:  a.credentials.AccessToken,
		RefreshToken: a.credentials.RefreshToken,
		Expiry:       expiry,
		TokenType:    "Bearer",
	}

	// Check buffer (30 mins)
	buffer := time.Duration(TokenRefreshBufferMs) * time.Millisecond
	if time.Until(token.Expiry) < buffer {
		a.logger.Infow("Token expiring in less than 30m or expired, forcing refresh",
			"provider", "Kiro")
		token.Expiry = time.Now().Add(-1 * time.Second)
	}

	// Create TokenSource
	ts := oauth2.ReuseTokenSource(token, &kiroTokenRefresher{auth: a, ctx: ctx})

	// Get token (this triggers refresh if expired)
	newToken, err := ts.Token()
	if err != nil {
		a.logger.Errorw("Failed to refresh token",
			"provider", "Kiro",
			"error", err)
		return "", err
	}

	// Update credentials if changed
	if newToken.AccessToken != a.credentials.AccessToken || newToken.RefreshToken != a.credentials.RefreshToken {
		a.logger.Infow("Token refreshed successfully, saving credentials",
			"provider", "Kiro")
		a.credentials.AccessToken = newToken.AccessToken
		a.credentials.RefreshToken = newToken.RefreshToken
		a.credentials.ExpiresAt = newToken.Expiry.Format(time.RFC3339)

		if err := a.saveCredentials(a.credentials); err != nil {
			a.logger.Errorw("Failed to save refreshed credentials",
				"provider", "Kiro",
				"error", err)
		}
	}

	return a.credentials.AccessToken, nil
}

// performRefresh executes the custom Kiro refresh logic and returns an oauth2.Token
func (a *Authenticator) performRefresh(ctx context.Context) (*oauth2.Token, error) {
	if a.credentials == nil || a.credentials.RefreshToken == "" {
		return nil, fmt.Errorf("no refresh token available")
	}

	region := a.credentials.Region
	if region == "" {
		region = a.config.Region
	}

	// Determine refresh URL based on auth method
	var refreshURL string
	if a.credentials.AuthMethod == "social" {
		refreshURL = strings.ReplaceAll(a.config.RefreshURL, "{{region}}", region)
	} else {
		refreshURL = strings.ReplaceAll(a.config.RefreshIDCURL, "{{region}}", region)
	}

	// Build request body
	requestBody := map[string]string{
		"refreshToken": a.credentials.RefreshToken,
	}
	if a.credentials.AuthMethod != "social" {
		requestBody["clientId"] = a.credentials.ClientID
		requestBody["clientSecret"] = a.credentials.ClientSecret
		requestBody["grantType"] = "refresh_token"
	}

	bodyBytes, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal refresh request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", refreshURL, strings.NewReader(string(bodyBytes)))
	if err != nil {
		return nil, fmt.Errorf("failed to create refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send refresh request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token refresh failed with status: %d", resp.StatusCode)
	}

	var tokenResp struct {
		AccessToken  string `json:"accessToken"`
		ExpiresIn    int64  `json:"expiresIn"`
		RefreshToken string `json:"refreshToken,omitempty"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, fmt.Errorf("failed to decode refresh response: %w", err)
	}

	// Return as oauth2.Token
	// Ensure we preserve the old refresh token if new one is empty
	refreshToken := tokenResp.RefreshToken
	if refreshToken == "" {
		refreshToken = a.credentials.RefreshToken
	}

	return &oauth2.Token{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: refreshToken,
		Expiry:       time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second),
		TokenType:    "Bearer",
	}, nil
}

// GetAuthMethod returns the authentication method used
func (a *Authenticator) GetAuthMethod() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.credentials != nil {
		return a.credentials.AuthMethod
	}
	return ""
}

// GetProfileArn returns the profile ARN if available
func (a *Authenticator) GetProfileArn() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.credentials != nil {
		return a.credentials.ProfileArn
	}
	return ""
}

// GetRegion returns the configured region
func (a *Authenticator) GetRegion() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.credentials != nil && a.credentials.Region != "" {
		return a.credentials.Region
	}
	return a.config.Region
}