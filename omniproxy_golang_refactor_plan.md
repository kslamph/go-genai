# OmniProxy v2 Re-Architecture Proposal (Golang)

## 1. Executive Summary
This proposal outlines a re-architecture for **OmniProxy** to address current limitations in resiliency, authentication, and state management. The goal is to transform OmniProxy from a simple pass-through proxy into a **smart, self-healing gateway** while retaining the existing Golang codebase and protocol constraints.

**Key Design Shifts:**
1.  **Strict Protocol Segregation:** OpenAI models are accessed *only* via OpenAI protocol; Gemini models *only* via Gemini protocol. No cross-protocol translation (unlike LiteLLM).
2.  **Stateful Credential Management:** Moving from static config-based providers to dynamic, state-aware "Smart Auth" managers.
3.  **Resilient Routing:** Implementing a "Retry Loop" with silent failover and "Penalty Box" logic for rate limits.

---

## 2. Core Concepts

### 2.1 The "Virtual Model" Abstraction
Currently, OmniProxy couples a request directly to a provider. We introduce **Virtual Models** as the primary addressable entity.

*   **Definition:** A Virtual Model (e.g., `gemini-1.5-pro`) is a logical resource backed by a **Pool** of interchangeable credentials.
*   **Benefit:** The router targets a Virtual Model, not a specific API key. This allows the system to swap underlying credentials transparently during errors.

### 2.2 Lazy Proactive Auth Manager (The "Life Support" System)
Authentication logic is extracted from the Provider and moved to a dedicated manager. Instead of constant background refresh, we use a "lazy proactive" approach that balances efficiency with zero-downtime recovery.

*   **Lazy Proactive Refresh:** Before each request, perform a lightweight check (cached, once per 5 minutes per credential) to see if token is expiring within 1 minute. Only refresh if actually needed. No background goroutines required.
*   **Reactive Recovery (Lazarus Protocol):**
    *   If a request fails with `401 Unauthorized`:
    *   The Router catches this specific error.
    *   Triggers `ForceRefresh(credentialID)` immediately (blocking).
    *   For genai-based providers (Gemini/Antigravity), also recreates the `genai.Client` (critical - see note below).
    *   Retries the request *once* with the new token and new client.
    *   If it fails again, marks the credential as **DEAD**.

**⚠️ Critical: genai.Client Token Caching Issue**

The Google `genai.Client` SDK caches authentication tokens internally and does not automatically pick up refreshed tokens from the `TokenProvider`. This means:

- Simply updating `cred.AccessToken` is **insufficient** for Gemini/Antigravity providers
- The `genai.Client` instance must be **recreated** after token refresh
- Client recreation involves an HTTP call to discover project ID (~100-200ms overhead)

**Solution:** Use a Client Pool pattern to minimize recreation overhead and enable seamless token rotation.

### 2.3 Resilient Router (The "Retry Loop")
The Router wraps every request in a loop that persists until success or total exhaustion.

*   **Silent Failover:** If Credential A fails (429/5xx), the user *never knows*. The router immediately grabs Credential B and retries.
*   **Penalty Box:** Credentials that hit Rate Limits (429) are not just "skipped" once; they are placed in a `PenaltyBox` for `X` seconds (respecting `Retry-After` headers).

---

## 3. Architecture Design (Golang)

### 3.1 New Interface Definitions

#### `Credential` (Stateful Entity)
```go
type ProviderType int
const (
    ProviderOpenAI ProviderType = iota  // Standard HTTP client, no token cache
    ProviderGemini                      // genai.Client with internal token cache
    ProviderAntigravity                 // genai.Client with internal token cache
    ProviderQwen                        // Standard HTTP client
    ProviderIFlow                       // Standard HTTP client
)

type CredentialState int
const (
    StateActive CredentialState = iota
    StatePenaltyBox
    StateDead
)

type Credential struct {
    ID           string
    ProviderType ProviderType

    // Auth Data (Thread-Safe Access)
    mu           sync.RWMutex
    AccessToken  string
    RefreshToken string
    Expiry       time.Time
    ProjectID    string  // For genai-based providers

    // Health State
    State        CredentialState
    PenaltyUntil time.Time
    FailureCount int

    // Client Pool (for genai-based providers only)
    clientPool   *ClientPool  // nil for OpenAI/Qwen/IFlow
}

// ClientPool manages multiple genai.Client instances for seamless rotation
type ClientPool struct {
    mu      sync.RWMutex
    clients []*genai.Client  // Pool of client instances
    current int              // Index of currently active client
}

// GetClient returns the current active client from the pool
func (p *ClientPool) GetClient() *genai.Client {
    p.mu.RLock()
    defer p.mu.RUnlock()
    if len(p.clients) == 0 {
        return nil
    }
    return p.clients[p.current]
}

// RefreshClient creates a new client with fresh credentials and rotates it into the pool
func (p *ClientPool) RefreshClient(ctx context.Context, tokenProvider TokenProvider, projectID string, backend genai.Backend, baseURL string) error {
    newClient, err := genai.NewClient(ctx, &genai.ClientConfig{
        Backend:     backend,
        Project:     projectID,
        Credentials: cloudauth.NewCredentials(&cloudauth.CredentialsOptions{
            TokenProvider: tokenProvider,
        }),
        HTTPOptions: genai.HTTPOptions{
            BaseURL: baseURL,
        },
    })
    if err != nil {
        return err
    }

    p.mu.Lock()
    // Add new client to pool
    p.clients = append(p.clients, newClient)
    // Rotate to new client
    p.current = len(p.clients) - 1
    p.mu.Unlock()

    return nil
}

// Cleanup removes old clients (called periodically to prevent memory leaks)
func (p *ClientPool) Cleanup() {
    p.mu.Lock()
    defer p.mu.Unlock()
    // Keep only the most recent client
    if len(p.clients) > 1 {
        p.clients = p.clients[len(p.clients)-1:]
        p.current = 0
    }
}
```

