package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	cloudauth "cloud.google.com/go/auth"
	"github.com/sunbankio/omniproxy/auth"
	"google.golang.org/genai"
)

type geminiTokenProvider struct {
	authenticator *auth.GeminiAuthenticator
}

func (p *geminiTokenProvider) Token(ctx context.Context) (*cloudauth.Token, error) {
	token, err := p.authenticator.GetToken(ctx)
	if err != nil {
		return nil, err
	}
	return &cloudauth.Token{
		Value:  token,
		Expiry: time.Now().Add(time.Hour),
	}, nil
}

func discoverProjectID(ctx context.Context, authenticator *auth.GeminiAuthenticator, baseURL string) (string, error) {
	authenticator.ForceRefresh(ctx)
	token, err := authenticator.GetToken(ctx)
	if err != nil {
		return "", err
	}

	clientMetadata := map[string]interface{}{
		"ideType":    "IDE_UNSPECIFIED",
		"platform":   "PLATFORM_UNSPECIFIED",
		"pluginType": "GEMINI",
	}

	loadRequest := map[string]interface{}{
		"cloudaicompanionProject": "",
		"metadata":                clientMetadata,
	}

	reqBody, _ := json.Marshal(loadRequest)
	url := fmt.Sprintf("%s/v1internal:loadCodeAssist", baseURL)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(reqBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("loadCodeAssist failed (%d): %s", resp.StatusCode, string(body))
	}

	var loadResponse map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&loadResponse); err != nil {
		return "", err
	}

	if projectID, ok := loadResponse["cloudaicompanionProject"].(string); ok && projectID != "" {
		return projectID, nil
	}

	return "", fmt.Errorf("failed to discover project ID")
}

func main() {
	ctx := context.Background()

	// fmt.Println("=== Testing BackendGeminiCLI ===")
	// testGeminiCLI(ctx)

	fmt.Println("\n=== Testing BackendAntigravity ===")
	testAntigravity(ctx)
}

func testGeminiCLI(ctx context.Context) {
	authenticator := auth.NewGeminiAuthenticator(nil)

	if !authenticator.IsAuthenticated() {
		log.Println("Gemini CLI: Not authenticated. Skipping.")
		return
	}

	projectID, err := discoverProjectID(ctx, authenticator, "https://cloudcode-pa.googleapis.com")
	if err != nil {
		log.Fatalf("Project discovery failed: %v", err)
	}
	fmt.Printf("Discovered Project ID: %s\n", projectID)

	tokenProvider := &geminiTokenProvider{authenticator: authenticator}
	creds := cloudauth.NewCredentials(&cloudauth.CredentialsOptions{
		TokenProvider: tokenProvider,
	})

	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		Backend:     genai.BackendGeminiCLI,
		Project:     projectID,
		Credentials: creds,
	})
	if err != nil {
		log.Fatalf("Failed to create genai client: %v", err)
	}

	model := "gemini-2.5-flash"
	runTests(ctx, client, model)
}

func testAntigravity(ctx context.Context) {
	antigravityAuth := auth.NewGeminiAuthenticator(&auth.GeminiOAuthConfig{
		ClientID:     "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com",
		ClientSecret: "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",
		Scope:        "https://www.googleapis.com/auth/cloud-platform",
		RedirectPort: 8086,
		CredsDir:     "agent/AIClient-2-API/configs/antigravity",
		CredsFile:    "1766389593140_oauth_creds.json",
	})

	if !antigravityAuth.IsAuthenticated() {
		fmt.Println("Antigravity token potentially expired, trying to refresh...")
		if err := antigravityAuth.ForceRefresh(ctx); err != nil {
			log.Fatalf("Antigravity: Not authenticated and refresh failed: %v. Please run authentication flow first.", err)
		}
	}

	// For Antigravity, we can try to discover project ID too
	projectID, err := discoverProjectID(ctx, antigravityAuth, "https://daily-cloudcode-pa.sandbox.googleapis.com")
	if err != nil {
		log.Printf("Antigravity Project discovery failed: %v. Using fallback.", err)
		projectID = "antigravity-test-project"
	}
	fmt.Printf("Antigravity Project ID: %s\n", projectID)

	tokenProvider := &geminiTokenProvider{authenticator: antigravityAuth}
	creds := cloudauth.NewCredentials(&cloudauth.CredentialsOptions{
		TokenProvider: tokenProvider,
	})

	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		Backend:     genai.BackendAntigravity,
		Project:     projectID,
		Credentials: creds,
		HTTPOptions: genai.HTTPOptions{
			BaseURL: "https://daily-cloudcode-pa.sandbox.googleapis.com",
		},
	})
	if err != nil {
		log.Fatalf("Failed to create genai client for Antigravity: %v", err)
	}

	model := "claude-opus-4-5-thinking"
	runTests(ctx, client, model)
}

func runTests(ctx context.Context, client *genai.Client, model string) {
	fmt.Printf("Calling GenerateContent with model %s...\n", model)

	result, err := client.Models.GenerateContent(ctx, model, genai.Text("describe what model are you"), nil)
	if err != nil {
		log.Printf("GenerateContent failed: %v", err)
		return
	}

	if len(result.Candidates) > 0 && result.Candidates[0].Content != nil {
		fmt.Printf("Response: %s\n", result.Text())
	} else {
		fmt.Printf("Full Result: %+v\n", result)
	}

	fmt.Println("\nCalling GenerateContentStream...")
	iter := client.Models.GenerateContentStream(ctx, model, genai.Text("Write a short poem about the moon."), nil)
	fmt.Print("Streamed Response: ")
	for resp, err := range iter {
		if err != nil {
			log.Printf("Stream failed: %v", err)
			return
		}
		if len(resp.Candidates) > 0 && resp.Candidates[0].Content != nil {
			fmt.Print(resp.Text())
		}
	}
	fmt.Println("\nDone.")
}
