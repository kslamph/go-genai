package iflow

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/flock"
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
		creds, err := a.loadCredentials()
		if err != nil {
			return false
		}
		a.credentials = creds
	}

	return a.credentials.IsValid()
}

// loadCredentials loads credentials from file
func (a *Authenticator) loadCredentials() (*Credentials, error) {
	credsPath := a.GetCredentialsPath()

	data, err := os.ReadFile(credsPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read credentials file: %w", err)
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

		// If we have access token but no API key, try to fetch user info
		if creds.APIKey == "" && creds.AccessToken != "" {
			// In a real load, we might want to avoid network calls, but for now we follow existing logic
			// a.fetchUserInfo()
		}
		return creds, nil
	}

	// Fallback to standard loading
	var creds Credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, fmt.Errorf("failed to parse credentials: %w", err)
	}

	if creds.AuthType == "" {
		creds.AuthType = "oauth"
	}
	if creds.Type == "" {
		creds.Type = "iflow"
	}

	return &creds, nil
}

// saveCredentials saves credentials to file
func (a *Authenticator) saveCredentials() error {
	if a.credentials == nil {
		return fmt.Errorf("no credentials to save")
	}

	credsPath := a.GetCredentialsPath()
	lockPath := credsPath + ".lock"

	if err := os.MkdirAll(filepath.Dir(credsPath), 0700); err != nil {
		return fmt.Errorf("failed to create credentials directory: %w", err)
	}

	// Use a file lock to prevent race conditions
	fileLock := flock.New(lockPath)
	locked, lockErr := fileLock.TryLock()
	if lockErr != nil {
		return fmt.Errorf("failed to acquire file lock: %v", lockErr)
	}
	if !locked {
		return fmt.Errorf("failed to acquire file lock, another process is holding it")
	}
	defer fileLock.Unlock()

	var data []byte

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
			if t, parseErr := time.Parse(time.RFC3339, a.credentials.ExpiresAt); parseErr == nil {
				fileCreds.ExpiryDate = t.UnixMilli()
			}
		}

		var marshalErr error
		data, marshalErr = json.MarshalIndent(fileCreds, "", "  ")
		if marshalErr != nil {
			return fmt.Errorf("failed to marshal credentials: %w", marshalErr)
		}
	} else {
		var marshalErr error
		data, marshalErr = json.MarshalIndent(a.credentials, "", "  ")
		if marshalErr != nil {
			return fmt.Errorf("failed to marshal credentials: %w", marshalErr)
		}
	}

	if err := os.WriteFile(credsPath, data, 0600); err != nil {
		return fmt.Errorf("failed to write credentials file: %w", err)
	}

	return nil
}

// IsTokenValid checks if the token is still valid
func IsTokenValid(credentials Credentials) bool {
	if credentials.ExpiryDate == 0 {
		return false
	}
	// Add 30 second buffer. TokenRefreshBufferMs is defined in constants.go
	return time.Now().UnixMilli() < credentials.ExpiryDate-1800*1000 // 30 minutes
}

