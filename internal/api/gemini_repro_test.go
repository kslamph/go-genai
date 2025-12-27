package api

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func fixThoughtSignatures(data any) {
	switch v := data.(type) {
	case map[string]any:
		for k, val := range v {
			if k == "thoughtSignature" {
				if str, ok := val.(string); ok {
					v[k] = base64.StdEncoding.EncodeToString([]byte(str))
				}
			} else {
				fixThoughtSignatures(val)
			}
		}
	case []any:
		for _, item := range v {
			fixThoughtSignatures(item)
		}
	}
}

func TestReproduceAndFixDecodeError(t *testing.T) {
	content, err := os.ReadFile("../../debug_dumps/gemini_req_pws_oNFsFhULCy-000002.log")
	if err != nil {
		t.Fatalf("Failed to read log file: %v", err)
	}

	sContent := string(content)
	startMarker := "=== Raw Request Body ===\n"
	startIndex := strings.Index(sContent, startMarker)
	if startIndex == -1 {
		t.Fatal("Could not find start marker")
	}
	startIndex += len(startMarker)

	endMarker := "\n\n=== Parsed GenerationConfig ==="
	endIndex := strings.Index(sContent, endMarker)
	if endIndex == -1 {
		t.Fatal("Could not find end marker")
	}

	jsonBody := sContent[startIndex:endIndex]

	var raw map[string]any
	if err := json.Unmarshal([]byte(jsonBody), &raw); err != nil {
		t.Fatalf("Raw unmarshal failed: %v", err)
	}

	fixThoughtSignatures(raw)

	fixedBytes, _ := json.Marshal(raw)

	var req GeminiRequest
	if err := json.Unmarshal(fixedBytes, &req); err != nil {
		t.Fatalf("Unmarshal with fix failed: %v", err)
	}
	t.Log("Unmarshal with fix successful")
}

