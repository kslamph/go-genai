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
	"github.com/sunbankio/omniproxy/internal/provider/iflow"
	"github.com/sunbankio/omniproxy/internal/provider/qwen"
)

func main() {
	provider := flag.String("provider", "", "Provider to authenticate with (gemini, antigravity, iflow, or qwen)")
	reauthPath := flag.String("reauth", "", "Path to existing credential file to re-authenticate")
	flag.Parse()

	if *provider == "" {
		fmt.Fprintf(os.Stderr, "Error: -provider flag is required\n")
		fmt.Fprintf(os.Stderr, "Usage: newprofile -provider <gemini|antigravity|iflow|qwen> [-reauth <path>]\n")
		os.Exit(1)
	}

	if *provider != "gemini" && *provider != "antigravity" && *provider != "iflow" && *provider != "qwen" {
		fmt.Fprintf(os.Stderr, "Error: unsupported provider '%s'. Supported providers: gemini, antigravity, iflow, qwen\n", *provider)
		os.Exit(1)
	}

	var credsPath string
	if *reauthPath != "" {
		// Use existing file for re-authentication
		credsPath = *reauthPath
		if _, err := os.Stat(credsPath); os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "Error: file for re-auth does not exist: %s\n", credsPath)
			os.Exit(1)
		}
		fmt.Printf("Starting re-authentication for %s...\n", *provider)
		fmt.Printf("Credentials will be updated at: %s\n\n", credsPath)
	} else {
		// Create new credential file
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
		credsPath = filepath.Join(credsDir, credsFileName)

		fmt.Printf("Starting OAuth flow for %s...\n", *provider)
		fmt.Printf("Credentials will be saved to: %s\n\n", credsPath)
	}

	ctx := context.Background()

	var authErr error

	switch *provider {
	case "gemini":
		authErr = authenticateGemini(ctx, credsPath)
	case "antigravity":
		authErr = authenticateAntigravity(ctx, credsPath)
	case "iflow":
		authErr = authenticateIFlow(ctx, credsPath)
	case "qwen":
		authErr = authenticateQwen(ctx, credsPath)
	}

	if authErr != nil {
		fmt.Fprintf(os.Stderr, "Error: authentication failed: %v\n", authErr)
		os.Exit(1)
	}

	fmt.Printf("\n✓ Authentication successful!\n")
	fmt.Printf("✓ Credentials saved to: %s\n", credsPath)

	if *reauthPath == "" {
		// Load config and add the new credential only if it's a new profile
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
	} else {
		fmt.Printf("\nYour profile has been re-authenticated and is ready to use!\n")
	}
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

func authenticateIFlow(ctx context.Context, credsPath string) error {
	config := iflow.DefaultOAuthConfig()
	config.CredsPath = credsPath

	authenticator := iflow.NewAuthenticator(config)
	return authenticator.Authenticate(ctx)
}

func authenticateQwen(ctx context.Context, credsPath string) error {
	config := qwen.DefaultOAuthConfig()
	config.CredsPath = credsPath

	authenticator := qwen.NewAuthenticator(config)
	return authenticator.Authenticate(ctx)
}
