package auth

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestFactoryModelRouting(t *testing.T) {
	cfg := &config.Config{FactoryKey: []config.FactoryKey{{APIKey: "fake", Models: []config.FactoryModel{{Name: "gpt-5", Alias: "chat"}}}}}
	mgr := NewManager(nil, nil, nil)
	mgr.SetConfig(cfg)
	a := &Auth{ID: "factory-test", Provider: "factory", Attributes: map[string]string{"api_key": "fake", "auth_kind": "apikey"}}
	if _, err := mgr.Register(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if got := mgr.lookupAPIKeyUpstreamModel(a.ID, "chat"); got != "gpt-5" {
		t.Fatalf("alias mapped to %q", got)
	}
	slow := &apiKeyModelRoutingSnapshot{config: cfg, aliases: make(apiKeyModelAliasTable)}
	if got := mgr.applyAPIKeyModelAliasWithRouting(slow, a, "chat"); got != "gpt-5" {
		t.Fatalf("slow alias mapped to %q", got)
	}
	if authHasRefreshCredential(a) {
		t.Fatal("API key must not refresh")
	}
	cfg.FactoryKey[0].Models[0].Name = "gpt-5.1"
	mgr.SetConfig(cfg)
	if got := mgr.lookupAPIKeyUpstreamModel(a.ID, "chat"); got != "gpt-5.1" {
		t.Fatalf("reloaded alias mapped to %q", got)
	}
}
