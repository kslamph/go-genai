# Model Selection Logic

The model selection logic in `omniproxy` is handled by the `ProviderService` in [`internal/manager/service.go`](internal/manager/service.go). It coordinates between the `ProviderRegistry`, `LoadBalancer`, and `RateLimitTracker` to select the best provider for a given request.

## Provider Categorization

Providers are categorized into three main types:
-   **OpenAI-compatible**: Providers like Qwen and IFlow.
-   **Gemini-native**: Providers like Gemini and Antigravity.
-   **Kiro-native**: Providers with custom protocols.

## Selection Strategies

### 1. Gemini-native Selection

For Gemini-native requests, the selection logic is implemented in `ProviderService.GetGeminiProvider` and `LoadBalancer.SelectGeminiProvider`.

-   **Candidate Selection**: It identifies all providers of the requested type (e.g., "gemini" or "antigravity") that support the requested model.
-   **Rate Limit Awareness**: It uses the `RateLimitTracker` to check the last rejection time for each candidate provider-model combination.
-   **Best Candidate Selection**:
    -   It prefers providers that have never been rejected or have the oldest rejection time.
    -   If multiple providers have the same rejection time (e.g., both never rejected), it randomly selects one to distribute load.
-   **Quota Awareness**: It also checks if a provider is currently blocked due to quota exhaustion (`QUOTA_EXHAUSTED` error with a specific reset time).

### 2. OpenAI-compatible Selection

For OpenAI-compatible requests, the selection logic is implemented in `ProviderService.GetOpenAIProvider` and `LoadBalancer.SelectOpenAIProvider`.

-   **Last Success Preference**: It first checks if the last provider that successfully served a request for the given model matches the requested type. If so, it reuses that provider to maintain consistency.
-   **Smart Round-Robin**: If no last success is available or it doesn't match, it uses a round-robin strategy among available providers of that type.
-   **Failure Tracking**: The `LoadBalancer` tracks failure counts for each provider. It prefers providers with the minimum number of recent failures.
-   **Tie-breaking**: If multiple providers have the same minimum failure count, it uses round-robin among them.

### 3. Model-based Selection (Load Balancing)

When a request doesn't specify a provider type (e.g., `POST /v1beta/models/{model}:generateContent`), `omniproxy` load balances across all available providers that support the model.

-   **Gemini**: It collects all Gemini-native providers (from both "gemini" and "antigravity" pools) that support the model and applies the Gemini selection strategy.
-   **OpenAI**: It collects all OpenAI-compatible providers that support the model and applies the OpenAI selection strategy.

## State Management

The selection state is maintained in-memory:
-   **`ProviderRegistry`**: Stores the initialized provider instances.
-   **`LoadBalancer`**: Tracks `lastSuccess`, `failureCount`, and `lastUsedIndex` using thread-safe `sync.Map` and `atomic` values.
-   **`RateLimitTracker`**: Tracks `rateLimitRejectTime` and `quotaExhaustedResetTime` using `sync.Map`.

This architecture ensures high performance and low contention even under heavy concurrent load.
