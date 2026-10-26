package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestFactoryConfig(t *testing.T) {
	for _, input := range []string{
		"factory-api-key:\n  - api-key: ' fake '\n    models: [{name: claude-sonnet, alias: sonnet}]\n",
		"api-keys:\n  factory:\n    - keys: [{api-key: ' fake '}]\n      models: [{name: claude-sonnet, alias: sonnet}]\n",
	} {
		cfg, err := ParseConfigBytes([]byte(input))
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(cfg)
		var wire map[string]json.RawMessage
		_ = json.Unmarshal(b, &wire)
		var keys []struct {
			APIKey  string `json:"api-key"`
			BaseURL string `json:"base-url"`
		}
		_ = json.Unmarshal(wire["factory-api-key"], &keys)
		if len(keys) != 1 || keys[0].APIKey != "fake" || keys[0].BaseURL != "https://api.factory.ai" {
			t.Fatal("Factory config not parsed and normalized")
		}
		encoded, err := yaml.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		roundtrip, err := ParseConfigBytes(encoded)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(cfg.FactoryKey, roundtrip.FactoryKey) {
			t.Fatal("Factory YAML round trip changed configuration")
		}
		clone := cfg.CloneForRuntime()
		clone.FactoryKey[0].Models[0].Name = "changed"
		clone.FactoryKey[0].Cloak.Mode = "never"
		if cfg.FactoryKey[0].Models[0].Name != "claude-sonnet" || cfg.FactoryKey[0].Cloak.Mode != "auto" {
			t.Fatal("Factory runtime clone shares mutable state")
		}
	}
}

func TestFactoryInvalidConfig(t *testing.T) {
	for _, field := range []string{"cloak: {mode: invalid}", "cloak: {prompt-profile: invalid}", "models: [{name: gpt-5, protocol: invalid}]", "weight: 1000001"} {
		data := []byte("factory-api-key:\n  - api-key: fake\n    " + field + "\n")
		if _, err := ParseConfigBytes(data); err == nil {
			t.Errorf("accepted %s", field)
		}
		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadConfig(path); err == nil {
			t.Errorf("LoadConfig accepted %s", field)
		}
	}
}
