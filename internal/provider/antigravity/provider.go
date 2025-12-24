package antigravity

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	cloudauth "cloud.google.com/go/auth"
	"github.com/sashabaranov/go-openai"
	"github.com/sunbankio/omniproxy/internal/provider"
	"github.com/sunbankio/omniproxy/internal/provider/gemini"
	"google.golang.org/genai"
)

const (
	AntigravityBaseURL = "https://daily-cloudcode-pa.sandbox.googleapis.com"
)

type AntigravityProvider struct {
	client *genai.Client
	name   string
	auth   *Authenticator
}

// Ensure AntigravityProvider implements provider.Provider
var _ provider.Provider = (*AntigravityProvider)(nil)

func NewProvider(ctx context.Context, name string, auth *Authenticator) (*AntigravityProvider, error) {
	// 1. Discover Project ID
	projectID, err := discoverProjectID(ctx, auth, AntigravityBaseURL)
	if err != nil {
		// Fallback as seen in POC
		projectID = "antigravity-test-project"
	}

	// 2. Create GenAI Client
	tokenProvider := &TokenProvider{authenticator: auth}
	creds := cloudauth.NewCredentials(&cloudauth.CredentialsOptions{
		TokenProvider: tokenProvider,
	})

	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		Backend:     genai.BackendAntigravity,
		Project:     projectID,
		Credentials: creds,
		HTTPOptions: genai.HTTPOptions{
			BaseURL: AntigravityBaseURL,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create genai client: %w", err)
	}

	return &AntigravityProvider{
		client: client,
		name:   name,
		auth:   auth,
	}, nil
}

func (p *AntigravityProvider) Type() string {
	return "antigravity"
}

func (p *AntigravityProvider) Name() string {
	return p.name
}

func (p *AntigravityProvider) ChatCompletion(ctx context.Context, req openai.ChatCompletionRequest) (*openai.ChatCompletionResponse, error) {
	sys, contents, config, err := gemini.ToGeminiRequest(req)
	if err != nil {
		return nil, err
	}

	if sys != nil {
		config.SystemInstruction = sys
	}

	resp, err := p.client.Models.GenerateContent(ctx, req.Model, contents, config)
	if err != nil {
		return nil, err
	}

	return gemini.FromGeminiResponse(resp, req.Model), nil
}

func (p *AntigravityProvider) StreamChatCompletion(ctx context.Context, req openai.ChatCompletionRequest) (<-chan openai.ChatCompletionStreamResponse, <-chan error) {
	respChan := make(chan openai.ChatCompletionStreamResponse)
	errChan := make(chan error, 1)

	sys, contents, config, err := gemini.ToGeminiRequest(req)
	if err != nil {
		errChan <- err
		close(respChan)
		close(errChan)
		return respChan, errChan
	}

	if sys != nil {
		config.SystemInstruction = sys
	}

	go func() {
		defer close(respChan)
		defer close(errChan)

		iter := p.client.Models.GenerateContentStream(ctx, req.Model, contents, config)
		for resp, err := range iter {
			if err != nil {
				errChan <- err
				return
			}

			chunk := gemini.FromGeminiChunk(resp, req.Model)
			respChan <- *chunk
		}
	}()

	return respChan, errChan
}

// discoverProjectID helps find the project ID needed for API
func discoverProjectID(ctx context.Context, authenticator *Authenticator, baseURL string) (string, error) {
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
	defer func() {
		_ = resp.Body.Close()
	}()

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
