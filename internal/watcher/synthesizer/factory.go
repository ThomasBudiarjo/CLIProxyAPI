package synthesizer

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func (s *ConfigSynthesizer) synthesizeFactoryKeys(ctx *SynthesisContext) []*coreauth.Auth {
	entries := make([]config.CodexKey, len(ctx.Config.FactoryKey))
	for i, k := range ctx.Config.FactoryKey {
		base := k.BaseURL
		if base == "" {
			base = "https://api.factory.ai"
		}
		entries[i] = config.CodexKey{APIKey: k.APIKey, BaseURL: base, Prefix: k.Prefix, ProxyURL: k.ProxyURL, Priority: k.Priority, Weight: k.Weight, Headers: k.Headers, DisableCooling: k.DisableCooling, RequestRetry: k.RequestRetry, RequestScopedErrors: k.RequestScopedErrors}
	}
	auths := s.synthesizeCodexStyleKeys(ctx, entries, "factory")
	for i, a := range auths {
		k := ctx.Config.FactoryKey[i]
		models, _ := json.Marshal(k.Models)
		cloak := config.FactoryCloakConfig{Mode: "auto", PromptProfile: "interactive"}
		if k.Cloak != nil {
			cloak = *k.Cloak
		}
		if cloak.Mode == "" {
			cloak.Mode = "auto"
		}
		if cloak.PromptProfile == "" {
			cloak.PromptProfile = "interactive"
		}
		encodedCloak, _ := json.Marshal(cloak)
		a.Attributes["factory_models"] = string(models)
		a.Attributes["factory_cloak"] = string(encodedCloak)
		a.Attributes["models_hash"] = fmt.Sprintf("%x", sha256.Sum256(models))
	}
	return auths
}
