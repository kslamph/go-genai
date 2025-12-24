package gemini

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
	"google.golang.org/genai"
)

const (
	CloudCodeBaseURL = "https://cloudcode-pa.googleapis.com"
)

type GeminiProvider struct {
	client *genai.Client
	name   string
	auth   *Authenticator
}

// Ensure GeminiProvider implements provider.Provider
var _ provider.Provider = (*GeminiProvider)(nil)

func NewProvider(ctx context.Context, name string, auth *Authenticator) (*GeminiProvider, error) {
	// 1. Discover Project ID
	projectID, err := discoverProjectID(ctx, auth, CloudCodeBaseURL)
	if err != nil {
		return nil, fmt.Errorf("failed to discover project ID: %w", err)
	}

	// 2. Create GenAI Client
	tokenProvider := &TokenProvider{authenticator: auth}
	creds := cloudauth.NewCredentials(&cloudauth.CredentialsOptions{
		TokenProvider: tokenProvider,
	})

	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		Backend:     genai.BackendGeminiCLI,
		Project:     projectID,
		Credentials: creds,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create genai client: %w", err)
	}

	return &GeminiProvider{
		client: client,
		name:   name,
		auth:   auth,
	}, nil
}

func (p *GeminiProvider) Type() string {
	return "gemini"
}

func (p *GeminiProvider) Name() string {
	return p.name
}

func (p *GeminiProvider) ChatCompletion(ctx context.Context, req openai.ChatCompletionRequest) (*openai.ChatCompletionResponse, error) {
	sys, contents, config, err := ToGeminiRequest(req)
	if err != nil {
		return nil, err
	}

	if sys != nil {
		config.SystemInstruction = sys
	}

	// The genai library's GenerateContent method signature might vary slightly depending on version,
	// but generally takes model, contents, and config.
	// We assume "gemini-2.5-flash" or similar model is passed in req.Model
	resp, err := p.client.Models.GenerateContent(ctx, req.Model, contents, config)
	if err != nil {
		return nil, err
	}

	return FromGeminiResponse(resp, req.Model), nil
}

func (p *GeminiProvider) StreamChatCompletion(ctx context.Context, req openai.ChatCompletionRequest) (<-chan openai.ChatCompletionStreamResponse, <-chan error) {
	respChan := make(chan openai.ChatCompletionStreamResponse)
	errChan := make(chan error, 1)

	sys, contents, config, err := ToGeminiRequest(req)
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

			chunk := FromGeminiChunk(resp, req.Model)
			respChan <- *chunk
		}
	}()

	return respChan, errChan
}

// discoverProjectID helps find the project ID needed for Gemini API
func discoverProjectID(ctx context.Context, authenticator *Authenticator, baseURL string) (string, error) {
	// authenticator.ForceRefresh(ctx) // Optional: might be needed if token is stale
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
