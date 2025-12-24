package qwen

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gofrs/flock"
	"golang.org/x/oauth2"
	"go.uber.org/zap"
)

// Qwen OAuth constants
const (
	DefaultBaseURL     = "https://portal.qwen.ai/v1"
	TokenRefreshBufferMs = 1800 * 1000 // 30 minutes
	OAuthTokenURL      = "https://chat.qwen.ai/api/v1/oauth2/token"
	OAuthClientID      = "f0304373b74a44d2b584a3fb70ca9e56"
	OAuthScope         = "openid profile email model.completion"
	OAuthDeviceAuthURL = "https://chat.qwen.ai/api/v1/oauth2/device/code"
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
	logger      *zap.SugaredLogger
	httpClient  *http.Client
}

// NewAuthenticator creates a new Qwen authenticator
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
