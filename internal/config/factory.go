package config

import (
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

// FactoryKey configures one native Factory credential and its explicit model catalog.
type FactoryKey struct {
	APIKey              string                   `yaml:"api-key" json:"api-key"`
	BaseURL             string                   `yaml:"base-url" json:"base-url"`
	Prefix              string                   `yaml:"prefix,omitempty" json:"prefix,omitempty"`
	ProxyURL            string                   `yaml:"proxy-url,omitempty" json:"proxy-url,omitempty"`
	Priority            int                      `yaml:"priority,omitempty" json:"priority,omitempty"`
	Weight              *int                     `yaml:"weight,omitempty" json:"weight,omitempty"`
	Headers             map[string]string        `yaml:"headers,omitempty" json:"headers,omitempty"`
	DisableCooling      *bool                    `yaml:"disable-cooling,omitempty" json:"disable-cooling,omitempty"`
	RequestRetry        *int                     `yaml:"request-retry,omitempty" json:"request-retry,omitempty"`
	RequestScopedErrors []RequestScopedErrorRule `yaml:"request-scoped-errors,omitempty" json:"request-scoped-errors,omitempty"`
	Models              []FactoryModel           `yaml:"models" json:"models"`
	Cloak               *FactoryCloakConfig      `yaml:"cloak,omitempty" json:"cloak,omitempty"`
}

type FactoryModel struct {
	Name             string                    `yaml:"name" json:"name"`
	Alias            string                    `yaml:"alias" json:"alias"`
	Protocol         string                    `yaml:"protocol,omitempty" json:"protocol,omitempty"`
	APIProvider      string                    `yaml:"api-provider,omitempty" json:"api-provider,omitempty"`
	Thinking         *registry.ThinkingSupport `yaml:"thinking,omitempty" json:"thinking,omitempty"`
	MaxContextLength int                       `yaml:"max-context-length,omitempty" json:"max-context-length,omitempty"`
}

type FactoryCloakConfig struct {
	Mode          string `yaml:"mode" json:"mode"`
	StrictMode    bool   `yaml:"strict-mode" json:"strict-mode"`
	PromptProfile string `yaml:"prompt-profile" json:"prompt-profile"`
}

func (k FactoryKey) GetAPIKey() string                        { return k.APIKey }
func (k FactoryKey) GetBaseURL() string                       { return k.BaseURL }
func (k FactoryKey) GetPrefix() string                        { return k.Prefix }
func (k FactoryKey) GetProxyURL() string                      { return k.ProxyURL }
func (m FactoryModel) GetName() string                        { return m.Name }
func (m FactoryModel) GetAlias() string                       { return m.Alias }
func (m FactoryModel) GetDisplayName() string                 { return "" }
func (m FactoryModel) GetThinking() *registry.ThinkingSupport { return m.Thinking }
func (m FactoryModel) GetMaxContextLength() int               { return m.MaxContextLength }
func (m FactoryModel) GetForceMapping() bool                  { return false }

// SanitizeFactoryKeys validates enums before dropping empty credentials.
func (cfg *Config) SanitizeFactoryKeys() error {
	out := make([]FactoryKey, 0, len(cfg.FactoryKey))
	for i, entry := range cfg.FactoryKey {
		if entry.Cloak == nil {
			entry.Cloak = &FactoryCloakConfig{}
		}
		cloak := *entry.Cloak
		cloak.Mode = strings.ToLower(strings.TrimSpace(cloak.Mode))
		cloak.PromptProfile = strings.ToLower(strings.TrimSpace(cloak.PromptProfile))
		if cloak.Mode == "" {
			cloak.Mode = "auto"
		}
		if cloak.PromptProfile == "" {
			cloak.PromptProfile = "interactive"
		}
		if cloak.Mode != "auto" && cloak.Mode != "always" && cloak.Mode != "never" {
			return fmt.Errorf("factory-api-key[%d]: invalid cloak mode", i)
		}
		if cloak.PromptProfile != "interactive" && cloak.PromptProfile != "noninteractive" {
			return fmt.Errorf("factory-api-key[%d]: invalid prompt profile", i)
		}
		entry.Cloak = &cloak
		for j := range entry.Models {
			m := &entry.Models[j]
			m.Name = strings.TrimSpace(m.Name)
			m.Alias = strings.TrimSpace(m.Alias)
			m.APIProvider = strings.TrimSpace(m.APIProvider)
			m.Protocol = strings.ToLower(strings.TrimSpace(m.Protocol))
			switch m.Protocol {
			case "", "claude", "responses", "openai", "gemini":
			default:
				return fmt.Errorf("factory-api-key[%d].models[%d]: invalid protocol", i, j)
			}
		}
		entry.APIKey = strings.TrimSpace(entry.APIKey)
		entry.BaseURL = strings.TrimSpace(entry.BaseURL)
		if entry.BaseURL == "" {
			entry.BaseURL = "https://api.factory.ai"
		}
		entry.Prefix = normalizeModelPrefix(entry.Prefix)
		entry.ProxyURL = strings.TrimSpace(entry.ProxyURL)
		entry.Headers = NormalizeHeaders(entry.Headers)
		if entry.APIKey != "" {
			out = append(out, entry)
		}
	}
	cfg.FactoryKey = out
	return nil
}
