package cliproxy

import (
	"context"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestFactoryCatalogExplicitModelsAndPrefix(t *testing.T) {
	cfg := &config.Config{FactoryKey: []config.FactoryKey{{APIKey: "fake", Models: []config.FactoryModel{{Name: "gpt-5", Alias: "chat", MaxContextLength: 120000}}}}}
	cfg.ForceModelPrefix = true
	service := &Service{cfg: cfg}
	a := &coreauth.Auth{ID: "factory-catalog-test", Provider: "factory", Prefix: "team", Attributes: map[string]string{"source": "config:factory[test]", "api_key": "fake", "config_index": "0", "auth_kind": "apikey"}}
	reg := GlobalModelRegistry()
	t.Cleanup(func() { reg.UnregisterClient(a.ID) })
	service.registerModelsForAuth(context.Background(), a)
	models := reg.GetModelsForClient(a.ID)
	if len(models) != 1 || models[0].ID != "team/chat" {
		t.Fatalf("unexpected catalog: %+v", models)
	}
	cfg.FactoryKey[0].Models = nil
	service.registerModelsForAuth(context.Background(), a)
	if got := reg.GetModelsForClient(a.ID); len(got) != 0 {
		t.Fatal("empty catalog invented models")
	}
}
