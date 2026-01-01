# Refactoring Plan: Standardize Auth Providers

## Goal
Refactor `antigravity`, `gemini`, and `iflow` providers to follow the `qwen` provider's authentication style. This involves simplifying `GetToken` by delegating logic to helper functions (`IsTokenValid`, `refreshAccessToken`, `saveCredentials`) and improving error handling and concurrency safety.

## Reference Pattern (`internal/provider/qwen/auth.go`)
The `qwen` provider uses the following pattern:
1.  **`IsTokenValid(c Credentials) bool`**: A standalone helper function that checks token expiry with a buffer.
2.  **`refreshAccessToken(c Credentials) (Credentials, error)`**: A method that handles the OAuth refresh flow and returns new credentials.
3.  **`saveCredentials(c Credentials) error`**: A method that saves credentials to disk using file locking (`flock`) to prevent race conditions.
4.  **`GetToken(ctx) (string, error)`**: A simplified orchestration method that:
    *   Loads credentials.
    *   Checks validity using `IsTokenValid`.
    *   Refreshes if necessary using `refreshAccessToken`.
    *   Updates internal state and returns the token.

## Task Breakdown

### 1. Refactor `internal/provider/antigravity/auth.go`
- [ ] **Add `IsTokenValid` function**:
    - Extract expiry check logic (currently inline in `GetToken` and `IsAuthenticated`).
    - Include the 30-minute buffer check.
- [ ] **Add `refreshAccessToken` method**:
    - Extract the OAuth token refresh logic from `GetToken`.
    - Input: `Credentials` (by value or pointer).
    - Output: `(Credentials, error)`.
    - Handle the "force refresh" logic if needed, or keep it separate.
    - Handle 401/Unauthorized errors explicitly.
- [ ] **Update `saveCredentials`**:
    - Add file locking (`github.com/gofrs/flock`) to match `qwen`.
- [ ] **Simplify `GetToken`**:
    - Rewrite to use `IsTokenValid` and `refreshAccessToken`.
    - Remove inline refresh logic.
- [ ] **Update `loadCredentials`**:
    - Ensure it returns `(*Credentials, error)` (already does, but verify consistency).

### 2. Refactor `internal/provider/gemini/auth.go`
- [ ] **Add `IsTokenValid` function**:
    - Extract expiry check logic.
- [ ] **Add `refreshAccessToken` method**:
    - Extract OAuth refresh logic.
    - Handle `ForceRefresh` logic appropriately (maybe keep `ForceRefresh` as a wrapper that calls `refreshAccessToken` with modified expiry).
- [ ] **Update `saveCredentials`**:
    - Add file locking (`github.com/gofrs/flock`).
- [ ] **Simplify `GetToken`**:
    - Rewrite to use `IsTokenValid` and `refreshAccessToken`.

### 3. Refactor `internal/provider/iflow/auth.go`
- [ ] **Add `IsTokenValid` function**:
    - Extract logic from `Credentials.IsExpired` and `Credentials.IsValid`.
    - Handle both `Expire` and `ExpiresAt` fields.
- [ ] **Add `refreshAccessToken` method**:
    - Extract OAuth refresh logic.
    - **Crucial**: Include the `fetchUserInfo` logic (fetching API key) within this method or immediately after token refresh, so it returns a fully valid `Credentials` object.
- [ ] **Update `loadCredentials`**:
    - Change signature from `func (a *Authenticator) loadCredentials()` to `func (a *Authenticator) loadCredentials() (*Credentials, error)` to match the pattern.
- [ ] **Update `saveCredentials`**:
    - Add file locking (`github.com/gofrs/flock`).
- [ ] **Simplify `GetToken`**:
    - Rewrite to use the new helpers.

### 4. Verification
- [ ] Verify all providers build successfully.
- [ ] Ensure `go.mod` has `github.com/gofrs/flock` (it is already used in `qwen`, so it should be there).
