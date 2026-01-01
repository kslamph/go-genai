package antigravity

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	cloudauth "cloud.google.com/go/auth"
	"github.com/gofrs/flock"
	"github.com/sunbankio/omniproxy/pkg/utils"
	"golang.org/x/oauth2"
)

const (
	TokenRefreshBufferMs = 1800 * 1000 // 30 minutes
)

// OAuthConfig holds the OAuth configuration for Antigravity
type OAuthConfig struct {
	ClientID     string
	ClientSecret string
	Scope        string
	RedirectPort int
	CredsDir     string
	CredsFile    string
}

// DefaultOAuthConfig returns the default Antigravity OAuth configuration
func DefaultOAuthConfig() *OAuthConfig {
	return &OAuthConfig{
		ClientID:     "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com",
		ClientSecret: "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",
		Scope:        "https://www.googleapis.com/auth/cloud-platform",
		RedirectPort: 8086,
		CredsDir:     ".antigravity",
		CredsFile:    "oauth_creds.json",
	}
}

// Credentials represents the stored OAuth credentials
type Credentials struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiryDate   int64  `json:"expiry_date"`
	Scope        string `json:"scope,omitempty"`
	ProjectID    string `json:"project_id,omitempty"` // Antigravity project ID
}

// Authenticator implements the Authenticator interface for Antigravity
type Authenticator struct {
	config      *OAuthConfig
	credentials *Credentials
	mu          sync.RWMutex
	httpClient  *http.Client
	isValid     bool // Tracks if the credentials are still valid (not revoked)
}

// NewAuthenticator creates a new Antigravity authenticator
func NewAuthenticator(config *OAuthConfig) *Authenticator {
	if config == nil {
		config = DefaultOAuthConfig()
	}
	return &Authenticator{
		config:     config,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		isValid:    true,
	}
}

// GetCredentialsPath returns the path to the credentials file
func (a *Authenticator) GetCredentialsPath() string {
	// If CredsDir is an absolute path, use it directly
	if filepath.IsAbs(a.config.CredsDir) {
		return filepath.Join(a.config.CredsDir, a.config.CredsFile)
	}
	// Otherwise, join with home directory
	homeDir, _ := os.UserHomeDir()
	return filepath.Join(homeDir, a.config.CredsDir, a.config.CredsFile)
}

// ensureLoaded loads credentials from file if not already in memory
func (a *Authenticator) ensureLoaded() error {
	if a.credentials != nil {
		return nil
	}
	creds, err := a.loadCredentials()
	if err != nil {
		return err
	}
	a.credentials = creds
	return nil
}

// IsAuthenticated checks if valid credentials exist
func (a *Authenticator) IsAuthenticated() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()

	// Check if the authenticator is marked as invalid (e.g., due to 401 on refresh)
	if !a.isValid {
		return false
	}

	if err := a.ensureLoaded(); err != nil {
		return false
	}

	// Check if token is still valid (with 30 minute buffer)
	return a.credentials != nil && IsTokenValid(*a.credentials)
}

// IsValid returns whether this authenticator is still valid (not revoked)
func (a *Authenticator) IsValid() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.isValid
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

	return &creds, nil
}

// saveCredentials saves credentials to file
func (a *Authenticator) saveCredentials(creds Credentials) error {
	credsPath := a.GetCredentialsPath()
	lockPath := credsPath + ".lock"

	// Create the directory if it doesn't exist
	dir := filepath.Dir(credsPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create credentials directory: %v", err)
	}

	// Use a file lock to prevent race conditions
	fileLock := flock.New(lockPath)
	locked, err := fileLock.TryLock()
	if err != nil {
		return fmt.Errorf("failed to acquire file lock: %v", err)
	}
	if !locked {
		return fmt.Errorf("failed to acquire file lock, another process is holding it")
	}
	defer fileLock.Unlock()

	// Create or overwrite the file
	file, err := os.Create(credsPath)
	if err != nil {
		return fmt.Errorf("failed to create credentials file: %v", err)
	}
	defer file.Close()

	// Encode and write the credentials
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(creds); err != nil {
		return fmt.Errorf("failed to encode credentials: %v", err)
	}

	return nil
}

// IsTokenValid checks if the token is still valid
func IsTokenValid(credentials Credentials) bool {
	if credentials.ExpiryDate == 0 {
		return false
	}
	// Add 30 second buffer. TokenRefreshBufferMs is defined in constants.go
	return time.Now().UnixMilli() < credentials.ExpiryDate-TokenRefreshBufferMs
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
			TokenURL: "https://oauth2.googleapis.com/token",
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
		// Check if this is a 401 error (invalid credentials)
		errStr := err.Error()
		if strings.Contains(errStr, "401") || strings.Contains(errStr, "unauthorized") {
			a.isValid = false
			utils.L().Errorw("Token refresh failed with 401 - credentials are invalid/revoked. Provider marked as invalid.",
				"provider", "Antigravity",
				"creds_path", a.GetCredentialsPath(),
				"error", err)
			return Credentials{}, fmt.Errorf("credentials are invalid (401). Please re-authenticate: %w", err)
		}
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

	if err := a.saveCredentials(updatedCredentials); err != nil {
		return Credentials{}, fmt.Errorf("failed to save updated credentials: %v", err)
	}

	return updatedCredentials, nil
}

