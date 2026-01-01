package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"time"

	"cloud.google.com/go/auth"
	"google.golang.org/genai"
)

type MockTokenProvider struct {
	accessToken string
}

func (m *MockTokenProvider) Token(ctx context.Context) (*auth.Token, error) {
	// Return a token valid for 1 hour
	return &auth.Token{
		Value:  m.accessToken,
		Expiry: time.Now().Add(1 * time.Hour),
	}, nil
}

func main() {
	// 1. Start a mock server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		fmt.Printf("Server received Authorization: %s\n", auth)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`))
	}))
	defer server.Close()

	fmt.Printf("Mock server listening on %s\n", server.URL)

	// 2. Create Token Provider
	tp := &MockTokenProvider{accessToken: "TOKEN_1"}

	// 3. Create genai.Client
	ctx := context.Background()
	
	// Create credentials using our provider
	creds := auth.NewCredentials(&auth.CredentialsOptions{
		TokenProvider: tp,
	})

	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		Backend:     genai.BackendVertexAI,
		Project:     "test-project",
		Location:    "us-central1",
		Credentials: creds,
		HTTPOptions: genai.HTTPOptions{
			BaseURL: server.URL + "/",
		},
	})
	if err != nil {
		log.Fatalf("Failed to create client: %v", err)
	}

	// 4. Make request 1
	fmt.Println("---", "Request 1 (Initial) --- ")
	client.Models.List(ctx, nil)

	// 5. Update Token Provider
	fmt.Println("---", "Updating token provider to TOKEN_2 ---")
	tp.accessToken = "TOKEN_2"

	// 6. Make request 2 (Should still use TOKEN_1 if cached)
	fmt.Println("---", "Request 2 (Same Client) ---")
	client.Models.List(ctx, nil)
	
	// 7. Recreate Client with SAME creds
	fmt.Println("---", "Recreating client (Same Creds object) ---")
	client2, err := genai.NewClient(ctx, &genai.ClientConfig{
		Backend:     genai.BackendVertexAI,
		Project:     "test-project",
		Location:    "us-central1",
		Credentials: creds, // Same credentials object
		HTTPOptions: genai.HTTPOptions{
			BaseURL: server.URL + "/",
		},
	})
	if err != nil {
		log.Fatalf("Failed to create client 2: %v", err)
	}

	// 8. Make request 3
	fmt.Println("---", "Request 3 (New Client, Same Creds) ---")
	client2.Models.List(ctx, nil)

	// 9. Recreate Client with NEW creds
	fmt.Println("---", "Recreating client (New Creds object) ---")
	creds2 := auth.NewCredentials(&auth.CredentialsOptions{
		TokenProvider: tp, // Same provider instance (which now returns TOKEN_2)
	})
	client3, err := genai.NewClient(ctx, &genai.ClientConfig{
		Backend:     genai.BackendVertexAI,
		Project:     "test-project",
		Location:    "us-central1",
		Credentials: creds2,
		HTTPOptions: genai.HTTPOptions{
			BaseURL: server.URL + "/",
		},
	})
	if err != nil {
		log.Fatalf("Failed to create client 3: %v", err)
	}

	// 10. Make request 4
	fmt.Println("---", "Request 4 (New Client, New Creds) ---")
	client3.Models.List(ctx, nil)
}