// refreshAccessToken refreshes the OAuth token using the refresh token
func (a *Authenticator) refreshAccessToken(credentials Credentials) (Credentials, error) {
	if credentials.RefreshToken == "" {
		return Credentials{}, fmt.Errorf("no refresh token available")
	}

	conf := &oauth2.Config{
		ClientID:     a.config.ClientID,
		ClientSecret: a.config.ClientSecret,
		Endpoint: oauth2.Endpoint{
			TokenURL: TokenURL,
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	token := &oauth2.Token{
		RefreshToken: credentials.RefreshToken,
	}

	tokenSource := conf.TokenSource(ctx, token)
	newToken, err := tokenSource.Token()
	if err != nil {
		return Credentials{}, fmt.Errorf("failed to refresh token: %w", err)
	}

	updatedCredentials := Credentials{
		AccessToken:  newToken.AccessToken,
		TokenType:    newToken.TokenType,
		RefreshToken: newToken.RefreshToken,
		ExpiryDate:   newToken.Expiry.UnixMilli(),
	}

	// Scope might update
	if extraScope, ok := newToken.Extra("scope").(string); ok && extraScope != "" {
		updatedCredentials.Scope = extraScope
	}

	// Fetch user info and API key after refresh
	if err := a.fetchUserInfo(); err != nil {
		utils.L().Errorw("Failed to fetch user info after refresh",
			"provider", "iFlow",
			"error", err)
	}

	if err := a.saveCredentials(); err != nil {
		return Credentials{}, fmt.Errorf("failed to save updated credentials: %v", err)
	}

	return updatedCredentials, nil
}

// GetToken returns a valid API key for LLM calls, refreshing if necessary
func (a *Authenticator) GetToken(ctx context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	// Load credentials if not loaded
	if a.credentials == nil {
		creds, loadErr := a.loadCredentials()
		if loadErr != nil {
			return "", fmt.Errorf("credentials not found: %w", loadErr)
		}
		a.credentials = creds
	}

	// Check if we have credentials
	if a.credentials == nil || a.credentials.AccessToken == "" {
		return "", fmt.Errorf("no valid credentials available")
	}

	// Check if token is valid
	if IsTokenValid(*a.credentials) {
		// If we still don't have an API key, try to fetch it
		if a.credentials.APIKey == "" {
			if err := a.fetchUserInfo(); err != nil {
				utils.L().Errorw("Failed to fetch API key",
					"provider", "iFlow",
					"error", err)
			} else {
				_ = a.saveCredentials()
			}
		}

		// Return the API key for LLM calls, as requested by the user
		if a.credentials.APIKey != "" {
			return a.credentials.APIKey, nil
		}
		// Fallback to AccessToken if APIKey is still not available
		return a.credentials.AccessToken, nil
	}

	// Token is invalid, try to refresh
	updatedCreds, err := a.refreshAccessToken(*a.credentials)
	if err != nil {
		utils.L().Errorw("Token refresh failed",
			"provider", "iFlow",
			"error", err)
		return "", fmt.Errorf("failed to refresh token: %w", err)
	}

	utils.L().Infow("Token refreshed successfully",
		"provider", "iFlow")

	a.credentials = &updatedCreds

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

// Authenticate performs the OAuth web flow authentication
func (a *Authenticator) Authenticate(ctx context.Context) error {
	// Generate state for CSRF protection
	stateBytes := make([]byte, 16)
	if _, err := rand.Read(stateBytes); err != nil {
		return fmt.Errorf("failed to generate state: %w", err)
	}
	state := base64.URLEncoding.EncodeToString(stateBytes)

	redirectURI := fmt.Sprintf("http://localhost:%d", a.config.RedirectPort)

	// Build authorization URL
	authURL := fmt.Sprintf(
		"%s?client_id=%s&redirect_uri=%s&response_type=code&scope=%s&access_type=offline&prompt=consent&state=%s",
		AuthURL,
		url.QueryEscape(a.config.ClientID),
		url.QueryEscape(redirectURI),
		url.QueryEscape("openid email profile offline_access"),
		url.QueryEscape(state),
	)

	// Channel to receive the authorization code
	codeChan := make(chan string, 1)
	errChan := make(chan error, 1)

	// Start local server to receive callback
	server := &http.Server{Addr: fmt.Sprintf(":%d", a.config.RedirectPort)}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Verify state
		if r.URL.Query().Get("state") != state {
			errChan <- fmt.Errorf("state mismatch")
			http.Error(w, "State mismatch", http.StatusBadRequest)
			return
		}

		code := r.URL.Query().Get("code")
		if code == "" {
			errChan <- fmt.Errorf("no code in callback")
			http.Error(w, "No code received", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html><body><h1>Authorization successful!</h1><p>You can close this window.</p></body></html>"))
		codeChan <- code
	})

	// Start server in goroutine
	go func() {
		if err := server.ListenAndServe(); err != http.ErrServerClosed {
			errChan <- err
		}
	}()

	// Print authorization URL for user
	fmt.Printf("\n[iFlow Auth] Please visit the following URL to authorize:\n\n%s\n\n", authURL)
	fmt.Println("[iFlow Auth] Waiting for authorization...")

	// Wait for code or error
	var code string
	select {
	case code = <-codeChan:
		// Got the code
	case err := <-errChan:
		server.Shutdown(ctx)
		return fmt.Errorf("authorization failed: %w", err)
	case <-ctx.Done():
		server.Shutdown(ctx)
		return ctx.Err()
	case <-time.After(5 * time.Minute):
		server.Shutdown(ctx)
		return fmt.Errorf("authorization timeout")
	}

	// Shutdown server
	server.Shutdown(ctx)

	// Exchange code for tokens
	return a.exchangeCodeForTokens(ctx, code, redirectURI)
}

// exchangeCodeForTokens exchanges the authorization code for tokens
func (a *Authenticator) exchangeCodeForTokens(ctx context.Context, code, redirectURI string) error {
	data := url.Values{}
	data.Set("client_id", a.config.ClientID)
	data.Set("client_secret", a.config.ClientSecret)
	data.Set("code", code)
	data.Set("grant_type", "authorization_code")
	data.Set("redirect_uri", redirectURI)

	req, err := http.NewRequestWithContext(ctx, "POST", TokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return fmt.Errorf("failed to create token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send token request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("token exchange failed with status: %d", resp.StatusCode)
	}

	var tokenResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		TokenType    string `json:"token_type"`
		Scope        string `json:"scope"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return fmt.Errorf("failed to decode token response: %w", err)
	}

	a.mu.Lock()
	a.credentials = &Credentials{
		AuthType:     "oauth",
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		TokenType:    tokenResp.TokenType,
		ExpiryDate:   time.Now().UnixMilli() + tokenResp.ExpiresIn*1000,
		Scope:        tokenResp.Scope,
		Type:         "iflow",
	}
	a.mu.Unlock()

	// Fetch user info and API key
	if err := a.fetchUserInfo(); err != nil {
		utils.L().Errorw("Failed to fetch user info",
			"provider", "iFlow",
			"error", err)
	}

	// Save credentials
	if err := a.saveCredentials(); err != nil {
		return fmt.Errorf("failed to save credentials: %w", err)
	}

	utils.L().Debugw("Authentication successful, credentials saved",
		"provider", "iFlow")
	return nil
}
