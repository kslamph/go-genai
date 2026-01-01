package router

import (
	"encoding/base64"
	"encoding/json"

	"google.golang.org/genai"
)

// thoughtSignature is a custom type that handles the thought_signature field
// in Gemini API requests. It implements custom JSON unmarshaling to handle
// both base64-encoded strings and plain strings.
type thoughtSignature []byte

// UnmarshalJSON implements custom JSON unmarshaling for thoughtSignature.
// It handles both base64-encoded strings and plain strings.
func (ts *thoughtSignature) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	// Try base64 decoding
	decoded, err := base64.StdEncoding.DecodeString(s)
	if err == nil {
		*ts = decoded
		return nil
	}
	// If not base64, it's a plain string
	*ts = []byte(s)
	return nil
}

// MarshalJSON implements custom JSON marshaling for thoughtSignature.
// It encodes the bytes as a base64 string.
func (ts thoughtSignature) MarshalJSON() ([]byte, error) {
	if len(ts) == 0 {
		return []byte("null"), nil
	}
	return json.Marshal(base64.StdEncoding.EncodeToString(ts))
}

// optimizedContent is a custom content structure that handles thought_signature
// properly through custom unmarshaling.
type optimizedContent struct {
	Parts []*optimizedPart `json:"parts,omitempty"`
	Role  string           `json:"role,omitempty"`
}

// toGenAI converts optimizedContent to genai.Content
func (oc *optimizedContent) toGenAI() *genai.Content {
	if oc == nil {
		return nil
	}
	parts := make([]*genai.Part, len(oc.Parts))
	for i, p := range oc.Parts {
		parts[i] = p.toGenAI()
	}
	return &genai.Content{
		Parts: parts,
		Role:  oc.Role,
	}
}

// optimizedPart is a custom part structure that handles thought_signature
// properly through custom unmarshaling.
type optimizedPart struct {
	MediaResolution     *genai.PartMediaResolution `json:"mediaResolution,omitempty"`
	CodeExecutionResult *genai.CodeExecutionResult `json:"codeExecutionResult,omitempty"`
	ExecutableCode      *genai.ExecutableCode      `json:"executableCode,omitempty"`
	FileData            *genai.FileData            `json:"fileData,omitempty"`
	FunctionCall        *genai.FunctionCall        `json:"functionCall,omitempty"`
	FunctionResponse    *genai.FunctionResponse    `json:"functionResponse,omitempty"`
	InlineData          *genai.Blob                `json:"inlineData,omitempty"`
	Text                string                     `json:"text,omitempty"`
	Thought             bool                       `json:"thought,omitempty"`
	ThoughtSignature    thoughtSignature           `json:"thoughtSignature,omitempty"`
	VideoMetadata       *genai.VideoMetadata       `json:"videoMetadata,omitempty"`
}

// toGenAI converts optimizedPart to genai.Part
func (op *optimizedPart) toGenAI() *genai.Part {
	if op == nil {
		return nil
	}
	return &genai.Part{
		MediaResolution:     op.MediaResolution,
		CodeExecutionResult: op.CodeExecutionResult,
		ExecutableCode:      op.ExecutableCode,
		FileData:            op.FileData,
		FunctionCall:        op.FunctionCall,
		FunctionResponse:    op.FunctionResponse,
		InlineData:          op.InlineData,
		Text:                op.Text,
		Thought:             op.Thought,
		ThoughtSignature:    []byte(op.ThoughtSignature),
		VideoMetadata:       op.VideoMetadata,
	}
}