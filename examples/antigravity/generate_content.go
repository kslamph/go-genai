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

// Antigravity Client ID and Secret (Publicly known for Cloud Code extension)
const (
	ClientID     = "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com"
	ClientSecret = "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"
	TokenURL     = "https://oauth2.googleapis.com/token"
)

// FileTokenProvider implements auth.TokenProvider by reading from a local JSON file.
type FileTokenProvider struct {
	Path string
}

// Credentials represents the JSON structure of the oauth_creds.json file.
// Adjust fields if your specific file format differs.
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
		// Save back to file (Optional but recommended)
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

	// Create an oauth2.Token to use the library's Refresh functionality
	token := &oauth2.Token{
		RefreshToken: oldCreds.RefreshToken,
		Expiry:       time.Now().Add(-1 * time.Hour), // Force expired to trigger logic if using TokenSource, but here we just want the source
	}

	tokenSource := conf.TokenSource(ctx, token)
	newToken, err := tokenSource.Token()
	if err != nil {
		return nil, err
	}

	// Update credentials struct
	oldCreds.AccessToken = newToken.AccessToken
	oldCreds.RefreshToken = newToken.RefreshToken // OAuth2 library handles rotation if a new one is returned
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
	credsPath := filepath.Join(homeDir, ".antigravity", "oauth_creds.json")

	// 2. Create the TokenProvider
	tokenProvider := &FileTokenProvider{Path: credsPath}
	creds := auth.NewCredentials(&auth.CredentialsOptions{
		TokenProvider: tokenProvider,
	})

	// 3. Discover Project ID
	// This uses the helper to fetch the dynamic "Cloud Companion" Project ID.
	projectID, err := genai.DiscoverCloudCodeProject(ctx, creds, genai.BackendAntigravity)
	if err != nil {
		log.Printf("Project discovery failed: %v", err)
		log.Println("Attempting fallback project ID...")
		// Fallback or exit depending on your needs.
		// Note: Antigravity requests often require the specific associated project ID.
		projectID = "antigravity-fallback-project"
	}
	log.Printf("Using Project ID: %s", projectID)

	// 4. Initialize the GenAI Client
	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		Backend:     genai.BackendAntigravity,
		Project:     projectID,
		Credentials: creds,
		// BaseURL and APIVersion are automatically set for BackendAntigravity
	})
	if err != nil {
		log.Fatalf("Failed to create client: %v", err)
	}

	// 5. Generate Content
	model := "gpt-oss-120b-medium"
	//["gemini-2.5-flash-lite",
	//  "gemini-3-pro-low",
	//  "gemini-3-pro-image",
	//  "rev19-uic3-1p",
	//  "chat_23310",
	//  "gpt-oss-120b-medium",
	//  "gemini-2.5-pro",
	//  "claude-opus-4-5-thinking",
	//  "gemini-3-flash",
	//  "gemini-3-pro-high",
	//  "claude-sonnet-4-5-thinking",
	//  "gemini-2.5-flash",
	//  "gemini-2.5-flash-thinking",
	//  "claude-sonnet-4-5",
	//  "chat_20706"]
	prompt := "Explain the concept of 'antigravity' in 3 sentences."

	log.Printf("Sending request to model: %s", model)
	resp, err := client.Models.GenerateContent(ctx, model, genai.Text(prompt), nil)
	if err != nil {
		log.Fatalf("GenerateContent failed: %v", err)
	}

	// 6. Print Response
	if len(resp.Candidates) > 0 {
		for _, part := range resp.Candidates[0].Content.Parts {
			if part.Text != "" {
				fmt.Printf("\nResponse:\n%s\n", part.Text)
			}
		}
	} else {
		fmt.Println("No content returned.")
	}
}
