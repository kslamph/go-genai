# newprofile Usage

The `newprofile` tool is used to authenticate with various providers and manage OAuth credentials.

## Supported Providers

- `gemini` - Google Gemini
- `antigravity` - Antigravity
- `iflow` - iFlow
- `qwen` - Qwen AI

## New Authentication

To create a new profile and authenticate with a provider:

```bash
newprofile -provider <provider>
```

Example:
```bash
newprofile -provider iflow
```

This will:
1. Start the OAuth flow for the specified provider
2. Save credentials to `~/.omniproxy/credential/<provider>-cli_<timestamp>_oauth_creds.json`
3. Automatically add the credential to your omniproxy.yaml configuration

## Re-authentication

To re-authenticate an existing credential file:

```bash
newprofile -provider <provider> -reauth <path-to-credential-file>
```

Example:
```bash
newprofile -provider iflow -reauth ~/.omniproxy/credential/iflow-cli_1735567890_oauth_creds.json
```

This will:
1. Update the existing credential file with fresh tokens
2. Not modify the omniproxy.yaml configuration (since the credential path already exists)

## Authentication Flows

- **gemini, antigravity, iflow**: Opens a browser window for OAuth authorization
- **qwen**: Uses OAuth 2.0 device flow - displays a verification URL and user code to enter