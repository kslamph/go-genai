package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"

	"github.com/sunbankio/omniproxy/internal/auth"
	"github.com/sunbankio/omniproxy/internal/config"
	"github.com/sunbankio/omniproxy/internal/manager"
)

func main() {
	ctx := context.Background()

	fmt.Println("=== Testing Kiro Provider Integration ===")

	// Create config
	cfg := &config.Config{
		Credentials: map[string][]string{
			"kiro": {"~/.aws/sso/cache/kiro-auth-token.json"},
		},
	}

	// Expand home directory
	homeDir, _ := os.UserHomeDir()
	cfg.Credentials["kiro"][0] = homeDir + "/.aws/sso/cache/kiro-auth-token.json"

	// Create registry
	registry := manager.NewRegistry()

	// Create auth manager
	authManager := auth.NewManager()

	// Create credential factory
	factory := manager.NewCredentialFactory()

	// Initialize credentials
	fmt.Println("\nInitializing Kiro credentials...")
	if err := factory.InitializeAllCredentials(ctx, cfg, registry, authManager); err != nil {
		log.Fatalf("Failed to initialize credentials: %v", err)
	}

	// Check if Kiro credentials were loaded
	fmt.Println("\nChecking for Kiro credentials...")
	models := registry.ListModels()
	if len(models) == 0 {
		fmt.Println("No models loaded. Make sure ~/.aws/sso/cache/kiro-auth-token.json exists.")
		return
	}

	// Check if any Kiro models are present
	kiroModels := []string{"claude-opus-4-5", "claude-haiku-4-5", "claude-sonnet-4-5"}
	hasKiro := false
	for _, model := range models {
		for _, kiroModel := range kiroModels {
			if model == kiroModel {
				hasKiro = true
				break
			}
		}
		if hasKiro {
			break
		}
	}

	if !hasKiro {
		fmt.Println("No Kiro models loaded.")
		return
	}

	fmt.Printf("✓ Kiro models are available in registry\n")

	// Get a credential for a Kiro model
	kiroCred := registry.GetCredential("claude-haiku-4-5")
	if kiroCred == nil {
		fmt.Println("No credential found for claude-haiku-4-5")
		return
	}

	cred, ok := kiroCred.(*auth.Credential)
	if !ok {
		fmt.Println("Credential is not of expected type")
		return
	}

	fmt.Printf("✓ Found Kiro credential: %s\n", cred.ID)
	fmt.Printf("  - Token length: %d\n", len(cred.AccessToken))
	fmt.Printf("  - Expiry: %s\n", cred.Expiry.Format("2006-01-02 15:04:05"))

	// Get models
	fmt.Println("\nGetting Kiro models...")
	models, err := cred.ListModels(ctx)
	if err != nil {
		log.Fatalf("Failed to get models: %v", err)
	}

	fmt.Printf("✓ Kiro supports %d models:\n", len(models))
	for _, model := range models {
		fmt.Printf("  - %s\n", model)
	}

	// Get models by provider from registry
	fmt.Println("\nGetting all models from registry...")
	modelsByProvider := registry.GetModelsByProvider()
	modelsJSON, _ := json.MarshalIndent(modelsByProvider, "", "  ")
	fmt.Printf("Registry models:\n%s\n", string(modelsJSON))

	// Test authenticator directly
	fmt.Println("\nTesting Kiro authenticator directly...")
	authInterface := cred.GetAuthenticator()
	if authInterface != nil {
		fmt.Printf("✓ Authenticator found: %T\n", authInterface)
	}

	// Test provider
	fmt.Println("\nTesting Kiro provider...")
	providerInstance := cred.GetProvider()
	if providerInstance != nil {
		fmt.Printf("✓ Provider found\n")
		fmt.Printf("  - Provider type: %s\n", providerInstance.Type())
		fmt.Printf("  - Provider name: %s\n", providerInstance.Name())
		fmt.Printf("  - Supported protocols: %v\n", providerInstance.SupportedProtocols())
	}

	fmt.Println("\n=== Integration Test Complete ===")
}