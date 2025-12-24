package iflow

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/sunbankio/omniproxy/pkg/utils"

	"golang.org/x/oauth2"
)

const (
	AuthURL      = "https://iflow.cn/oauth"
	TokenURL     = "https://iflow.cn/oauth/token"
	UserInfoURL  = "https://iflow.cn/api/oauth/getUserInfo"
	APIKeyURL    = "https://platform.iflow.cn/api/openapi/apikey"
	ClientID     = "10009311001"
	ClientSecret = "4Z3YjXycVsQvyGF1etiNlIBB4RsqSDtW"
	DefaultPort  = 11451
)

// OAuthConfig holds the OAuth configuration for iFlow
type OAuthConfig struct {
	ClientID     string
	ClientSecret string
	RedirectPort int
	CredsPath    string
}

// DefaultOAuthConfig returns the default iFlow OAuth configuration
func DefaultOAuthConfig() *OAuthConfig {
	homeDir, _ := os.UserHomeDir()
	return &OAuthConfig{
		ClientID:     ClientID,
		ClientSecret: ClientSecret,
		RedirectPort: DefaultPort,
		CredsPath:    filepath.Join(homeDir, ".iflow", "oauth_creds.json"),
	}
}

// Credentials represents the stored OAuth credentials for iFlow
type Credentials struct {
	AuthType        string `json:"auth_type"` // "oauth" or "cookie"
	AccessToken     string `json:"access_token,omitempty"`
	RefreshToken    string `json:"refresh_token,omitempty"`
	Expire          string `json:"expire,omitempty"`
	ExpiresAt       string `json:"expires_at,omitempty"`
	ExpiryDate      int64  `json:"expiry_date,omitempty"`
	Cookies         string `json:"cookies,omitempty"`
	CookieExpiresAt string `json:"cookie_expires_at,omitempty"`
	Email           string `json:"email,omitempty"`
	UserID          string `json:"user_id,omitempty"`
	LastRefresh     string `json:"last_refresh,omitempty"`
	APIKey          string `json:"apiKey,omitempty"`
	TokenType       string `json:"token_type,omitempty"`
	Scope           string `json:"scope,omitempty"`
	Type            string `json:"type"` // "iflow"
}

