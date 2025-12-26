package config

import (
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config represents the application configuration
type Config struct {
	// Credentials maps provider types to lists of file paths
	Credentials map[string][]string `yaml:"credentials"`
}

// GetDefaultConfigPath returns the default config file path
func GetDefaultConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".omniproxy", "omniproxy.yaml")
}

// LoadConfig loads the configuration from a YAML file
func LoadConfig(path string) (*Config, error) {
	config := &Config{
		Credentials: make(map[string][]string),
	}

	if path == "" {
		return config, nil
	}

	// Handle tilde expansion for the config file path itself
	if strings.HasPrefix(path, "~") {
		home, _ := os.UserHomeDir()
		path = filepath.Join(home, path[1:])
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return config, nil
		}
		return nil, err
	}

	if err := yaml.Unmarshal(data, config); err != nil {
		return nil, err
	}

	// Expand paths in the credentials map
	for provider, paths := range config.Credentials {
		var expandedPaths []string
		for _, p := range paths {
			if strings.HasPrefix(p, "~") {
				home, _ := os.UserHomeDir()
				p = filepath.Join(home, p[1:])
			}
			expandedPaths = append(expandedPaths, p)
		}
		config.Credentials[provider] = expandedPaths
	}

	return config, nil
}

// SaveConfig saves the configuration to a YAML file
func SaveConfig(path string, config *Config) error {
	// Handle tilde expansion
	if strings.HasPrefix(path, "~") {
		home, _ := os.UserHomeDir()
		path = filepath.Join(home, path[1:])
	}

	// Ensure directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	// Convert absolute paths back to tilde notation for cleaner config
	home, _ := os.UserHomeDir()
	configToSave := &Config{
		Credentials: make(map[string][]string),
	}
	for provider, paths := range config.Credentials {
		var relativePaths []string
		for _, p := range paths {
			if strings.HasPrefix(p, home) {
				p = "~" + strings.TrimPrefix(p, home)
			}
			relativePaths = append(relativePaths, p)
		}
		configToSave.Credentials[provider] = relativePaths
	}

	data, err := yaml.Marshal(configToSave)
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644)
}

// AddCredential adds a credential path to the config for a given provider
func AddCredential(config *Config, provider, credPath string) {
	if config.Credentials == nil {
		config.Credentials = make(map[string][]string)
	}

	// Check if credential already exists
	for _, existing := range config.Credentials[provider] {
		if existing == credPath {
			return // Already exists
		}
	}

	config.Credentials[provider] = append(config.Credentials[provider], credPath)
}
