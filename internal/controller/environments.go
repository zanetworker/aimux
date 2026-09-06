package controller

import (
	"fmt"

	"github.com/zanetworker/aimux/internal/config"
)

// AddEnvironment validates and persists a new environment to config.yaml.
func AddEnvironment(configPath, name string, env config.EnvironmentConfig) error {
	if err := config.ValidateEnvironmentName(name); err != nil {
		return fmt.Errorf("add environment: %w", err)
	}
	if err := config.ValidateEnvironmentConfig(env); err != nil {
		return fmt.Errorf("add environment %q: %w", name, err)
	}
	return config.AddEnvironmentToConfig(configPath, name, env)
}

// RemoveEnvironment removes a named environment from config.yaml.
// The built-in "local" environment cannot be removed.
func RemoveEnvironment(configPath, name string) error {
	if name == "local" {
		return fmt.Errorf("remove environment: cannot remove built-in \"local\" environment")
	}
	if err := config.ValidateEnvironmentName(name); err != nil {
		return fmt.Errorf("remove environment: %w", err)
	}
	return config.RemoveEnvironmentFromConfig(configPath, name)
}