// OAuthFileCredentials represents the exact file structure required by the user
type OAuthFileCredentials struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiryDate   int64  `json:"expiry_date"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
	APIKey       string `json:"apiKey"`
}

// PKCECodes represents PKCE codes for OAuth2 authorization
type PKCECodes struct {
	CodeVerifier  string `json:"code_verifier"`
	CodeChallenge string `json:"code_challenge"`
}

// IsExpired checks if the credentials are expired
func (c *Credentials) IsExpired() bool {
	if c.Expire != "" {
		if expire, err := time.Parse(time.RFC3339, c.Expire); err == nil {
			return expire.Before(time.Now().Add(5 * time.Minute))
		}
	}
	if c.ExpiresAt != "" {
		if expire, err := time.Parse(time.RFC3339, c.ExpiresAt); err == nil {
			return expire.Before(time.Now().Add(5 * time.Minute))
		}
	}
	return true
}

// IsValid checks if the credentials are valid
func (c *Credentials) IsValid() bool {
	if c.AuthType == "oauth" {
		return c.AccessToken != "" && !c.IsExpired()
	}
	if c.AuthType == "cookie" {
		return c.Cookies != "" || c.APIKey != ""
	}
	return false
}

// Authenticator implements the auth.Authenticator interface for iFlow
type Authenticator struct {
	config      *OAuthConfig
	credentials *Credentials
	mu          sync.RWMutex

	httpClient *http.Client
}

// NewAuthenticator creates a new iFlow authenticator
func NewAuthenticator(config *OAuthConfig) *Authenticator {
	if config == nil {
		config = DefaultOAuthConfig()
	}
	return &Authenticator{
		config: config,

		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// GetCredentialsPath returns the path to stored credentials
func (a *Authenticator) GetCredentialsPath() string {
	return a.config.CredsPath
}

// IsAuthenticated checks if valid credentials exist
func (a *Authenticator) IsAuthenticated() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.credentials == nil {
		// Try to load from file
		a.loadCredentials()
		if a.credentials == nil {
			return false
		}
	}

	return a.credentials.IsValid()
}

// loadCredentials loads credentials from file
func (a *Authenticator) loadCredentials() {
	credsPath := a.GetCredentialsPath()

	data, err := os.ReadFile(credsPath)
	if err != nil {
		return
	}

	// Try to load as OAuthFileCredentials first (strict user format)
	var fileCreds OAuthFileCredentials
	if err := json.Unmarshal(data, &fileCreds); err == nil && fileCreds.AccessToken != "" {
		// Map to internal struct
		creds := &Credentials{
			AuthType:     "oauth",
			Type:         "iflow",
			AccessToken:  fileCreds.AccessToken,
			RefreshToken: fileCreds.RefreshToken,
			TokenType:    fileCreds.TokenType,
			Scope:        fileCreds.Scope,
			APIKey:       fileCreds.APIKey,
			ExpiryDate:   fileCreds.ExpiryDate,
		}

		// Convert expiry date (millis) to RFC3339 string for internal use
		if fileCreds.ExpiryDate > 0 {
			t := time.UnixMilli(fileCreds.ExpiryDate)
			creds.ExpiresAt = t.Format(time.RFC3339)
			creds.Expire = creds.ExpiresAt
		}

		a.credentials = creds

		// If we have access token but no API key, try to fetch user info
		if a.credentials.APIKey == "" && a.credentials.AccessToken != "" {
			// In a real load, we might want to avoid network calls, but for now we follow existing logic
			// a.fetchUserInfo()
		}
		return
	}

	// Fallback to standard loading
	var creds Credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return
	}

	a.credentials = &creds

	if a.credentials.AuthType == "" {
		a.credentials.AuthType = "oauth"
	}
	if a.credentials.Type == "" {
		a.credentials.Type = "iflow"
	}
}

// saveCredentials saves credentials to file
func (a *Authenticator) saveCredentials() error {
	if a.credentials == nil {
		return fmt.Errorf("no credentials to save")
	}

	credsPath := a.GetCredentialsPath()

	if err := os.MkdirAll(filepath.Dir(credsPath), 0700); err != nil {
		return fmt.Errorf("failed to create credentials directory: %w", err)
	}

	var data []byte
	var err error

	// Use strict format for OAuth
	if a.credentials.AuthType == "oauth" {
		fileCreds := OAuthFileCredentials{
			AccessToken:  a.credentials.AccessToken,
			RefreshToken: a.credentials.RefreshToken,
			TokenType:    a.credentials.TokenType,
			Scope:        a.credentials.Scope,
			APIKey:       a.credentials.APIKey,
		}

		if a.credentials.ExpiryDate > 0 {
			fileCreds.ExpiryDate = a.credentials.ExpiryDate
		} else if a.credentials.ExpiresAt != "" {
			if t, err := time.Parse(time.RFC3339, a.credentials.ExpiresAt); err == nil {
				fileCreds.ExpiryDate = t.UnixMilli()
			}
		}

		data, err = json.MarshalIndent(fileCreds, "", "  ")
	} else {
		data, err = json.MarshalIndent(a.credentials, "", "  ")
	}

	if err != nil {
		return fmt.Errorf("failed to marshal credentials: %w", err)
	}

	if err := os.WriteFile(credsPath, data, 0600); err != nil {
		return fmt.Errorf("failed to write credentials file: %w", err)
	}

	return nil
}

// GetToken returns a valid API key for LLM calls, refreshing if necessary
func (a *Authenticator) GetToken(ctx context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	// Load credentials if not loaded
	if a.credentials == nil {
		a.loadCredentials()
	}

	// Check if we have credentials
	if a.credentials == nil || a.credentials.AccessToken == "" {
		return "", fmt.Errorf("no valid credentials available")
	}

	// Convert to oauth2.Token
	token := &oauth2.Token{
		AccessToken:  a.credentials.AccessToken,
		RefreshToken: a.credentials.RefreshToken,
		TokenType:    a.credentials.TokenType,
	}

	// Parse expiry if available
	if a.credentials.ExpiresAt != "" {
		if expiry, err := time.Parse(time.RFC3339, a.credentials.ExpiresAt); err == nil {
			token.Expiry = expiry
		}
	}

	// Setup OAuth2 config for iFlow
	conf := &oauth2.Config{
		ClientID:     a.config.ClientID,
		ClientSecret: a.config.ClientSecret,
		Endpoint: oauth2.Endpoint{
			AuthURL:  AuthURL,
			TokenURL: TokenURL,
		},
	}

	// Create a context with the custom HTTP client
	oauth2Context := context.WithValue(ctx, oauth2.HTTPClient, a.httpClient)

	// Create TokenSource with the current token
	ts := conf.TokenSource(oauth2Context, token)

	// Get token (this will refresh if needed)
	newToken, err := ts.Token()
	if err != nil {
		return "", fmt.Errorf("failed to refresh token: %w", err)
	}

	// Update credentials if token changed
	if newToken.AccessToken != a.credentials.AccessToken || newToken.RefreshToken != a.credentials.RefreshToken {
		utils.L().Infow("Token refreshed successfully, saving credentials",
			"provider", "iFlow")

		a.credentials.AccessToken = newToken.AccessToken
		a.credentials.RefreshToken = newToken.RefreshToken
		a.credentials.TokenType = newToken.TokenType
		if !newToken.Expiry.IsZero() {
			a.credentials.ExpiresAt = newToken.Expiry.Format(time.RFC3339)
			a.credentials.ExpiryDate = newToken.Expiry.UnixMilli()
		}

		// Fetch user info and API key after refresh
		if err := a.fetchUserInfo(); err != nil {
			utils.L().Errorw("Failed to fetch user info after refresh",
				"provider", "iFlow",
				"error", err)
		}

		if err := a.saveCredentials(); err != nil {
			utils.L().Errorw("Failed to save refreshed credentials",
				"provider", "iFlow",
				"error", err)
		}
	}

	// If we still don't have an API key, try to fetch it
	if a.credentials.APIKey == "" {
		if err := a.fetchUserInfo(); err != nil {
			utils.L().Errorw("Failed to fetch API key",
				"provider", "iFlow",
				"error", err)
		} else {
			a.saveCredentials()
		}
	}

	// Return the API key for LLM calls, as requested by the user
	if a.credentials.APIKey != "" {
		return a.credentials.APIKey, nil
	}

	// Fallback to AccessToken if APIKey is still not available
	return a.credentials.AccessToken, nil
}

// fetchUserInfo fetches user information and API key
func (a *Authenticator) fetchUserInfo() error {
	if a.credentials == nil || a.credentials.AccessToken == "" {
		return fmt.Errorf("no access token available")
	}

	userInfoURL := fmt.Sprintf("%s?accessToken=%s", UserInfoURL, url.QueryEscape(a.credentials.AccessToken))

	req, err := http.NewRequest("GET", userInfoURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create user info request: %w", err)
	}

	req.Header.Set("Accept", "application/json")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send user info request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("user info request failed (status %d): %s", resp.StatusCode, string(body))
	}

	var userInfoResp map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&userInfoResp); err != nil {
		return fmt.Errorf("failed to decode user info response: %w", err)
	}

	if success, ok := userInfoResp["success"].(bool); !ok || !success {
		return fmt.Errorf("user info request unsuccessful")
	}

	data, ok := userInfoResp["data"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("no data in user info response")
	}

	if apiKey, ok := data["apiKey"].(string); ok {
		a.credentials.APIKey = apiKey
	}

	return nil
}
