package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/sunbankio/omniproxy/internal/auth"
	"github.com/sunbankio/omniproxy/internal/manager"
	"github.com/sunbankio/omniproxy/internal/provider"
	"github.com/sunbankio/omniproxy/internal/router"
	"github.com/sunbankio/omniproxy/pkg/utils"
	"google.golang.org/genai"
)

// GeminiRequest represents the incoming Gemini API request structure
type GeminiRequest struct {
	Contents          []*optimizedContent          `json:"contents"`
	Tools             []*genai.Tool                `json:"tools,omitempty"`
	ToolConfig        *genai.ToolConfig            `json:"toolConfig,omitempty"`
	SafetySettings    []*genai.SafetySetting       `json:"safetySettings,omitempty"`
	SystemInstruction *optimizedContent            `json:"systemInstruction,omitempty"`
	GenerationConfig  *genai.GenerateContentConfig `json:"generationConfig,omitempty"`
	CachedContent     string                       `json:"cachedContent,omitempty"`
}

type optimizedContent struct {
	Parts []*optimizedPart `json:"parts,omitempty"`
	Role  string           `json:"role,omitempty"`
}

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

type thoughtSignature []byte

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

func (r *GeminiRequest) toGenAIConfig() *genai.GenerateContentConfig {
	config := r.GenerationConfig
	if config == nil {
		config = &genai.GenerateContentConfig{}
	}

	// Map top-level fields to the config if they are present
	if r.SystemInstruction != nil {
		config.SystemInstruction = r.SystemInstruction.toGenAI()
	}
	if len(r.Tools) > 0 {
		config.Tools = r.Tools

		// Set toolConfig - use the one from request if present, otherwise add a default
		// This is required by the Gemini API when tools are present
		if r.ToolConfig != nil {
			config.ToolConfig = r.ToolConfig
		} else {
			config.ToolConfig = &genai.ToolConfig{
				FunctionCallingConfig: &genai.FunctionCallingConfig{
					Mode: genai.FunctionCallingConfigModeAuto,
				},
			}
		}
	}
	if len(r.SafetySettings) > 0 {
		config.SafetySettings = r.SafetySettings
	}
	if r.CachedContent != "" {
		config.CachedContent = r.CachedContent
	}

	return config
}

func (r *GeminiRequest) toGenAIContents() []*genai.Content {
	contents := make([]*genai.Content, len(r.Contents))
	for i, c := range r.Contents {
		contents[i] = c.toGenAI()
	}
	return contents
}

type ServerV2 struct {
	sr       *router.SmartRouterV2
	registry *manager.Registry
	router   *chi.Mux
}

func NewServerV2(sr *router.SmartRouterV2, registry *manager.Registry) *ServerV2 {
	s := &ServerV2{
		sr:       sr,
		registry: registry,
		router:   chi.NewRouter(),
	}
	s.setupRoutes()
	return s
}

func (s *ServerV2) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}

func (s *ServerV2) setupRoutes() {
	s.router.Use(middleware.RequestID)
	s.router.Use(middleware.RealIP)
	s.router.Use(middleware.Logger)
	s.router.Use(middleware.Recoverer)

	// OpenAI-compatible API
	s.router.Route("/v1", func(r chi.Router) {
		r.Post("/chat/completions", s.HandleOpenAIChatCompletions)
		r.Get("/models", s.HandleListModels)
	})

	// Gemini V1Beta API
	s.router.Route("/v1beta", func(r chi.Router) {
		r.Get("/models", s.HandleGeminiListModels)
		r.Post("/models/{model}:generateContent", s.HandleGeminiGenerateContent)
		r.Post("/models/{model}:streamGenerateContent", s.HandleGeminiStreamGenerateContent)
	})

	s.router.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})
}

// Handlers

func (s *ServerV2) HandleGeminiGenerateContent(w http.ResponseWriter, r *http.Request) {
	modelName := chi.URLParam(r, "model")

	// Read request body
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		utils.L().Errorf("Failed to read request body: %v", err)
		http.Error(w, "failed to read request body", http.StatusInternalServerError)
		return
	}
	defer r.Body.Close()

	// Parse the request directly into structured types (like old implementation)
	var req GeminiRequest
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		utils.L().Errorf("Failed to decode Gemini request: %v", err)
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Validate request
	if modelName == "" {
		http.Error(w, "model is required", http.StatusBadRequest)
		return
	}
	if len(req.Contents) == 0 {
		http.Error(w, "contents is required", http.StatusBadRequest)
		return
	}

	// Create router request with structured types (not map)
	routerReq := &router.Request{
		Protocol: provider.ProtocolGemini,
		Model:    modelName,
		Payload: map[string]interface{}{
			"request":          &req,
			"contents":         req.toGenAIContents(),
			"generationConfig": req.toGenAIConfig(),
		},
		Headers:  make(map[string]string),
		IsStream: false,
	}

	// Copy headers
	for key, values := range r.Header {
		if len(values) > 0 {
			routerReq.Headers[key] = values[0]
		}
	}

	resp, err := s.sr.Execute(r.Context(), routerReq)
	if err != nil {
		s.handleError(w, err)
		return
	}
	defer resp.Body.Close()

	// Copy Response Headers
	for k, v := range resp.Headers {
		w.Header().Set(k, v)
	}
	w.Header().Set("x-goog-api-client", "genai-go")
	w.WriteHeader(resp.StatusCode)
	
	// Stream Body
	io.Copy(w, resp.Body)
}

