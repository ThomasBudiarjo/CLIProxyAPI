package synthesizer

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestFactorySynthesisReload(t *testing.T) {
	cfg, err := config.ParseConfigBytes([]byte("factory-api-key:\n - api-key: fake\n   prefix: team\n   models: [{name: gpt-5, alias: chat}]\n"))
	if err != nil {
		t.Fatal(err)
	}
	synth := NewConfigSynthesizer()
	ctx := &SynthesisContext{Config: cfg, Now: time.Now(), IDGenerator: NewStableIDGenerator()}
	first, err := synth.Synthesize(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 {
		t.Fatal("missing Factory auth")
	}
	a := first[0]
	if a.Provider != "factory" || a.Prefix != "team" || a.Attributes["factory_models"] == "" || a.Attributes["factory_cloak"] == "" {
		t.Fatal("missing Factory attributes")
	}
	var cloak config.FactoryCloakConfig
	if err := json.Unmarshal([]byte(a.Attributes["factory_cloak"]), &cloak); err != nil {
		t.Fatal(err)
	}
	if cloak.Mode != "auto" || cloak.PromptProfile != "interactive" {
		t.Fatal("incorrect cloak defaults")
	}
	var models []config.FactoryModel
	if err := json.Unmarshal([]byte(a.Attributes["factory_models"]), &models); err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].Name != "gpt-5" {
		t.Fatal("incorrect model serialization")
	}
	cfg.FactoryKey[0].Models[0].Protocol = "openai"
	cfg.FactoryKey[0].Cloak.Mode = "always"
	ctx.IDGenerator = NewStableIDGenerator()
	second, err := synth.Synthesize(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if second[0].ID != a.ID || second[0].Attributes["factory_models"] == a.Attributes["factory_models"] || second[0].Attributes["factory_cloak"] == a.Attributes["factory_cloak"] {
		t.Fatal("reload did not preserve identity and update payload configuration")
	}
}
