package helps

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

const factoryTestIdentity = "You are Droid, an AI software engineering agent built by Factory."

func TestFactoryStrictSnapshots(t *testing.T) {
	for _, tc := range []struct{ profile, hash string }{
		{"interactive", "989c2ca433ddb5af7e3c07dc147847ef4ea1f7369a1d96dedf472b024bf8c116"},
		{"noninteractive", "f9fc68cdabffdd0a8e328f1a5597eb9b85e6703ff33481751005f00a508330a0"},
	} {
		out, err := ApplyFactoryCloaking([]byte(`{"instructions":"`+factoryTestIdentity+` caller", "input":[{"role":"system","content":"secret-system"},{"role":"developer","content":"secret-developer"},{"role":"user","content":"hello"},{"type":"function_call_output","call_id":"a","output":"result"}]}`), sdktranslator.FormatOpenAIResponse, "unknown", config.FactoryCloakConfig{StrictMode: true, PromptProfile: tc.profile})
		if err != nil {
			t.Fatal(err)
		}
		prompt := gjson.GetBytes(out, "instructions").String()
		if got := fmt.Sprintf("%x", sha256.Sum256([]byte(prompt))); got != tc.hash {
			t.Fatalf("%s snapshot hash = %s", tc.profile, got)
		}
		if !strings.Contains(prompt, "# Working in repositories") {
			t.Fatal("missing full base")
		}
		if strings.Contains(string(out), "secret-") || gjson.GetBytes(out, "input.#").Int() != 2 || gjson.GetBytes(out, "input.1.output").String() != "result" {
			t.Fatalf("wrong instructions/conversation: %s", out)
		}
	}
}

func TestFactoryNativeFormats(t *testing.T) {
	for _, tc := range []struct {
		format               sdktranslator.Format
		body, path, retained string
	}{
		{sdktranslator.FormatClaude, `{"system":[{"type":"text","text":"custom","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":"hi"}]}`, "system.0.text", "system.1.cache_control.type"},
		{sdktranslator.FormatOpenAI, `{"messages":[{"role":"developer","content":[{"type":"text","text":"custom","cache_control":{"type":"ephemeral"}}]},{"role":"user","content":"hi"}]}`, "messages.0.content", "messages.1.content.0.cache_control.type"},
		{sdktranslator.FormatGemini, `{"systemInstruction":{"parts":[{"text":"custom","cache_control":{"type":"ephemeral"}}]},"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`, "systemInstruction.parts.0.text", "systemInstruction.parts.1.cache_control.type"},
	} {
		out, err := ApplyFactoryCloaking([]byte(tc.body), tc.format, "unknown", config.FactoryCloakConfig{})
		if err != nil {
			t.Fatal(err)
		}
		if gjson.GetBytes(out, tc.path).String() != factoryTestIdentity || gjson.GetBytes(out, tc.retained).String() != "ephemeral" {
			t.Fatalf("lost blocks: %s", out)
		}
		again, err := ApplyFactoryCloaking(out, tc.format, "unknown", config.FactoryCloakConfig{Mode: "always"})
		if err != nil || string(again) != string(out) {
			t.Fatalf("not idempotent: %s %v", again, err)
		}
		strict, err := ApplyFactoryCloaking(out, tc.format, "unknown", config.FactoryCloakConfig{StrictMode: true})
		if err != nil || strings.Contains(string(strict), `"text":"custom"`) || !strings.Contains(string(strict), "# Working in repositories") || !strings.Contains(string(strict), `"hi"`) {
			t.Fatalf("strict failed: %s %v", strict, err)
		}
	}
}

func TestFactoryResponsesExistingInputIdentity(t *testing.T) {
	body := []byte(`{"input":[{"role":"developer","content":[{"type":"input_text","text":"` + factoryTestIdentity + ` Existing instructions"}]}]}`)
	out, err := ApplyFactoryCloaking(body, sdktranslator.FormatOpenAIResponse, "unknown", config.FactoryCloakConfig{})
	if err != nil || string(out) != string(body) {
		t.Fatalf("duplicated existing input identity: %s %v", out, err)
	}
}

func TestFactoryNeverAndValidation(t *testing.T) {
	body := []byte(" { \"system\" : \"untouched\" } ")
	out, err := ApplyFactoryCloaking(body, sdktranslator.FormatClaude, "", config.FactoryCloakConfig{Mode: "never", StrictMode: true})
	if err != nil || string(out) != string(body) {
		t.Fatal("never changed bytes")
	}
	if _, err = ApplyFactoryCloaking(body, sdktranslator.FormatClaude, "", config.FactoryCloakConfig{Mode: "typo"}); err == nil {
		t.Fatal("invalid mode accepted")
	}
}

func TestFactoryModelAndTools(t *testing.T) {
	for _, tc := range []struct{ model, tools, want, absent string }{
		{"claude-opus-5", `[{"name":"TodoWrite","input_schema":{"type":"object"}}]`, "# Planning with TodoWrite", "# TodoWrite must share"},
		{"claude-opus-5", `[]`, "In user-facing prose, avoid mannered phrasing", "# Planning with TodoWrite"},
		{"gemini-3.5-flash", `[{"function":{"name":"Execute","parameters":{"properties":{"riskLevel":{"type":"string"}}}}}]`, "MUST write summary and riskLevel", "You have dedicated tools"},
		{"gemini-3.5-flash", `[{"functionDeclarations":[{"name":"Grep"}]}]`, "You have dedicated tools", "MUST write summary and riskLevel"},
	} {
		out, err := ApplyFactoryCloaking([]byte(`{"tools":`+tc.tools+`}`), sdktranslator.FormatOpenAIResponse, tc.model, config.FactoryCloakConfig{StrictMode: true})
		if err != nil {
			t.Fatal(err)
		}
		prompt := gjson.GetBytes(out, "instructions").String()
		if !strings.Contains(prompt, tc.want) || strings.Contains(prompt, tc.absent) {
			t.Fatalf("model/tool additions wrong for %s: %s", tc.model, prompt)
		}
	}
}