#### `AuthManager` (Authentication Handler)
```go
type AuthManager interface {
    // Called by Router on 401 - synchronous blocking refresh
    ForceRefresh(ctx context.Context, cred *Credential) error

    // Called by Router before each request - lazy proactive check
    // Only refreshes if token is expiring within 1 minute (cached check, once per 5 min)
    EnsureValidToken(ctx context.Context, cred *Credential) error

    // Recreates genai.Client with fresh credentials (for genai-based providers only)
    RefreshClient(ctx context.Context, cred *Credential) error

    // Validates if token is structurally valid before request
    Validate(cred *Credential) bool
}
```

#### `SmartRouter` (The Orchestrator)
```go
type Router interface {
    // Protocol-Specific Handlers
    HandleOpenAIRequest(w http.ResponseWriter, r *http.Request)
    HandleGeminiRequest(w http.ResponseWriter, r *http.Request)
}
```

### 3.2 Request Lifecycle (The "Retry Loop")

**Scenario:** User requests `gemini-1.5-pro` via Gemini Protocol.

1.  **Ingress:** `HandleGeminiRequest` receives the request.
2.  **Lookup:** Identifies `VirtualModel("gemini-1.5-pro")`.
3.  **Loop Start:**
    *   **Select Credential:** `Pool.Next()` returns the best available credential (skipping DEAD/PENALTY).
    *   **Lazy Proactive Check:** `AuthManager.EnsureValidToken(cred)` - lightweight cached check (once per 5 min).
        *   If token expiring < 1 min: `ForceRefresh(cred)` + `RefreshClient(cred)` (for genai-based providers).
    *   **Inject Auth:**
        *   For OpenAI/Qwen/IFlow: Mutate request headers with `cred.AccessToken`.
        *   For Gemini/Antigravity: Get client from `cred.clientPool.GetClient()`.
    *   **Execute:** Request sent to upstream provider.
    *   **Analyze Result:**
        *   `200 OK`: Reset failure count -> **Return to User**.
        *   `429 Too Many Requests`:
            *   Read `Retry-After` header.
            *   `Pool.MarkPenalty(cred, duration)`.
            *   **Continue Loop** (Try next cred).
        *   `401 Unauthorized`:
            *   `AuthManager.ForceRefresh(cred)` - immediate synchronous refresh.
            *   If success:
                *   For genai-based providers: `AuthManager.RefreshClient(cred)` - recreate client.
                *   **Retry same cred** (transparent to user).
            *   If fail: `Pool.MarkDead(cred)` -> **Continue Loop**.
        *   `5xx Server Error`:
            *   Increment failure count.
            *   If count > Threshold -> `Pool.MarkPenalty(cred, 5m)`.
            *   **Continue Loop**.
4.  **Exhaustion:** If all credentials fail, return `503 Service Unavailable` to user.

**Timeline for 401 Recovery (Zero Downtime):**
- T+0ms: Request fails with 401
- T+100ms: Token refresh completes
- T+200ms: Client recreation completes (genai-based only)
- T+300ms: Retry succeeds
- **Total: ~300ms user delay** (but no error visible - just slightly slower response)

**Performance Comparison:**

| Approach | Network Calls/ Hour | Client Recreation | User-Visible Errors | Complexity |
|----------|-------------------|-------------------|-------------------|-----------|
| Proactive Refresh | ~6 per credential | Every 5 min | Rare (race condition) | High (background goroutines) |
| Lazy Proactive | 1-2 per credential (only when needed) | Only on 401 | None (transparent retry) | Medium (client pool) |

---

## 4. Implementation Plan (Refactoring Steps)

