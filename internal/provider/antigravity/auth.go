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
	"golang.org/x/oauth2"
	"go.uber.org/zap"
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
	CredsPath    string
}

// DefaultOAuthConfig returns the default Antigravity OAuth configuration
func DefaultOAuthConfig() *OAuthConfig {
	homeDir, _ := os.UserHomeDir()
	return &OAuthConfig{
		ClientID:     "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com",
		ClientSecret: "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",
		Scope:        "https://www.googleapis.com/auth/cloud-platform",
		RedirectPort: 8086,
		CredsPath:    filepath.Join(homeDir, ".antigravity", "oauth_creds.json"),
	}
}

// Credentials represents the stored OAuth credentials
type Credentials struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiryDate   int64  `json:"expiry_date"`
	Scope        string `json:"scope,omitempty"`
}

// Authenticator implements the Authenticator interface for Antigravity
type Authenticator struct {
	config      *OAuthConfig
	credentials *Credentials
	mu          sync.RWMutex
	logger      *zap.SugaredLogger
	httpClient  *http.Client
}

// NewAuthenticator creates a new Antigravity authenticator
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

// TokenProvider adapts Authenticator to cloudauth.TokenProvider
type TokenProvider struct {
	authenticator *Authenticator
}

func (p *TokenProvider) Token(ctx context.Context) (*cloudauth.Token, error) {
	token, err := p.authenticator.GetToken(ctx)
	if err != nil {
		return nil, err
	}
	return &cloudauth.Token{
		Value:  token,
		Expiry: time.Now().Add(time.Hour),
	}, nil
}

func (a *Authenticator) GetCredentialsPath() string {
	return a.config.CredsPath
}

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

	buffer := time.Duration(TokenRefreshBufferMs) * time.Millisecond
	return a.credentials != nil && time.Unix(a.credentials.ExpiryDate, 0).After(time.Now().Add(buffer))
}

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

func (a *Authenticator) saveCredentials(creds *Credentials) error {
	credsPath := a.GetCredentialsPath()
	dir := filepath.Dir(credsPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create credentials directory: %w", err)
	}

	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal credentials: %w", err)
	}

	if err := os.WriteFile(credsPath, data, 0600); err != nil {
		return fmt.Errorf("failed to write credentials file: %w", err)
	}

	return nil
}

func (a *Authenticator) GetToken(ctx context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.credentials == nil {
		creds, err := a.loadCredentials()
		if err != nil {
			return "", fmt.Errorf("credentials not found: %w", err)
		}
		a.credentials = creds
	}

	token := &oauth2.Token{
		AccessToken:  a.credentials.AccessToken,
		RefreshToken: a.credentials.RefreshToken,
		TokenType:    a.credentials.TokenType,
		Expiry:       time.Unix(a.credentials.ExpiryDate, 0),
	}

	buffer := time.Duration(TokenRefreshBufferMs) * time.Millisecond
	conf := &oauth2.Config{
		ClientID:     a.config.ClientID,
		ClientSecret: a.config.ClientSecret,
		Scopes:       []string{a.config.Scope},
		Endpoint: oauth2.Endpoint{
			AuthURL:  "https://accounts.google.com/o/oauth2/v2/auth",
			TokenURL: "https://oauth2.googleapis.com/token",
		},
	}
	ctx = context.WithValue(ctx, oauth2.HTTPClient, a.httpClient)

	if time.Until(token.Expiry) < buffer {
		token.Expiry = time.Now().Add(-1 * time.Second)
	}

	ts := conf.TokenSource(ctx, token)
	newToken, err := ts.Token()
	if err != nil {
		a.credentials = nil
		return "", fmt.Errorf("failed to refresh token: %w", err)
	}

	if newToken.AccessToken != a.credentials.AccessToken || newToken.RefreshToken != a.credentials.RefreshToken {
		a.credentials.AccessToken = newToken.AccessToken
		a.credentials.RefreshToken = newToken.RefreshToken
		a.credentials.TokenType = newToken.TokenType
		a.credentials.ExpiryDate = newToken.Expiry.Unix()
		if extraScope, ok := newToken.Extra("scope").(string); ok && extraScope != "" {
			a.credentials.Scope = extraScope
		}
		if err := a.saveCredentials(a.credentials); err != nil {
			a.logger.Errorw("Failed to save credentials", "error", err)
		}
	}

	return a.credentials.AccessToken, nil
}

