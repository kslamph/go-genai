package main

import (
	"context"
	"fmt"
	"log"
	"time"

	cloudauth "cloud.google.com/go/auth"
	"github.com/sunbankio/omniproxy/internal/provider/antigravity"
	"google.golang.org/genai"
)

type geminiTokenProvider struct {
	authenticator *antigravity.Authenticator
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

func main() {
	ctx := context.Background()

	// fmt.Println("=== Testing BackendGeminiCLI ===")
	// testGeminiCLI(ctx)

	fmt.Println("\n=== Testing BackendAntigravity ===")
	testAntigravity(ctx)
}

func testGeminiCLI(ctx context.Context) {
	authenticator := antigravity.NewAuthenticator(nil)

	if !authenticator.IsAuthenticated() {
		log.Println("Gemini CLI: Not authenticated. Skipping.")
		return
	}

	tokenProvider := &geminiTokenProvider{authenticator: authenticator}
	creds := cloudauth.NewCredentials(&cloudauth.CredentialsOptions{
		TokenProvider: tokenProvider,
	})

	// Use the library helper for Project Discovery
	projectID, err := genai.DiscoverCloudCodeProject(ctx, creds, genai.BackendGeminiCLI)
	if err != nil {
		log.Fatalf("Project discovery failed: %v", err)
	}
	fmt.Printf("Discovered Project ID: %s\n", projectID)

	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		Backend:     genai.BackendGeminiCLI,
		Project:     projectID,
		Credentials: creds,
	})
	if err != nil {
		log.Fatalf("Failed to create genai client: %v", err)
	}

	model := "gemini-2.0-flash"
	runTests(ctx, client, model)
}

func testAntigravity(ctx context.Context) {
	antigravityAuth := antigravity.NewAuthenticator(&antigravity.OAuthConfig{
		ClientID:     "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com",
		ClientSecret: "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",
		Scope:        "https://www.googleapis.com/auth/cloud-platform",
		RedirectPort: 8086,
		CredsDir:     ".antigravity",
		CredsFile:    "oauth_creds.json",
	})

	if !antigravityAuth.IsAuthenticated() {
		fmt.Println("Antigravity token potentially expired, trying to refresh...")
		if err := antigravityAuth.ForceRefresh(ctx); err != nil {
			log.Fatalf("Antigravity: Not authenticated and refresh failed: %v. Please run authentication flow first.", err)
		}
	}

	tokenProvider := &geminiTokenProvider{authenticator: antigravityAuth}
	creds := cloudauth.NewCredentials(&cloudauth.CredentialsOptions{
		TokenProvider: tokenProvider,
	})

	// Use the library helper for Project Discovery
	projectID, err := genai.DiscoverCloudCodeProject(ctx, creds, genai.BackendAntigravity)
	if err != nil {
		log.Printf("Antigravity Project discovery failed: %v. Using fallback.", err)
		projectID = "substantial-dragon-7kd70"
	}
	fmt.Printf("Antigravity Project ID: %s\n", projectID)

	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		Backend:     genai.BackendAntigravity,
		Project:     projectID,
		Credentials: creds,
		// HTTPOptions.BaseURL is now set automatically by the library
	})
	if err != nil {
		log.Fatalf("Failed to create genai client for Antigravity: %v", err)
	}

	model := "rev19-uic3-1p"
	runTests(ctx, client, model)
}

func runTests(ctx context.Context, client *genai.Client, model string) {
	fmt.Printf("Calling GenerateContent with model %s...\n", model)

	result, err := client.Models.GenerateContent(ctx, model, genai.Text("describe what model are you, who developed trained you, based on what model, and your version, your capability"), nil)
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
	iter := client.Models.GenerateContentStream(ctx, model, genai.Text("describe what model are you, who developed trained you, based on what model, and your version, your capability"), nil)
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
