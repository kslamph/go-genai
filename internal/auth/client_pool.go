package auth

import (
	"context"
	"sync"

	cloudauth "cloud.google.com/go/auth"
	"google.golang.org/genai"
)

// ClientPool manages a pool of genai.Client instances for safe token refresh
type ClientPool struct {
	mu      sync.RWMutex
	clients []*genai.Client
	current int
}

// NewClientPool creates a new empty client pool
func NewClientPool() *ClientPool {
	return &ClientPool{
		clients: make([]*genai.Client, 0),
		current: -1,
	}
}

// GetClient returns the current client safely
// Returns nil if no client is available
func (p *ClientPool) GetClient() *genai.Client {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if p.current < 0 || p.current >= len(p.clients) {
		return nil
	}

	return p.clients[p.current]
}

// RefreshClient creates a new genai.Client with the provided parameters
// and adds it to the pool, updating the current index
func (p *ClientPool) RefreshClient(
	ctx context.Context,
	tokenProvider cloudauth.TokenProvider,
	projectID string,
	backend genai.Backend,
	baseURL string,
) error {
	// Create credentials from token provider
	creds := cloudauth.NewCredentials(&cloudauth.CredentialsOptions{
		TokenProvider: tokenProvider,
	})

	// Prepare client configuration
	config := &genai.ClientConfig{
		Backend:     backend,
		Project:     projectID,
		Credentials: creds,
	}

	// Set base URL if provided
	if baseURL != "" {
		config.HTTPOptions = genai.HTTPOptions{
			BaseURL: baseURL,
		}
	}

	// Create new client
	client, err := genai.NewClient(ctx, config)
	if err != nil {
		return err
	}

	// Add to pool and update current index
	p.mu.Lock()
	p.clients = append(p.clients, client)
	p.current = len(p.clients) - 1
	p.mu.Unlock()

	return nil
}

// Cleanup removes old clients from the pool, keeping only the most recent one
func (p *ClientPool) Cleanup() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if len(p.clients) <= 1 {
		return
	}

	// Keep only the most recent client
	latest := p.clients[len(p.clients)-1]
	p.clients = []*genai.Client{latest}
	p.current = 0
}

// Size returns the number of clients in the pool
func (p *ClientPool) Size() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.clients)
}

// Reset clears all clients from the pool
func (p *ClientPool) Reset() {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Clear the pool
	p.clients = make([]*genai.Client, 0)
	p.current = -1
}
