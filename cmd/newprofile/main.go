package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sunbankio/omniproxy/internal/config"
	"github.com/sunbankio/omniproxy/internal/provider/antigravity"
	"github.com/sunbankio/omniproxy/internal/provider/gemini"
)

func main() {
	provider := flag.String("provider", "", "Provider to authenticate with (gemini or antigravity)")
	flag.Parse()

	if *provider == "" {
		fmt.Fprintf(os.Stderr, "Error: -provider flag is required\n")
		fmt.Fprintf(os.Stderr, "Usage: newprofile -provider <gemini|antigravity>\n")
		os.Exit(1)
	}

	if *provider != "gemini" && *provider != "antigravity" {
		fmt.Fprintf(os.Stderr, "Error: unsupported provider '%s'. Supported providers: gemini, antigravity\n", *provider)
		os.Exit(1)
	}

	ctx := context.Background()

	// Create credentials directory
	homeDir, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to get home directory: %v\n", err)
		os.Exit(1)
	}

	credsDir := filepath.Join(homeDir, ".omniproxy", "credential")
	if err := os.MkdirAll(credsDir, 0700); err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to create credentials directory: %v\n", err)
		os.Exit(1)
	}

	// Generate credential file name with timestamp
	timestamp := time.Now().Unix()
	credsFileName := fmt.Sprintf("%s-cli_%d_oauth_creds.json", *provider, timestamp)
	credsPath := filepath.Join(credsDir, credsFileName)

	fmt.Printf("Starting OAuth flow for %s...\n", *provider)
	fmt.Printf("Credentials will be saved to: %s\n\n", credsPath)

	var authErr error

	switch *provider {
	case "gemini":
		authErr = authenticateGemini(ctx, credsPath)
	case "antigravity":
		authErr = authenticateAntigravity(ctx, credsPath)
	}

	if authErr != nil {
		fmt.Fprintf(os.Stderr, "Error: authentication failed: %v\n", authErr)
		os.Exit(1)
	}

	fmt.Printf("\n✓ Authentication successful!\n")
	fmt.Printf("✓ Credentials saved to: %s\n", credsPath)

	// Load config and add the new credential
	configPath := config.GetDefaultConfigPath()
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to load config: %v\n", err)
		fmt.Printf("\nYou can manually add this credential to your omniproxy.yaml configuration.\n")
		return
	}

	// Add the credential to the config
	config.AddCredential(cfg, *provider, credsPath)

	// Save the updated config
	if err := config.SaveConfig(configPath, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to save config: %v\n", err)
		fmt.Printf("\nYou can manually add this credential to your omniproxy.yaml configuration.\n")
		return
	}

	fmt.Printf("✓ Added credential to config: %s\n", configPath)
	fmt.Printf("\nYour new profile is ready to use!\n")
}

func authenticateGemini(ctx context.Context, credsPath string) error {
	config := gemini.DefaultOAuthConfig()
	config.CredsPath = credsPath

	authenticator := gemini.NewAuthenticator(config)
	return authenticator.Authenticate(ctx)
}

func authenticateAntigravity(ctx context.Context, credsPath string) error {
	config := antigravity.DefaultOAuthConfig()
	config.CredsDir = filepath.Dir(credsPath)
	config.CredsFile = filepath.Base(credsPath)

	authenticator := antigravity.NewAuthenticator(config)
	return authenticator.Authenticate(ctx)
}