// ForceRefresh is same as Gemini
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

	token := &oauth2.Token{
		AccessToken:  a.credentials.AccessToken,
		RefreshToken: a.credentials.RefreshToken,
		TokenType:    a.credentials.TokenType,
		Expiry:       time.Now().Add(-1 * time.Second),
	}

	conf := &oauth2.Config{
		ClientID:     a.config.ClientID,
		ClientSecret: a.config.ClientSecret,
		Scopes:       []string{a.config.Scope},
		Endpoint: oauth2.Endpoint{
			AuthURL:  "https://accounts.google.com/o/oauth2/v2/auth",
			TokenURL: "https://oauth2.googleapis.com/token",
		},
	}
	ctx = context.WithValue(ctx, oauth2.HTTPClient, a.httpClient)
	ts := conf.TokenSource(ctx, token)
	newToken, err := ts.Token()

	if err != nil {
		return fmt.Errorf("failed to refresh token: %w", err)
	}

	a.credentials.AccessToken = newToken.AccessToken
	a.credentials.RefreshToken = newToken.RefreshToken
	a.credentials.TokenType = newToken.TokenType
	a.credentials.ExpiryDate = newToken.Expiry.Unix()

	if err := a.saveCredentials(a.credentials); err != nil {
		a.logger.Errorw("Failed to save credentials", "error", err)
	}
	return nil
}

// Authenticate is same logic
func (a *Authenticator) Authenticate(ctx context.Context) error {
	stateBytes := make([]byte, 16)
	if _, err := rand.Read(stateBytes); err != nil {
		return err
	}
	state := base64.URLEncoding.EncodeToString(stateBytes)
	redirectURI := fmt.Sprintf("http://localhost:%d", a.config.RedirectPort)

	authURL := fmt.Sprintf(
		"https://accounts.google.com/o/oauth2/v2/auth?client_id=%s&redirect_uri=%s&response_type=code&scope=%s&access_type=offline&prompt=consent&state=%s",
		url.QueryEscape(a.config.ClientID),
		url.QueryEscape(redirectURI),
		url.QueryEscape(a.config.Scope),
		url.QueryEscape(state),
	)

	codeChan := make(chan string, 1)
	errChan := make(chan error, 1)
	server := &http.Server{Addr: fmt.Sprintf(":%d", a.config.RedirectPort)}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("state") != state {
			errChan <- fmt.Errorf("state mismatch")
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" {
			errChan <- fmt.Errorf("no code")
			return
		}
		_, _ = w.Write([]byte("Authorized."))
		codeChan <- code
	})

	go func() {
		if err := server.ListenAndServe(); err != http.ErrServerClosed {
			errChan <- err
		}
	}()

	fmt.Printf("\n[Antigravity Auth] URL: %s\n", authURL)

	var code string
	select {
	case code = <-codeChan:
	case err := <-errChan:
		_ = server.Shutdown(ctx)
		return err
	case <-ctx.Done():
		_ = server.Shutdown(ctx)
		return ctx.Err()
	}
	_ = server.Shutdown(ctx)

	return a.exchangeCodeForTokens(ctx, code, redirectURI)
}

func (a *Authenticator) exchangeCodeForTokens(ctx context.Context, code, redirectURI string) error {
	data := url.Values{}
	data.Set("client_id", a.config.ClientID)
	data.Set("client_secret", a.config.ClientSecret)
	data.Set("code", code)
	data.Set("grant_type", "authorization_code")
	data.Set("redirect_uri", redirectURI)

	req, err := http.NewRequestWithContext(ctx, "POST", "https://oauth2.googleapis.com/token", strings.NewReader(data.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status: %d", resp.StatusCode)
	}

	var tokenResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		TokenType    string `json:"token_type"`
		Scope        string `json:"scope"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return err
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

	return a.saveCredentials(a.credentials)
}
