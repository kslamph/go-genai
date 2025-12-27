package common

import (
	"context"
	"net/http"
)

type TokenGetter interface {
	GetToken(ctx context.Context) (string, error)
}

type TokenTransport struct {
	Transport   http.RoundTripper
	TokenGetter TokenGetter
}

func (t *TokenTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	token, err := t.TokenGetter.GetToken(req.Context())
	if err != nil {
		return nil, err
	}

	// Clone the request to avoid race conditions
	newReq := req.Clone(req.Context())
	newReq.Header.Set("Authorization", "Bearer "+token)
	newReq.Header.Set("User-Agent", "iflow-cli/0.4.11")

	transport := t.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}

	return transport.RoundTrip(newReq)
}