### Phase 1: Decoupling Auth + Client Pool
*   **Goal:** Strip authentication logic out of `internal/provider/` and implement client pool for genai-based providers.
*   **Action:**
    *   Create `internal/auth/` package.
    *   Move OAuth logic (Antigravity/Gemini-CLI) into `AuthManager` implementations.
    *   Implement `ClientPool` struct for managing genai.Client instances.
    *   Add `RefreshClient()` method to `AuthManager` interface.
    *   Implement `EnsureValidToken()` with lazy proactive logic (cached check, 5-min interval).
*   **Result:**
    *   Providers become "dumb" HTTP clients that just take a token and send a request.
    *   Genai-based providers use client pool for seamless token rotation.
    *   No background goroutines - all checks happen inline during requests.

### Phase 2: State Management
*   **Goal:** Replace the static config lists with mutable Credential structures.
*   **Action:**
    *   Modify `internal/manager/pool.go` to store `*Credential` pointers instead of simple config objects.
    *   Add `sync.RWMutex` for thread safety.
    *   Initialize `ClientPool` for genai-based providers (Gemini, Antigravity).
    *   Set `ProviderType` based on credential configuration.

### Phase 3: The Router Loop
*   **Goal:** Implement the "Silent Failover" logic with lazy proactive token validation.
*   **Action:** Rewrite `internal/api/` handlers. Instead of `provider.ChatCompletion()`, call `router.Execute(ctx, request)`.
*   **Logic:** The `Execute` function implements the Retry Loop described in 3.2, including:
    *   Call `EnsureValidToken(cred)` before each request (lazy proactive check).
    *   On 401: call `ForceRefresh(cred)` and `RefreshClient(cred)` (for genai-based providers).
    *   Retry the request with refreshed token and client (transparent to user).

### Phase 4: Protocol Enforcement
*   **Goal:** Ensure strict protocol isolation.
*   **Action:**
    *   `router.Execute` should accept a `Protocol` argument.
    *   If `VirtualModel` supports multiple protocols (unlikely per your requirement, but good for future), filter credentials that match the requested protocol.
    *   Ensure OpenAI handlers *never* touch Gemini credentials and vice-versa.

### Phase 5: Client Pool Cleanup
*   **Goal:** Prevent memory leaks from accumulated genai.Client instances.
*   **Action:**
    *   Add a background cleanup goroutine that calls `clientPool.Cleanup()` every hour.
    *   Cleanup removes old clients, keeping only the most recent one.
    *   This is the only background goroutine in the system (minimal complexity).

---

## 5. Migration Strategy
To avoid breaking the current system entirely:
1.  **Parallel Implementation:** create `internal/router_v2/` and `internal/auth_v2/`.
2.  **Config Flag:** Add `enable_v2_routing: true` in `omniproxy.yaml`.
3.  **Gradual Rollout:** Switch endpoints to use V2 logic one protocol at a time (e.g., start with Gemini, then OpenAI).

## 6. Critical Advantages of V2
*   **Zero Downtime Token Rotation:**
    *   Lazy proactive refresh catches expiring tokens before 401s occur (most of the time).
    *   If a 401 does occur, transparent retry with refreshed token (user sees ~300ms delay, no error).
    *   No background goroutines for refresh - all checks happen inline during requests.
*   **Maximized Throughput:**
    *   If one account hits a rate limit, traffic instantly flows to the others.
    *   Client pool minimizes recreation overhead for genai-based providers.
*   **Observability:**
    *   We can now track "Dead Credentials" vs "Penalty Credentials" separately.
    *   Better monitoring alerts for credential health issues.
*   **Resource Efficiency:**
    *   Minimal network overhead: only refresh when actually needed (1-2 times/hour vs 6 times/hour).
    *   Client recreation only on 401 errors, not on fixed schedules.
    *   Single background goroutine for cleanup (vs multiple tickers for refresh).

## 7. Important Caveats

### 7.1 genai-Based Providers (Gemini, Antigravity)
- **Token Caching:** The `genai.Client` SDK caches tokens internally and doesn't automatically pick up refreshed tokens.
- **Client Recreation Required:** After token refresh, the `genai.Client` instance must be recreated.
- **Overhead:** Client recreation involves an HTTP call to discover project ID (~100-200ms).
- **Mitigation:** Client pool pattern minimizes recreation frequency and enables seamless rotation.

### 7.2 Race Conditions
- **Small Window:** There's a brief window (between token expiry check and actual request) where a 401 might still occur.
- **Impact:** ~300ms user delay (transparent retry), not a visible error.
- **Probability:** Very low with lazy proactive 1-minute buffer.

### 7.3 OpenAI/Qwen/IFlow Providers
- **No Token Caching:** These providers use standard HTTP clients with manual token injection.
- **Simpler:** No client recreation needed - just update the token in headers.
- **More Efficient:** Zero overhead for token refresh.
