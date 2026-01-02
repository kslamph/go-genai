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
	"time"

	"cloud.google.com/go/auth"
)

// DiscoverCloudCodeProject attempts to auto-discover the Google Cloud Project ID
// associated with the provided credentials for the Gemini CLI or Antigravity backend.
//
// This helper function mimics the behavior of the official Cloud Code extension
// and other internal tools by calling the `loadCodeAssist` endpoint.
//
// If no project is found, it will attempt to onboard the user by calling the
// `onboardUser` endpoint and polling until completion.
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

	// 2. Call loadCodeAssist to check if project exists
	loadResponse, err := callCodeAssistAPI(ctx, creds, baseURL, "loadCodeAssist", loadRequest, backend)
	if err != nil {
		return "", fmt.Errorf("loadCodeAssist failed: %w", err)
	}

	// 3. Check if project already exists
	if projectID, ok := loadResponse["cloudaicompanionProject"].(string); ok && projectID != "" {
		return projectID, nil
	}

	// 4. If no project exists, onboard the user
	// Get the default tier from the response
	defaultTier := "free-tier"
	if allowedTiers, ok := loadResponse["allowedTiers"].([]interface{}); ok {
		for _, tier := range allowedTiers {
			if tierMap, ok := tier.(map[string]interface{}); ok {
				if isDefault, ok := tierMap["isDefault"].(bool); ok && isDefault {
					if tierID, ok := tierMap["id"].(string); ok {
						defaultTier = tierID
						break
					}
				}
			}
		}
	}

	onboardRequest := map[string]interface{}{
		"tierId":                  defaultTier,
		"cloudaicompanionProject": "",
		"metadata":                clientMetadata,
	}

	// 5. Poll onboardUser until completion
	return onboardUser(ctx, creds, baseURL, onboardRequest, backend)
}

// onboardUser calls the onboardUser endpoint and polls for completion
func onboardUser(ctx context.Context, creds *auth.Credentials, baseURL string, request map[string]interface{}, backend Backend) (string, error) {
	const maxRetries = 30
	const pollInterval = 2 * time.Second

	for retryCount := 0; retryCount < maxRetries; retryCount++ {
		response, err := callCodeAssistAPI(ctx, creds, baseURL, "onboardUser", request, backend)
		if err != nil {
			return "", fmt.Errorf("onboardUser failed: %w", err)
		}

		// Check if operation is done
		if done, ok := response["done"].(bool); ok && done {
			// Extract project ID from response
			if respData, ok := response["response"].(map[string]interface{}); ok {
				if projectData, ok := respData["cloudaicompanionProject"].(map[string]interface{}); ok {
					if projectID, ok := projectData["id"].(string); ok && projectID != "" {
						return projectID, nil
					}
				}
			}
			// If no project ID in response, return empty string (fallback)
			return "", nil
		}

		// Wait before next poll
		if retryCount < maxRetries-1 {
			select {
			case <-time.After(pollInterval):
				continue
			case <-ctx.Done():
				return "", fmt.Errorf("onboardUser poll cancelled: %w", ctx.Err())
			}
		}
	}

	return "", fmt.Errorf("onboardUser timeout: operation did not complete within %d retries", maxRetries)
}

// callCodeAssistAPI is a helper function to make API calls to the Code Assist endpoints
func callCodeAssistAPI(ctx context.Context, creds *auth.Credentials, baseURL, method string, body map[string]interface{}, backend Backend) (map[string]interface{}, error) {
	apiPath := "v1internal:" + method
	url := baseURL + apiPath

	reqBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal %s request: %w", method, err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create %s request: %w", method, err)
	}

	req.Header.Set("Content-Type", "application/json")
	if backend == BackendAntigravity {
		req.Header.Set("User-Agent", AntigravityUserAgent)
	}

	token, err := creds.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get auth token for %s: %w", method, err)
	}
	req.Header.Set("Authorization", "Bearer "+token.Value)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s request failed: %w", method, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("%s API returned status %d: %s", method, resp.StatusCode, string(bodyBytes))
	}

	var response map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, fmt.Errorf("failed to decode %s response: %w", method, err)
	}

	return response, nil
}
