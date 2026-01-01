# Usage: Gemini CLI & Antigravity Backends

This library supports connecting to the **Gemini CLI** (Cloud Code) and **Antigravity** (Internal) backends. These backends require specific authentication flows and protocol handling which differ from the standard Google Cloud Vertex AI or Gemini API.

## Overview

| Backend | Constant | Description | Base URL |
| :--- | :--- | :--- | :--- |
| **Gemini CLI** | `genai.BackendGeminiCLI` | Access Google CGemini Code Assist LLM endpoint. | `https://cloudcode-pa.googleapis.com/` |
| **Antigravity** | `genai.BackendAntigravity` | Access Antigravity LLM endpoint. | `https://daily-cloudcode-pa.sandbox.googleapis.com/` (with auto-failover) |

## Authentication (Crucial)

Unlike standard Google Cloud APIs, these backends **do not** accept standard Application Default Credentials (ADC). They require OAuth 2.0 Access Tokens issued to specific, hardcoded Client IDs associated with the Cloud Code tools.

**This library does not bundle these secrets.** You must implement an OAuth flow in your application to acquire the tokens, and then inject them into the client using a custom `TokenProvider`.

### Reference Client IDs

If you are building a tool that emulates these clients (like `omniproxy`), you will need to authenticate using these specific OAuth 2.0 credentials:

*   **Gemini CLI Client ID:** `681255809395-oo8ft2oprdrnp9e3aqf6av3hmdib135j.apps.googleusercontent.com`
*   **Antigravity Client ID:** `1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com`

### Implementing a TokenProvider

You must wrap your access token logic in a struct that satisfies `cloud.google.com/go/auth.TokenProvider`.

```go
import (
    "context"
    "time"
    "cloud.google.com/go/auth"
)

type MyCustomTokenProvider struct {
    // Your logic to hold/refresh tokens (e.g., from a file or OAuth flow)
    AccessToken string
    Expiry      time.Time
}

func (p *MyCustomTokenProvider) Token(ctx context.Context) (*auth.Token, error) {
    // Logic to check expiry and refresh if needed...
    // ...
    return &auth.Token{
        Value:  p.AccessToken,
        Expiry: p.Expiry,
        Type:   "Bearer",
    }, nil
}
```

## Using BackendAntigravity

The `BackendAntigravity` mode includes several automatic behaviors to match the reference implementation:

1.  **Base URL:** Defaults to the "Daily" sandbox endpoint (`v1internal` API version).
2.  **Auto-Failover:** If the Daily endpoint fails (5xx or network error), the client automatically retries against the "Autopush" fallback endpoint.
3.  **User-Agent:** Automatically injects the required `antigravity/1.11.5...` header.
4.  **Session ID:** Uses the required negative 64-bit integer format (`-12345...`) instead of `session-...`.

### Project Discovery

Antigravity and Gemini CLI credentials often utilize a dynamic "Cloud Companion" Project ID rather than a static Google Cloud Project ID. This library provides a helper to discover this ID.

```go
import (
    "context"
    "log"
    "cloud.google.com/go/auth"
    "google.golang.org/genai"
)

func main() {
    ctx := context.Background()
    
    // 1. Setup your custom credentials (using the Antigravity Client ID)
    myTokenProvider := &MyCustomTokenProvider{ /* ... */ }
    creds := auth.NewCredentials(&auth.CredentialsOptions{
        TokenProvider: myTokenProvider,
    })

    // 2. Discover the correct Project ID
    // Pass the backend type (BackendAntigravity or BackendGeminiCLI) to ensure the correct endpoint is used.
    projectID, err := genai.DiscoverCloudCodeProject(ctx, creds, genai.BackendAntigravity)
    if err != nil {
        log.Printf("Discovery failed, falling back to manual ID: %v", err)
        projectID = "my-fallback-project-id"
    }
    log.Printf("Using Project ID: %s", projectID)

    // 3. Initialize the Client
    client, err := genai.NewClient(ctx, &genai.ClientConfig{
        Backend:     genai.BackendAntigravity,
        Project:     projectID,
        Credentials: creds,
        // BaseURL and APIVersion are set automatically for BackendAntigravity
    })
    if err != nil {
        log.Fatal(err)
    }

    // 4. Use the client (Models, Streaming, etc.)
    // Note: Antigravity model names often differ from public ones (e.g., "gemini-2.5-flash" vs internal names).
    // Ensure you use the correct model ID expected by the backend.
}
```

## Using BackendGeminiCLI

The `BackendGeminiCLI` is simpler but requires the specific Gemini CLI OAuth credentials.

```go
    // ... setup creds using Gemini CLI Client ID ...

## Token Caching & Re-authentication

The `genai.Client` utilizes the underlying Google Auth transport, which implements **in-memory token caching**.

### How it Works:
1.  **Memory First:** When making a request, the client checks if it has a valid, non-expired Access Token in memory. If it does, it uses it immediately.
2.  **Provider Call:** Your `TokenProvider.Token()` method is **only called** when the in-memory token has expired.
3.  **Automatic Refresh:** If your `Token()` method implementation handles refreshing (e.g., reading an updated JSON file or calling an OAuth refresh endpoint), the client will stay authenticated indefinitely without further intervention.

### Important Remarks for Developers:
*   **User Switching:** If your application changes the underlying credentials (e.g., the user logs out and logs in with a different account) while the program is running, the client **will not pick up the change immediately** if the previous token is still valid in memory.
*   **Immediate Update:** To force the client to use new credentials immediately, you should **re-initialize the client** by calling `genai.NewClient` again with the new credentials. This creates a fresh instance with an empty token cache.
*   **Persistent Connections:** The client is designed for long-running processes. Rely on the `Token()` method's refresh logic for standard operational continuity.
