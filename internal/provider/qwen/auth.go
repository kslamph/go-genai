package qwen

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/gofrs/flock"
	"golang.org/x/oauth2"
)

// Qwen OAuth constants
const (
	DefaultBaseURL       = "https://portal.qwen.ai/v1"
	TokenRefreshBufferMs = 1800 * 1000 // 30 minutes
	OAuthTokenURL        = "https://chat.qwen.ai/api/v1/oauth2/token"
	OAuthClientID        = "f0304373b74a44d2b584a3fb70ca9e56"
	OAuthScope           = "openid profile email model.completion"
	OAuthDeviceAuthURL   = "https://chat.qwen.ai/api/v1/oauth2/device/code"
)

// OAuthConfig holds the OAuth configuration for Qwen
type OAuthConfig struct {
	ClientID      string
	Scope         string
	TokenURL      string
	DeviceAuthURL string
	CredsPath     string
}

// DefaultOAuthConfig returns the default Qwen OAuth configuration
func DefaultOAuthConfig() *OAuthConfig {
	homeDir, _ := os.UserHomeDir()
	return &OAuthConfig{
		ClientID:      OAuthClientID,
		Scope:         OAuthScope,
		TokenURL:      OAuthTokenURL,
		DeviceAuthURL: OAuthDeviceAuthURL,
		CredsPath:     filepath.Join(homeDir, ".qwen", "qwenproxy_creds.json"),
	}
}

// Credentials represents the stored OAuth credentials for Qwen
type Credentials struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	RefreshToken string `json:"refresh_token"`
	ResourceURL  string `json:"resource_url"`
	ExpiryDate   int64  `json:"expiry_date"`
}

// Authenticator implements the Authenticator interface for Qwen
type Authenticator struct {
	config      *OAuthConfig
	credentials *Credentials
	mu          sync.RWMutex

	httpClient *http.Client
}

