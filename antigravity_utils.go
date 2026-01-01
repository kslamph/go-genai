// Copyright 2025 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package genai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"cloud.google.com/go/auth"
)

// DiscoverCloudCodeProject attempts to auto-discover the Google Cloud Project ID
// associated with the provided credentials for the Gemini CLI or Antigravity backend.
//
// This helper function mimics the behavior of the official Cloud Code extension
// and other internal tools by calling the `loadCodeAssist` endpoint.
//
// Supported backends: BackendAntigravity, BackendGeminiCLI.
func DiscoverCloudCodeProject(ctx context.Context, creds *auth.Credentials, backend Backend) (string, error) {
	var baseURL string
	if backend == BackendAntigravity {
		baseURL = AntigravityBaseURLDaily
	} else if backend == BackendGeminiCLI {
		baseURL = GeminiCLIBaseURL
	} else {
		return "", fmt.Errorf("unsupported backend for project discovery: %v", backend)
	}

	apiPath := "v1internal:loadCodeAssist"
	url := baseURL + apiPath

	// 1. Prepare Metadata & Request
	clientMetadata := map[string]interface{}{
		"ideType":    "IDE_UNSPECIFIED",
		"platform":   "PLATFORM_UNSPECIFIED",
		"pluginType": "GEMINI",
	}
	loadRequest := map[string]interface{}{
		"cloudaicompanionProject": "", // Empty to request discovery
		"metadata":                clientMetadata,
	}

	reqBody, err := json.Marshal(loadRequest)
	if err != nil {
		return "", fmt.Errorf("failed to marshal loadCodeAssist request: %w", err)
	}

	// 2. Create Request
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(reqBody))
	if err != nil {
		return "", fmt.Errorf("failed to create discovery request: %w", err)
	}

	// 3. Add Headers
	req.Header.Set("Content-Type", "application/json")
	if backend == BackendAntigravity {
		req.Header.Set("User-Agent", AntigravityUserAgent)
	}

	// Get Token from Creds
	token, err := creds.Token(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to get auth token for discovery: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token.Value)

	// 4. Execute Request
	client := &http.Client{} // Use default client for this helper
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("discovery request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("loadCodeAssist API returned status %d: %s", resp.StatusCode, string(body))
	}

	// 5. Parse Response
	var loadResponse map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&loadResponse); err != nil {
		return "", fmt.Errorf("failed to decode discovery response: %w", err)
	}

	// 6. Extract Project ID
	if projectID, ok := loadResponse["cloudaicompanionProject"].(string); ok && projectID != "" {
		return projectID, nil
	}

	return "", fmt.Errorf("project ID not found in response")
}
