package manager

import (
	"github.com/sunbankio/omniproxy/internal/provider"
)

// Profile represents the configuration required to instantiate a Provider.
// It maps a specific credential/config to a Provider Identity.
// A Profile conceptually represents "User X on Service Y".
type Profile struct {
	// Name is a unique identifier for this profile (e.g., "gemini-0")
	Name string
	
	// ProviderType is the type of the underlying service (e.g., gemini, antigravity)
	ProviderType provider.ProviderType
	
	// CredentialPath is the file path to the credentials for this profile
	CredentialPath string
}

// NewProfile creates a new Profile instance
func NewProfile(name string, providerType provider.ProviderType, credPath string) *Profile {
	return &Profile{
		Name:           name,
		ProviderType:   providerType,
		CredentialPath: credPath,
	}
}
