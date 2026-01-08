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
	UserAgent   string
}

func (t *TokenTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	token, err := t.TokenGetter.GetToken(req.Context())
	if err != nil {
		return nil, err
	}

	// Clone the request to avoid race conditions
	newReq := req.Clone(req.Context())
	newReq.Header.Set("Authorization", "Bearer "+token)
	if t.UserAgent != "" {
		newReq.Header.Set("User-Agent", t.UserAgent)
	}

	transport := t.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}

	return transport.RoundTrip(newReq)
}