func (s *ServerV2) HandleGeminiStreamGenerateContent(w http.ResponseWriter, r *http.Request) {
	modelName := chi.URLParam(r, "model")

	// Read request body
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		utils.L().Errorf("Failed to read request body: %v", err)
		http.Error(w, "failed to read request body", http.StatusInternalServerError)
		return
	}
	defer r.Body.Close()

	// Parse the request directly into structured types (like old implementation)
	var req GeminiRequest
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		utils.L().Errorf("Failed to decode Gemini request: %v", err)
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Validate request
	if modelName == "" {
		http.Error(w, "model is required", http.StatusBadRequest)
		return
	}
	if len(req.Contents) == 0 {
		http.Error(w, "contents is required", http.StatusBadRequest)
		return
	}

	// Create router request with structured types (not map)
	routerReq := &router.Request{
		Protocol: provider.ProtocolGemini,
		Model:    modelName,
		Payload: map[string]interface{}{
			"request":          &req,
			"contents":         req.toGenAIContents(),
			"generationConfig": req.toGenAIConfig(),
		},
		Headers:  make(map[string]string),
		IsStream: true,
	}

	// Copy headers
	for key, values := range r.Header {
		if len(values) > 0 {
			routerReq.Headers[key] = values[0]
		}
	}

	resp, err := s.sr.Execute(r.Context(), routerReq)
	if err != nil {
		s.handleError(w, err)
		return
	}
	defer resp.Body.Close()

	// Copy Response Headers
	for k, v := range resp.Headers {
		w.Header().Set(k, v)
	}
	w.Header().Set("x-goog-api-client", "genai-go")
	w.WriteHeader(resp.StatusCode)
	
	// Stream Body
	io.Copy(w, resp.Body)
}

func (s *ServerV2) HandleOpenAIChatCompletions(w http.ResponseWriter, r *http.Request) {
	// Read Body
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var payload map[string]interface{}
	if len(bodyBytes) > 0 {
		if err := json.Unmarshal(bodyBytes, &payload); err != nil {
			http.Error(w, "Invalid JSON payload", http.StatusBadRequest)
			return
		}
	}

	// Extract model from payload
	model, _ := payload["model"].(string)
	if model == "" {
		http.Error(w, "model is required", http.StatusBadRequest)
		return
	}

	// Detect if streaming from payload
	isStream := false
	if stream, ok := payload["stream"].(bool); ok {
		isStream = stream
	}

	req := &router.Request{
		Protocol: provider.ProtocolOpenAI,
		Model:    model,
		Payload:  payload,
		IsStream: isStream,
		Headers:  map[string]string{},
	}
	
	// Copy headers
	for k, v := range r.Header {
		if len(v) > 0 {
			req.Headers[k] = v[0]
		}
	}

	resp, err := s.sr.Execute(r.Context(), req)
	if err != nil {
		s.handleError(w, err)
		return
	}
	defer resp.Body.Close()

	// Copy Response Headers
	for k, v := range resp.Headers {
		w.Header().Set(k, v)
	}
	w.WriteHeader(resp.StatusCode)
	
	// Stream Body
	io.Copy(w, resp.Body)
}

