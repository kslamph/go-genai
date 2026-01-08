package kiro

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Kiro OAuth constants
const (
	DefaultBaseURL       = "https://codewhisperer.us-east-1.amazonaws.com"
	TokenRefreshBufferMs = 1800 * 1000 // 30 minutes
	SocialRefreshURL     = "https://prod.{{region}}.auth.desktop.kiro.dev/refreshToken"
	IdCRefreshURL        = "https://oidc.{{region}}.amazonaws.com/token"
	KiroAuthTokenPath    = ".aws/sso/cache/kiro-auth-token.json"
)

// OAuthConfig holds the OAuth configuration for Kiro
type OAuthConfig struct {
	Region     string
	RefreshURL string
	IdCURL     string
	BaseURL    string
	CredsPath  string
}

// DefaultOAuthConfig returns the default Kiro OAuth configuration
func DefaultOAuthConfig() *OAuthConfig {
	homeDir, _ := os.UserHomeDir()
	return &OAuthConfig{
		Region:     "us-east-1",
		RefreshURL: SocialRefreshURL,
		IdCURL:     IdCRefreshURL,
		BaseURL:    DefaultBaseURL,
		CredsPath:  filepath.Join(homeDir, KiroAuthTokenPath),
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
	UUID         string `json:"uuid,omitempty"`
}

// IsExpired checks if the credentials are expired (with 5 minute buffer)
func (c *Credentials) IsExpired() bool {
	if c.ExpiresAt == "" {
		return true
	}
	expiresAt, err := parseExpiresAt(c.ExpiresAt)
	if err != nil {
		return true
	}
	// Add 5 minute buffer before expiry
	return expiresAt.Before(time.Now().Add(5 * time.Minute))
}

// parseExpiresAt parses the expiresAt field which may contain milliseconds
func parseExpiresAt(s string) (time.Time, error) {
	// Try with milliseconds first (e.g., "2026-01-03T08:06:27.924Z")
	if t, err := time.Parse("2006-01-02T15:04:05.999Z", s); err == nil {
		return t, nil
	}
	// Fallback to standard RFC3339
	return time.Parse(time.RFC3339, s)
}

// IsValid checks if the credentials are valid
func (c *Credentials) IsValid() bool {
	return c.AccessToken != "" && !c.IsExpired()
}

// Authenticator implements the auth.Authenticator interface for Kiro
type Authenticator struct {
	config      *OAuthConfig
	credentials *Credentials
	mu          sync.RWMutex

	httpClient *http.Client
}

// NewAuthenticator creates a new Kiro authenticator
func NewAuthenticator(config *OAuthConfig) *Authenticator {
	if config == nil {
		config = DefaultOAuthConfig()
	}
	return &Authenticator{
		config: config,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// GetCredentialsPath returns the path to the credentials file
func (a *Authenticator) GetCredentialsPath() string {
	return a.config.CredsPath
}

// IsAuthenticated checks if valid credentials exist
// Returns true if credentials exist (even if expired, GetToken will refresh them)
func (a *Authenticator) IsAuthenticated() bool {
	a.mu.RLock()

	if a.credentials != nil {
		// If we have credentials in memory, check if they're usable
		if a.credentials.AccessToken != "" {
			a.mu.RUnlock()
			return true
		}
	}
	a.mu.RUnlock()

	// Try to load credentials
	creds, err := a.loadCredentials()
	if err != nil {
		return false
	}

	a.mu.Lock()
	a.credentials = creds
	a.mu.Unlock()

	// Return true if we have an access token (GetToken will refresh if needed)
	return creds.AccessToken != ""
}

// loadCredentials loads credentials from file
func (a *Authenticator) loadCredentials() (*Credentials, error) {
	credsPath := a.GetCredentialsPath()

	data, err := os.ReadFile(credsPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read credentials file: %w", err)
	}

	var creds Credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, fmt.Errorf("failed to parse credentials: %w", err)
	}

	// Set region from config if not in credentials
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

	// Check if token needs refresh
	if a.credentials.IsExpired() {
		if err := a.refreshAccessToken(ctx); err != nil {
			return "", fmt.Errorf("failed to refresh token: %w", err)
		}
	}

	return a.credentials.AccessToken, nil
}

// refreshAccessToken refreshes the OAuth token using the refresh token
func (a *Authenticator) refreshAccessToken(ctx context.Context) error {
	if a.credentials == nil || a.credentials.RefreshToken == "" {
		return fmt.Errorf("no refresh token available")
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
		refreshURL = strings.ReplaceAll(a.config.IdCURL, "{{region}}", region)
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
		return fmt.Errorf("failed to marshal refresh request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", refreshURL, strings.NewReader(string(bodyBytes)))
	if err != nil {
		return fmt.Errorf("failed to create refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send refresh request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("token refresh failed with status: %d", resp.StatusCode)
	}

	var tokenResp struct {
		AccessToken  string `json:"accessToken"`
		ExpiresIn    int64  `json:"expiresIn"`
		RefreshToken string `json:"refreshToken,omitempty"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return fmt.Errorf("failed to decode refresh response: %w", err)
	}

	// Update credentials
	a.credentials.AccessToken = tokenResp.AccessToken
	if tokenResp.RefreshToken != "" {
		a.credentials.RefreshToken = tokenResp.RefreshToken
	}
	if tokenResp.ExpiresIn > 0 {
		a.credentials.ExpiresAt = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second).Format(time.RFC3339)
	}

	// Save updated credentials
	if err := a.saveCredentials(a.credentials); err != nil {
		return fmt.Errorf("failed to save credentials: %w", err)
	}

	return nil
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

// GenerateMachineID generates a unique machine ID based on credentials
// Priority: UUID > ProfileArn > ClientID > Default
func (a *Authenticator) GenerateMachineID() string {
	a.mu.RLock()
	defer a.mu.RUnlock()

	var uniqueKey string
	if a.credentials != nil {
		if a.credentials.UUID != "" {
			uniqueKey = a.credentials.UUID
		} else if a.credentials.ProfileArn != "" {
			uniqueKey = a.credentials.ProfileArn
		} else if a.credentials.ClientID != "" {
			uniqueKey = a.credentials.ClientID
		}
	}

	if uniqueKey == "" {
		uniqueKey = "KIRO_DEFAULT_MACHINE"
	}

	hash := sha256.Sum256([]byte(uniqueKey))
	return hex.EncodeToString(hash[:8]) // Use first 8 bytes for shorter ID
}