// NewAuthenticator creates a new Qwen authenticator
func NewAuthenticator(config *OAuthConfig) *Authenticator {
	if config == nil {
		config = DefaultOAuthConfig()
	}
	return &Authenticator{
		config: config,

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
		// Try to load from file
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

	// Check if token is still valid (with 30 minute buffer)
	return IsTokenValid(*a.credentials)
}

// loadCredentials loads credentials from file
func (a *Authenticator) loadCredentials() (*Credentials, error) {
	credsPath := a.GetCredentialsPath()
	file, err := os.Open(credsPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open credentials file: %v", err)
	}
	defer file.Close()

	var creds Credentials
	decoder := json.NewDecoder(file)
	if err := decoder.Decode(&creds); err != nil {
		return nil, fmt.Errorf("failed to decode credentials file: %v", err)
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

	// Check if token is valid
	if IsTokenValid(*a.credentials) {
		return a.credentials.AccessToken, nil
	}

	// Token is invalid, try to refresh
	updatedCreds, err := a.refreshAccessToken(*a.credentials)
	if err != nil {
		return "", fmt.Errorf("failed to refresh token: %w", err)
	}

	a.credentials = &updatedCreds
	return a.credentials.AccessToken, nil
}

// refreshAccessToken refreshes the OAuth token using the refresh token
func (a *Authenticator) refreshAccessToken(credentials Credentials) (Credentials, error) {
	if credentials.RefreshToken == "" {
		return Credentials{}, fmt.Errorf("no refresh token available")
	}

	conf := &oauth2.Config{
		ClientID: a.config.ClientID,
		Endpoint: oauth2.Endpoint{
			TokenURL: a.config.TokenURL,
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

	if resourceURL, ok := newToken.Extra("resource_url").(string); ok {
		updatedCredentials.ResourceURL = resourceURL
	}

	if err := a.saveCredentials(updatedCredentials); err != nil {
		return Credentials{}, fmt.Errorf("failed to save updated credentials: %v", err)
	}

	return updatedCredentials, nil
}

// IsTokenValid checks if the token is still valid
func IsTokenValid(credentials Credentials) bool {
	if credentials.ExpiryDate == 0 {
		return false
	}
	// Add 30 second buffer. TokenRefreshBufferMs is defined in constants.go
	return time.Now().UnixMilli() < credentials.ExpiryDate-TokenRefreshBufferMs
}

// Authenticate performs the OAuth device authorization flow
func (a *Authenticator) Authenticate(ctx context.Context) error {
	return a.authenticateWithDeviceFlow()
}

// authenticateWithDeviceFlow handles the OAuth 2.0 device authorization flow using the golang.org/x/oauth2 package.
func (a *Authenticator) authenticateWithDeviceFlow() error {
	conf := &oauth2.Config{
		ClientID: a.config.ClientID,
		Scopes:   []string{a.config.Scope},
		Endpoint: oauth2.Endpoint{
			TokenURL:      a.config.TokenURL,
			DeviceAuthURL: a.config.DeviceAuthURL,
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	codeVerifier, err := a.generateCodeVerifier()
	if err != nil {
		return fmt.Errorf("failed to generate code verifier: %w", err)
	}
	codeChallenge := a.generateCodeChallenge(codeVerifier)

	deviceAuthResponse, err := conf.DeviceAuth(ctx,
		oauth2.SetAuthURLParam("code_challenge", codeChallenge),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
	)
	if err != nil {
		return fmt.Errorf("failed to start device auth flow: %w", err)
	}

	// Construct verification URL with user code and client parameter
	// Use "qwen-code" as the client parameter value
	var verificationURL string
	if deviceAuthResponse.VerificationURIComplete != "" {
		verificationURL = deviceAuthResponse.VerificationURIComplete
	} else {
		verificationURL = fmt.Sprintf("%s?user_code=%s&client=qwen-code", deviceAuthResponse.VerificationURI, deviceAuthResponse.UserCode)
	}

	// Try to open the verification URI in the browser
	if err := a.openBrowser(verificationURL); err != nil {
		fmt.Printf("Warning: failed to open browser automatically: %v\n", err)
	}

	fmt.Printf("\n=== Qwen OAuth Authentication ===\n")
	fmt.Printf("If your browser didn't open, please go to: %s\n", verificationURL)
	fmt.Printf("And enter this code: %s\n\n", deviceAuthResponse.UserCode)
	fmt.Println("Waiting for authorization...")

	token, err := conf.DeviceAccessToken(ctx, deviceAuthResponse, oauth2.SetAuthURLParam("code_verifier", codeVerifier))
	if err != nil {
		return fmt.Errorf("failed to get token: %w", err)
	}

	creds := Credentials{
		AccessToken:  token.AccessToken,
		TokenType:    token.TokenType,
		RefreshToken: token.RefreshToken,
		ExpiryDate:   token.Expiry.UnixMilli(),
	}
	if resourceURL, ok := token.Extra("resource_url").(string); ok {
		creds.ResourceURL = resourceURL
	}

	a.mu.Lock()
	a.credentials = &creds
	a.mu.Unlock()

	if err := a.saveCredentials(creds); err != nil {
		return fmt.Errorf("failed to save credentials: %w", err)
	}

	fmt.Println("Authentication successful! Credentials saved.")
	return nil
}

// openBrowser opens the default browser with the given URL.
func (a *Authenticator) openBrowser(url string) error {
	var err error
	switch runtime.GOOS {
	case "linux":
		err = exec.Command("xdg-open", url).Start()
	case "windows":
		err = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		err = exec.Command("open", url).Start()
	default:
		err = fmt.Errorf("unsupported platform")
	}
	return err
}

// generateCodeVerifier generates a random code verifier for PKCE.
func (a *Authenticator) generateCodeVerifier() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// generateCodeChallenge generates a code challenge from a code verifier using SHA-256.
func (a *Authenticator) generateCodeChallenge(codeVerifier string) string {
	h := sha256.New()
	h.Write([]byte(codeVerifier))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}
