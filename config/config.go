package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
	"github.com/google/go-containerregistry/pkg/name"
)

type Config struct {
	Stacks         []Stack `toml:"stacks"`
	DefaultStackID string  `toml:"default-stack-id"`
}

type Stack struct {
	ID          string   `toml:"id"`
	BuildImages []string `toml:"build-images"`
	RunImages   []string `toml:"run-images"`
}

func New(path string) (*Config, error) {
	configPath := filepath.Join(path, "config.toml")
	config, err := previousConfig(path)
	if err != nil {
		return nil, err
	}

	if config.DefaultStackID == "" {
		config.DefaultStackID = "io.buildpacks.stacks.bionic"
	}
	appendStackIfMissing(config, Stack{
		ID:          "io.buildpacks.stacks.bionic",
		BuildImages: []string{"packs/build"},
		RunImages:   []string{"packs/run"},
	})

	if err := os.MkdirAll(filepath.Dir(configPath), 0777); err != nil {
		return nil, err
	}
	w, err := os.OpenFile(configPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, err
	}
	defer w.Close()

	if err := toml.NewEncoder(w).Encode(config); err != nil {
		return nil, err
	}

	return config, nil
}

func previousConfig(path string) (*Config, error) {
	configPath := filepath.Join(path, "config.toml")
	config := &Config{}
	_, err := toml.DecodeFile(configPath, config)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return config, nil
}

func appendStackIfMissing(config *Config, stack Stack) {
	for _, stk := range config.Stacks {
		if stk.ID == stack.ID {
			return
		}
	}
	config.Stacks = append(config.Stacks, stack)
}

func (c *Config) Get(stackID string) (*Stack, error) {
	if stackID == "" {
		stackID = c.DefaultStackID
	}
	for _, stack := range c.Stacks {
		if stack.ID == stackID {
			return &stack, nil
		}
	}
	return nil, fmt.Errorf(`Missing stack: stack with id "%s" not found in pack config.toml`, stackID)
}

func ImageByRegistry(registry string, images []string) (string, error) {
	if len(images) == 0 {
		return "", errors.New("empty images")
	}
	for _, i := range images {
		ref, err := name.ParseReference(i, name.WeakValidation)
		if err != nil {
			continue
		}
		reg := ref.Context().RegistryStr()
		if registry == reg {
			return i, nil
		}
	}
	return images[0], nil
}
