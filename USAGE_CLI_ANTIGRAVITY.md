# Usage Examples for GeminiCLI and Antigravity Backends

This document provides examples of how to use the newly added `BackendGeminiCLI` and `BackendAntigravity` backends in the `go-genai` SDK. These backends are designed to work with the Cloud Code Assist and Antigravity protocols, which use a wrapped request/response format and require project discovery.

## Project ID Discovery

Both backends typically require a Project ID that is discovered dynamically after authentication. You can use the following helper function to discover it:

```go
func discoverProjectID(ctx context.Context, authenticator *auth.GeminiAuthenticator, baseURL string) (string, error) {
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
	// baseURL should be "https://cloudcode-pa.googleapis.com" for Gemini CLI
	// or "https://daily-cloudcode-pa.sandbox.googleapis.com" for Antigravity
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
	defer resp.Body.Close()

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
```

## 1. BackendGeminiCLI (Cloud Code Assist API)

### Initialization

```go
import (
    "context"
    "google.golang.org/genai"
    cloudauth "cloud.google.com/go/auth"
)

ctx := context.Background()
authenticator := auth.NewGeminiAuthenticator(nil)

projectID, _ := discoverProjectID(ctx, authenticator, "https://cloudcode-pa.googleapis.com")

// Create a TokenProvider wrapper for the SDK
tokenProvider := &MyTokenProvider{authenticator: authenticator}
creds := cloudauth.NewCredentials(&cloudauth.CredentialsOptions{
    TokenProvider: tokenProvider,
})

client, err := genai.NewClient(ctx, &genai.ClientConfig{
    Backend:     genai.BackendGeminiCLI,
    Project:     projectID,
    Credentials: creds,
})
```

---

## 2. BackendAntigravity

### Initialization

```go
antigravityAuth := auth.NewGeminiAuthenticator(&auth.GeminiOAuthConfig{
    // ... custom config for antigravity
    CredsDir: ".antigravity",
})

projectID, _ := discoverProjectID(ctx, antigravityAuth, "https://daily-cloudcode-pa.sandbox.googleapis.com")

client, err := genai.NewClient(ctx, &genai.ClientConfig{
    Backend:     genai.BackendAntigravity,
    Project:     projectID,
    Credentials: creds,
    HTTPOptions: genai.HTTPOptions{
        BaseURL: "https://daily-cloudcode-pa.sandbox.googleapis.com",
    },
})
```

## Key Differences from Standard Gemini API

1.  **Request Wrapping**: The SDK automatically wraps the native Gemini request inside a `"request"` field and elevates `model` and `project` to the top level.
2.  **Response Unwrapping**: The SDK automatically extracts the response from the `"response"` field returned by these backends.
3.  **Model ID**: Use the raw model ID (e.g., `gemini-2.5-flash`).
4.  **User Agent**: For `BackendAntigravity`, the SDK automatically sets the `userAgent` field to `"antigravity"`.