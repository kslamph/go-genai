# Refresh Token Failure Handling

## Overview

This document describes the current behavior of OmniProxy when refresh token requests are rejected by the authentication server, what mechanisms are missing, and the recovery path for administrators.

## Current Behavior on 401 Refresh Failure

### Affected Providers

The following providers use OAuth-based authentication with refresh tokens:
- **Gemini** (`internal/provider/gemini/auth.go`)
- **Antigravity** (`internal/provider/antigravity/auth.go`)

### Failure Detection

When a refresh token request is rejected by the auth server with `401 Unauthorized`, the system detects this by checking the error message:

```go
errStr := err.Error()
if strings.Contains(errStr, "401") || strings.Contains(errStr, "unauthorized") {
    a.isValid = false
    utils.L().Errorw("Token refresh failed with 401 - credentials are invalid/revoked. Provider marked as invalid.")
    return "", fmt.Errorf("credentials are invalid (401). Please re-authenticate: %w", err)
}
```

### Immediate Actions

1. **Mark Authenticator as Invalid**
   - The `isValid` flag is set to `false`
   - This flag is checked on all subsequent token requests

2. **Log Error**
   - Detailed error logged with provider name, credentials path, and error details
   - Example log:
     ```
     Token refresh failed with 401 - credentials are invalid/revoked. Provider marked as invalid.
     provider: Gemini
     creds_path: /path/to/credentials.json
     error: 401 Unauthorized
     ```

3. **Return Error to Caller**
   - Returns formatted error: `"credentials are invalid (401). Please re-authenticate: ..."`
   - Error propagates up through the call stack to the router

### Subsequent Request Behavior

After a provider is marked as invalid:

1. **Token Requests Fail Immediately**
   ```go
   func (a *Authenticator) GetToken(ctx context.Context) (string, error) {
       // Check if the authenticator is marked as invalid (e.g., due to 401 on refresh)
       if !a.isValid {
           return "", fmt.Errorf("authenticator is marked as invalid (401 on refresh). Please re-authenticate")
       }
       // ... rest of token logic
   }
   ```

2. **Credential Remains in Pool**
   - The credential is not automatically removed from the pool
   - Router may still select this credential for requests
   - All requests using this credential will fail with re-authentication error

3. **No Automatic Recovery**
   - The system does not attempt to re-authenticate automatically
   - The `isValid` flag persists until the service is restarted or credentials are reloaded

## What's Missing

### No Human-in-the-Loop Mechanisms

The current implementation lacks automated mechanisms to handle refresh token expiration:

1. **No Automatic OAuth Re-authentication**
   - No trigger to start a new OAuth flow when refresh tokens expire
   - No mechanism to prompt users for consent
   - No background re-authentication process

2. **No Alerting/Notifications**
   - No alerts sent to administrators when credentials become invalid
   - No webhook or notification system for credential failures
   - No monitoring integration (e.g., Prometheus metrics, Sentry alerts)

3. **No Fallback Authentication**
   - No alternative authentication methods
   - No credential rotation to backup accounts
   - No graceful degradation to read-only mode

4. **No API Endpoint for Re-authentication**
   - No HTTP endpoint to trigger re-authentication
   - No REST API to manage credential lifecycle
   - No admin interface for credential management

5. **No Credential Health Checks**
   - No periodic health checks to detect expiring credentials before they fail
   - No proactive warnings about soon-to-expire refresh tokens
   - No credential expiry dashboard

### Limited Observability

- **No Metrics**: No Prometheus metrics for credential health status
- **No Status Endpoint**: No `/health` or `/status` endpoint showing credential validity
- **No Audit Trail**: No tracking of credential lifecycle events (creation, refresh, failure)

## Current Recovery Path

### Step 1: Detection (Manual)

The administrator must be actively monitoring logs to detect refresh token failures:

```bash
# Check logs for 401 refresh failures
tail -f omniproxy.log | grep "Token refresh failed with 401"
```

### Step 2: Re-authentication (Manual)

The administrator must manually run the authentication flow for each affected provider:

```bash
# For Gemini
./newprofile --provider gemini

# For Antigravity
./newprofile --provider antigravity
```

This process:
- Opens a browser for OAuth consent
- Requires manual user interaction
- Generates new access and refresh tokens
- Saves credentials to the configured path

### Step 3: Service Restart (Required)

After re-authentication, the service must be restarted to load the new credentials:

```bash
# Restart the OmniProxy service
systemctl restart omniproxy
# or
kill -HUP <pid>
# or (if running directly)
./omniproxy
```

**Note**: Simply re-authenticating is not sufficient because:
- The `isValid` flag is cached in memory
- Credentials are loaded at startup
- No hot-reload mechanism exists for credential files

### Step 4: Verification

After restart, verify that credentials are working:

```bash
# Check logs for successful token refresh
tail -f omniproxy.log | grep "Token refreshed successfully"

# Test API endpoint
curl -X POST http://localhost:8143/v1beta/models/gemini-1.5-pro:generateContent \
  -H "Content-Type: application/json" \
  -d '{"contents":[{"parts":[{"text":"test"}]}]}'
```

## Recovery Time

The total recovery time depends on:
- **Detection time**: How quickly the administrator notices the failure (could be hours/days)
- **Re-authentication time**: ~2-5 minutes per provider (including OAuth flow)
- **Service restart time**: ~10-30 seconds
- **Verification time**: ~1-2 minutes

**Typical total recovery time**: 5-10 minutes (if detected immediately)

## Impact During Failure

While credentials are invalid:

1. **User Requests Fail**
   - All requests using the invalid credential return errors
   - Error message: `"credentials are invalid (401). Please re-authenticate"`
   - If other valid credentials exist, they may be used instead (load balancing)

2. **No Automatic Failover**
   - The invalid credential remains in the pool
   - Router may repeatedly select it until all credentials are exhausted
   - Depends on penalty box logic to avoid repeatedly trying invalid credentials

3. **Downtime**
   - If only one credential exists for a provider, complete service outage
   - If multiple credentials exist, degraded service (fewer available credentials)

## Recommendations

To improve refresh token failure handling, consider implementing:

1. **Alerting System**
   - Integrate with notification services (email, Slack, PagerDuty)
   - Alert on credential validation failures
   - Send warnings before tokens expire

2. **Automatic Re-authentication**
   - Implement background re-authentication for non-interactive scenarios
   - Use service accounts where possible
   - Support refresh token rotation

3. **Credential Management API**
   - Add `/admin/credentials` endpoints for lifecycle management
   - Support hot-reload without restart
   - Provide credential status dashboard

4. **Health Monitoring**
   - Expose credential health metrics (Prometheus)
   - Add `/health` endpoint with credential status
   - Track credential lifecycle events

5. **Graceful Degradation**
   - Implement circuit breakers for failing credentials
   - Automatically remove invalid credentials from pool
   - Provide read-only fallback mode

## Related Files

- `internal/provider/gemini/auth.go` - Gemini OAuth implementation
- `internal/provider/antigravity/auth.go` - Antigravity OAuth implementation
- `internal/auth/manager.go` - Authentication manager
- `internal/router/smart_router.go` - Request routing and error handling
- `cmd/newprofile/main.go` - Manual authentication tool