func (s *ServerV2) HandleListModels(w http.ResponseWriter, r *http.Request) {
	// Get all models from the registry
	allModels := s.registry.ListModels()

	// Filter to only include OpenAI-compatible models (exclude Gemini and Antigravity)
	models := make([]string, 0, len(allModels))
	for _, model := range allModels {
		// Check if this model belongs to an OpenAI-compatible provider
		// by getting the pool and checking the credential type
		pool := s.registry.GetPool(model)
		if pool != nil {
			creds := pool.List()
			if len(creds) > 0 {
				// Only include models from Qwen or IFlow providers
				if creds[0].ProviderType == auth.ProviderTypeQwen || creds[0].ProviderType == auth.ProviderTypeIFlow {
					models = append(models, model)
				}
			}
		}
	}

	// Model specifications based on iFlow platform data for OpenAI format
	modelSpecs := map[string]struct {
		contextLength int
		maxTokens     int
		description   string
	}{
		"qwen3-coder-plus":           {1048576, 65536, "480B MoE coding model"},
		"qwen3-coder-flash":          {1048576, 65536, "Fast coding model with same specs as Plus"},
		"qwen3-vl-plus":              {262144, 32768, "Vision-language model"},
		"qwen3-max":                  {262144, 32768, "Advanced Qwen3 model"},
		"qwen3-max-preview":          {262144, 32768, "Preview version of Qwen3 Max"},
		"kimi-k2-instruct-0905":      {262144, 65536, "320B MoE instruction model"},
		"kimi-k2":                    {131072, 65536, "1T MoE foundation model"},
		"deepseek-v3.2-exp":          {131072, 65536, "Experimental sparse attention model"},
		"deepseek-r1":                {131072, 32768, "Reasoning-optimized model"},
		"deepseek-v3-671b":           {131072, 32768, "671B parameter model"},
		"glm-4.6":                    {204800, 131072, "Thinking-enabled multimodal model"},
		"qwen3-32b":                  {131072, 32768, "32B parameter model"},
		"qwen3-235b-a22b-thinking":   {262144, 65536, "Thinking MoE model"},
		"qwen3-235b-a22b-instruct":   {262144, 65536, "Instruction-tuned MoE model"},
		"qwen3-235b-a22b":            {131072, 32768, "Base MoE model"},
		"chat_20706":                  {131072, 32768, "Chat model variant"},
		"chat_23310":                  {131072, 32768, "Chat model variant"},
		"rev19-uic3-1p":              {131072, 32768, "Specialized model"},
		"gpt-oss-120b-medium":        {131072, 32768, "Open-source 120B model"},
		"claude-opus-4-5-thinking":   {131072, 32768, "Thinking-enhanced Claude"},
		"claude-sonnet-4-5":          {131072, 32768, "Advanced Claude model"},
		"claude-sonnet-4-5-thinking": {131072, 32768, "Thinking-enhanced Sonnet"},
	}

	// Create response in OpenAI format with enhanced specifications
	data := make([]map[string]interface{}, 0, len(models))
	for _, model := range models {
		modelInfo := map[string]interface{}{
			"id":       model,
			"object":   "model",
			"created":  0,
			"owned_by": "omniproxy",
		}

		// Add enhanced specifications if available
		if spec, exists := modelSpecs[model]; exists {
			modelInfo["context_length"] = spec.contextLength
			modelInfo["max_tokens"] = spec.maxTokens
			modelInfo["description"] = spec.description
		}

		data = append(data, modelInfo)
	}

	response := map[string]interface{}{
		"object": "list",
		"data":   data,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func (s *ServerV2) HandleGeminiListModels(w http.ResponseWriter, r *http.Request) {
	// Get all models from the registry
	allModels := s.registry.ListModels()

	// Filter to only include Gemini and Antigravity models (exclude OpenAI-compatible providers)
	models := make([]string, 0, len(allModels))
	for _, model := range allModels {
		// Check if this model belongs to a Gemini-compatible provider
		// by getting the pool and checking the credential type
		pool := s.registry.GetPool(model)
		if pool != nil {
			creds := pool.List()
			if len(creds) > 0 {
				// Only include models from Gemini or Antigravity providers
				if creds[0].ProviderType == auth.ProviderTypeGemini || creds[0].ProviderType == auth.ProviderTypeAntigravity {
					models = append(models, model)
				}
			}
		}
	}

	// Model specifications for Gemini and Antigravity providers only
	modelSpecs := map[string]struct {
		inputTokenLimit    int
		outputTokenLimit   int
		displayName        string
		description        string
		thinking           bool
		namespace          string
	}{
		// Gemini models
		"gemini-1.5-flash":           {1048576, 8192, "Gemini 1.5 Flash", "Fast and efficient multimodal model", false, "Gemini-1.5"},
		"gemini-1.5-pro":             {2097152, 8192, "Gemini 1.5 Pro", "High-performance multimodal model", false, "Gemini-1.5"},
		"gemini-1.5-flash-8b":        {1048576, 8192, "Gemini 1.5 Flash 8B", "Lightweight fast model", false, "Gemini-1.5"},
		"gemini-2.0-flash":           {1048576, 8192, "Gemini 2.0 Flash", "Next-generation fast model", false, "Gemini-2.0"},
		"gemini-2.0-flash-exp":       {1048576, 8192, "Gemini 2.0 Flash Exp", "Experimental next-gen model", false, "Gemini-2.0"},
		"gemini-pro":                  {32768, 4096, "Gemini Pro", "Original Gemini model", false, "models"},
		"gemini-pro-vision":          {16384, 4096, "Gemini Pro Vision", "Multimodal vision model", false, "models"},
		
		// Antigravity models (based on iFlow data - these are Gemini-compatible models served via Antigravity)
		"gemini-3-pro-low":           {1048576, 8192, "Gemini 3 Pro Low", "Low-latency Gemini 3 model", false, "Gemini-3"},
		"gemini-2.5-pro":             {1048576, 8192, "Gemini 2.5 Pro", "Enhanced Gemini 2.5 model", false, "Gemini-2.5"},
		"gemini-3-pro-high":          {1048576, 8192, "Gemini 3 Pro High", "High-performance Gemini 3", false, "Gemini-3"},
		"gemini-3-flash":             {1048576, 8192, "Gemini 3 Flash", "Fast Gemini 3 model", false, "Gemini-3"},
		"gemini-2.5-flash":           {1048576, 8192, "Gemini 2.5 Flash", "Fast Gemini 2.5 model", false, "Gemini-2.5"},
		"gemini-2.5-flash-lite":      {1048576, 8192, "Gemini 2.5 Flash Lite", "Lightweight Gemini 2.5", false, "Gemini-2.5"},
		"gemini-2.5-flash-thinking": {1048576, 8192, "Gemini 2.5 Flash Thinking", "Reasoning-enhanced model", true, "Gemini-2.5"},
		"gemini-3-pro-image":         {1048576, 8192, "Gemini 3 Pro Image", "Image generation model", false, "Gemini-3"},
		
		// OpenAI models (via Antigravity)
		"gpt-oss-120b-medium":        {131072, 32768, "GPT OSS 120B Medium", "Open-source 120B model", false, "OpenAI"},
		
		// Anthropic models (via Antigravity)
		"claude-opus-4-5-thinking":   {131072, 32768, "Claude Opus 4.5 Thinking", "Thinking-enhanced Claude", true, "Anthropic"},
		"claude-sonnet-4-5":          {131072, 32768, "Claude Sonnet 4.5", "Advanced Claude model", false, "Anthropic"},
		"claude-sonnet-4-5-thinking": {131072, 32768, "Claude Sonnet 4.5 Thinking", "Thinking-enhanced Sonnet", true, "Anthropic"},
	}

	// Create response in Google AI API format
	geminiModels := make([]map[string]interface{}, 0, len(models))
	for _, model := range models {
		// Get model specifications, fallback to defaults if not found
		spec, exists := modelSpecs[model]
		if !exists {
			spec = struct {
				inputTokenLimit    int
				outputTokenLimit   int
				displayName        string
				description        string
				thinking           bool
				namespace          string
			}{
				inputTokenLimit:  1048576, // Default 1M tokens
				outputTokenLimit: 8192,    // Default 8K tokens
				displayName:     fmt.Sprintf("Model %s", model),
				description:     fmt.Sprintf("Generative AI model: %s", model),
				thinking:        false,
				namespace:       "models",
			}
		}

		// Extract base model name and version from the model identifier
		baseModelId := model
		version := "001"
		
		// Try to parse version if model contains version suffix
		if idx := strings.LastIndex(model, "-"); idx > 0 && idx < len(model)-1 {
			suffix := model[idx+1:]
			if _, err := strconv.Atoi(suffix); err == nil {
				baseModelId = model[:idx]
				version = suffix
			}
		}

		geminiModel := map[string]interface{}{
			"name":                         fmt.Sprintf("%s/%s", spec.namespace, model),
			"baseModelId":                  baseModelId,
			"version":                      version,
			"displayName":                  spec.displayName,
			"description":                  spec.description,
			"inputTokenLimit":              spec.inputTokenLimit,
			"outputTokenLimit":             spec.outputTokenLimit,
			"supportedGenerationMethods":   []string{"generateContent", "streamGenerateContent"},
			"thinking":                     spec.thinking,
			"temperature":                  1.0,
			"maxTemperature":               2.0,
			"topP":                         0.95,
			"topK":                         40,
		}
		geminiModels = append(geminiModels, geminiModel)
	}

	response := map[string]interface{}{
		"models": geminiModels,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func (s *ServerV2) handleError(w http.ResponseWriter, err error) {
	if pErr, ok := err.(*provider.ProviderError); ok {
		utils.L().Warnf("Request failed: %v", pErr)
		
		// Gemini Error Format
		errResp := map[string]interface{}{
			"error": map[string]interface{}{
				"code":    pErr.StatusCode,
				"message": pErr.Message,
				"status":  http.StatusText(pErr.StatusCode),
			},
		}
		
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(pErr.StatusCode)
		json.NewEncoder(w).Encode(errResp)
		return
	}

	utils.L().Errorf("Internal error: %v", err)
	http.Error(w, "Internal Server Error", http.StatusInternalServerError)
}
