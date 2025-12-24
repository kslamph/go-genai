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