// GetToken returns a valid access token, refreshing if necessary
func (a *Authenticator) GetToken(ctx context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	// Check if the authenticator is marked as invalid (e.g., due to 401 on refresh)
	if !a.isValid {
		return "", fmt.Errorf("provider credentials are invalid (revoked or expired). Please re-authenticate")
	}

	// Load credentials if not in memory
	if a.credentials == nil {
		creds, err := a.loadCredentials()
		if err != nil {
			return "", fmt.Errorf("credentials not found: %w", err)
		}
		a.credentials = creds
	}

	// Check if token is valid
	if IsTokenValid(*a.credentials) {
		return a.credentials.AccessToken, nil
	}

	// Token is invalid, try to refresh
	updatedCreds, err := a.refreshAccessToken(*a.credentials)
	if err != nil {
		utils.L().Errorw("Token refresh failed",
			"provider", "Antigravity",
			"creds_path", a.GetCredentialsPath(),
			"error", err)
		// Clear creds on failure so we retry/reload next time
		a.credentials = nil
		return "", fmt.Errorf("failed to refresh token: %w", err)
	}

	utils.L().Infow("Token refreshed successfully",
		"provider", "Antigravity",
		"creds_path", a.GetCredentialsPath())

	a.credentials = &updatedCreds
	return a.credentials.AccessToken, nil
}

// ForceRefresh forces a token refresh regardless of expiry
func (a *Authenticator) ForceRefresh(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.credentials == nil {
		creds, err := a.loadCredentials()
		if err != nil {
			return fmt.Errorf("credentials not found: %w", err)
		}
		a.credentials = creds
	}

	// Temporarily expire the token to force refresh
	originalExpiry := a.credentials.ExpiryDate
	a.credentials.ExpiryDate = 0

	updatedCreds, err := a.refreshAccessToken(*a.credentials)
	if err != nil {
		a.credentials.ExpiryDate = originalExpiry // Restore if failed
		return fmt.Errorf("failed to refresh token: %w", err)
	}

	utils.L().Infow("Token forced refresh successful",
		"provider", "Antigravity")

	a.credentials = &updatedCreds
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
		"https://accounts.google.com/o/oauth2/v2/auth?client_id=%s&redirect_uri=%s&response_type=code&scope=%s&access_type=offline&prompt=consent&state=%s",
		url.QueryEscape(a.config.ClientID),
		url.QueryEscape(redirectURI),
		url.QueryEscape(a.config.Scope),
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
	fmt.Printf("\n[Antigravity Auth] Please visit the following URL to authorize:\n\n%s\n\n", authURL)
	fmt.Println("[Antigravity Auth] Waiting for authorization...")

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

	req, err := http.NewRequestWithContext(ctx, "POST", "https://oauth2.googleapis.com/token", strings.NewReader(data.Encode()))
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
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		TokenType:    tokenResp.TokenType,
		ExpiryDate:   time.Now().Unix() + tokenResp.ExpiresIn,
		Scope:        tokenResp.Scope,
	}
	a.mu.Unlock()

	// Save credentials
	if err := a.saveCredentials(*a.credentials); err != nil {
		return fmt.Errorf("failed to save credentials: %w", err)
	}

	utils.L().Debugw("Authentication successful, credentials saved",
		"provider", "Antigravity")
	return nil
}

// GetProjectID returns the stored project ID for this credential
func (a *Authenticator) GetProjectID() string {
	a.mu.Lock()
	defer a.mu.Unlock()

	// Load credentials if not in memory
	if a.credentials == nil {
		creds, err := a.loadCredentials()
		if err != nil {
			return ""
		}
		a.credentials = creds
	}

	return a.credentials.ProjectID
}

// SetProjectID stores the project ID for this credential
func (a *Authenticator) SetProjectID(ctx context.Context, projectID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	// Load credentials if not in memory
	if a.credentials == nil {
		creds, err := a.loadCredentials()
		if err != nil {
			return fmt.Errorf("credentials not found: %w", err)
		}
		a.credentials = creds
	}

	// Update project ID
	a.credentials.ProjectID = projectID

	// Save credentials
	if err := a.saveCredentials(*a.credentials); err != nil {
		return fmt.Errorf("failed to save credentials with project ID: %w", err)
	}

	utils.L().Infow("Project ID saved to credentials",
		"provider", "Antigravity",
		"project_id", projectID)
	return nil
}

// GetExpiryDate returns the token expiry date from credentials
func (a *Authenticator) GetExpiryDate() int64 {
	a.mu.RLock()
	defer a.mu.RUnlock()

	// Load credentials if not in memory
	if a.credentials == nil {
		creds, err := a.loadCredentials()
		if err != nil {
			return 0
		}
		a.mu.RUnlock()
		a.mu.Lock()
		a.credentials = creds
		a.mu.Unlock()
		a.mu.RLock()
	}

	return a.credentials.ExpiryDate
}

// TokenProvider adapts Authenticator to cloudauth.TokenProvider
type TokenProvider struct {
	authenticator *Authenticator
}

// NewTokenProvider creates a new TokenProvider from an Authenticator
func NewTokenProvider(auth *Authenticator) *TokenProvider {
	return &TokenProvider{authenticator: auth}
}

func (p *TokenProvider) Token(ctx context.Context) (*cloudauth.Token, error) {
	token, err := p.authenticator.GetToken(ctx)
	if err != nil {
		return nil, err
	}

	// Get actual expiry from credentials
	p.authenticator.mu.RLock()
	expiry := time.Unix(p.authenticator.credentials.ExpiryDate, 0)
	p.authenticator.mu.RUnlock()

	return &cloudauth.Token{
		Value:  token,
		Expiry: expiry, // Use actual expiry instead of hardcoded 1 hour
	}, nil
}
