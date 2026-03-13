package compose

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type composeFile struct {
	Services map[string]any `yaml:"services"`
}

// ServiceNames parses a docker-compose file and returns all service names.
func ServiceNames(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read compose file: %w", err)
	}

	var cf composeFile
	if err := yaml.Unmarshal(data, &cf); err != nil {
		return nil, fmt.Errorf("parse compose file: %w", err)
	}

	if len(cf.Services) == 0 {
		return nil, fmt.Errorf("compose file has no services")
	}

	names := make([]string, 0, len(cf.Services))
	for name := range cf.Services {
		names = append(names, name)
	}
	return names, nil
}
