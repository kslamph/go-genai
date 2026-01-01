package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"cloud.google.com/go/auth"
	"golang.org/x/oauth2"
	"google.golang.org/genai"
)

// Gemini CLI Client ID and Secret (Publicly known for Cloud Code extension)
const (
	ClientID     = "681255809395-oo8ft2oprdrnp9e3aqf6av3hmdib135j.apps.googleusercontent.com"
	ClientSecret = "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"
	TokenURL     = "https://oauth2.googleapis.com/token"
)

// FileTokenProvider implements auth.TokenProvider by reading from a local JSON file.
type FileTokenProvider struct {
	Path string
}

// Credentials represents the JSON structure of the oauth_creds.json file.
type Credentials struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiryDate   int64  `json:"expiry_date"` // Unix timestamp in milliseconds
	TokenType    string `json:"token_type"`
}

func (p *FileTokenProvider) Token(ctx context.Context) (*auth.Token, error) {
	// 1. Read the credentials file

data, err := os.ReadFile(p.Path)
	if err != nil {
		return nil, fmt.Errorf("failed to read credentials file: %w", err)
	}

	var creds Credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, fmt.Errorf("failed to parse credentials JSON: %w", err)
	}

	// 2. Check if expired
	expiry := time.UnixMilli(creds.ExpiryDate)
	if time.Now().After(expiry.Add(-5 * time.Minute)) {
		log.Println("Token expired or expiring soon, refreshing...")
		newCreds, err := refreshAccessToken(ctx, creds)
		if err != nil {
			return nil, fmt.Errorf("failed to refresh token: %w", err)
		}
		creds = *newCreds
		// Save back to file
		if err := saveCredentials(p.Path, creds); err != nil {
			log.Printf("Warning: failed to save refreshed credentials: %v", err)
		}
		expiry = time.UnixMilli(creds.ExpiryDate)
	}

	return &auth.Token{
		Value:  creds.AccessToken,
		Type:   "Bearer",
		Expiry: expiry,
	}, nil
}

func refreshAccessToken(ctx context.Context, oldCreds Credentials) (*Credentials, error) {
	if oldCreds.RefreshToken == "" {
		return nil, fmt.Errorf("no refresh token available")
	}

	conf := &oauth2.Config{
		ClientID:     ClientID,
		ClientSecret: ClientSecret,
		Endpoint: oauth2.Endpoint{
			TokenURL: TokenURL,
		},
	}

	token := &oauth2.Token{
		RefreshToken: oldCreds.RefreshToken,
	}

	tokenSource := conf.TokenSource(ctx, token)
	newToken, err := tokenSource.Token()
	if err != nil {
		return nil, err
	}

	oldCreds.AccessToken = newToken.AccessToken
	oldCreds.RefreshToken = newToken.RefreshToken
	oldCreds.ExpiryDate = newToken.Expiry.UnixMilli()
	oldCreds.TokenType = newToken.TokenType

	return &oldCreds, nil
}

func saveCredentials(path string, creds Credentials) error {
	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

func main() {
	ctx := context.Background()

	// 1. Locate the credentials file
	homeDir, err := os.UserHomeDir()
	if err != nil {
		log.Fatalf("Failed to get user home directory: %v", err)
	}
	// Note: user specified path as "~/.gemini/oath_creds.json" or "oauth_creds.json"
	credsPath := filepath.Join(homeDir, ".gemini", "oauth_creds.json")
	if _, err := os.Stat(credsPath); os.IsNotExist(err) {
		// Try the "oath" variant if the "oauth" doesn't exist
		credsPath = filepath.Join(homeDir, ".gemini", "oath_creds.json")
	}

	// 2. Create the TokenProvider

tokenProvider := &FileTokenProvider{Path: credsPath}
	creds := auth.NewCredentials(&auth.CredentialsOptions{
		TokenProvider: tokenProvider,
	})

	// 3. Discover Project ID
	// Uses the shared helper function for Gemini CLI backend.
	projectID, err := genai.DiscoverCloudCodeProject(ctx, creds, genai.BackendGeminiCLI)
	if err != nil {
		log.Printf("Project discovery failed: %v", err)
		log.Println("Note: Gemini CLI typically requires a valid GCP Project ID.")
		projectID = "your-gcp-project-id" // Replace with your actual project ID if discovery fails
	}
	log.Printf("Using Project ID: %s", projectID)

	// 4. Initialize the GenAI Client
	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		Backend:     genai.BackendGeminiCLI,
		Project:     projectID,
		Credentials: creds,
	})
	if err != nil {
		log.Fatalf("Failed to create client: %v", err)
	}

	// 5. Generate Content
	model := "gemini-2.0-flash"
	prompt := "Write a haiku about a space explorer."

	log.Printf("Sending request to model: %s", model)
	resp, err := client.Models.GenerateContent(ctx, model, genai.Text(prompt), nil)
	if err != nil {
		log.Fatalf("GenerateContent failed: %v", err)
	}

	// 6. Print Response
	fmt.Printf("\nResponse:\n%s\n", resp.Text())
}
