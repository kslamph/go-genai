package provider

// ProviderType represents the type of AI provider.
type ProviderType string

const (
	ProviderGemini      ProviderType = "gemini"
	ProviderAntigravity ProviderType = "antigravity"
	ProviderQwen        ProviderType = "qwen"
	ProviderIFlow       ProviderType = "iflow"
	ProviderOpenAI      ProviderType = "openai" //not in use currently
	ProviderKiro        ProviderType = "kiro"
)
