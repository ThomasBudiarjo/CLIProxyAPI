package helps

import (
	"embed"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

//go:embed factory_prompts/*.txt factory_prompts/*.json
var factoryPrompts embed.FS

// ApplyFactoryCloaking applies the pinned Droid profile without altering conversation payloads.
func ApplyFactoryCloaking(body []byte, protocol sdktranslator.Format, model string, settings config.FactoryCloakConfig) ([]byte, error) {
	mode := strings.ToLower(strings.TrimSpace(settings.Mode))
	switch mode {
	case "never":
		return body, nil
	case "", "auto", "always":
	default:
		return nil, fmt.Errorf("factory cloak: unknown mode %q", settings.Mode)
	}
	profile := strings.ToLower(strings.TrimSpace(settings.PromptProfile))
	if profile == "" {
		profile = "interactive"
	}
	if profile != "interactive" && profile != "noninteractive" {
		return nil, fmt.Errorf("factory cloak: unknown prompt profile %q", profile)
	}
	if !gjson.ValidBytes(body) || !gjson.ParseBytes(body).IsObject() {
		return nil, fmt.Errorf("factory cloak: request must be a JSON object")
	}
	base, err := factoryPrompts.ReadFile("factory_prompts/" + profile + "-base.txt")
	if err != nil {
		return nil, fmt.Errorf("factory cloak: load profile: %w", err)
	}
	identity := strings.SplitN(string(base), "\n\n", 2)[0]
	path := ""
	switch protocol {
	case sdktranslator.FormatClaude:
		path = "system"
	case sdktranslator.FormatOpenAIResponse:
		path = "instructions"
	case sdktranslator.FormatOpenAI:
		path = "messages"
	case sdktranslator.FormatGemini:
		path = "systemInstruction.parts"
	default:
		return nil, fmt.Errorf("factory cloak: unsupported protocol %q", protocol)
	}
	current := gjson.GetBytes(body, path)
	first := current
	if protocol == sdktranslator.FormatOpenAI || (protocol == sdktranslator.FormatOpenAIResponse && current.String() == "") {
		items := current
		if protocol == sdktranslator.FormatOpenAIResponse {
			items = gjson.GetBytes(body, "input")
		}
		first = gjson.Result{}
		for _, m := range items.Array() {
			if factoryInstruction(m) {
				first = m.Get("content")
				break
			}
		}
	}
	if first.IsArray() {
		first = first.Get("0.text")
	}
	if !settings.StrictMode && strings.HasPrefix(first.String(), identity) {
		return body, nil
	}
	prompt := identity
	if settings.StrictMode {
		prompt, err = factoryFullPrompt(string(base), model, gjson.GetBytes(body, "tools"))
		if err != nil {
			return nil, err
		}
	}
	switch protocol {
	case sdktranslator.FormatClaude:
		if settings.StrictMode || !current.IsArray() {
			if !settings.StrictMode && current.String() != "" {
				prompt += "\n\n" + current.String()
			}
			return sjson.SetBytes(body, path, prompt)
		}
		return factoryPrependBlock(body, path, current, map[string]string{"type": "text", "text": prompt})
	case sdktranslator.FormatGemini:
		if settings.StrictMode {
			return sjson.SetBytes(body, "systemInstruction", map[string]any{"parts": []any{map[string]string{"text": prompt}}})
		}
		return factoryPrependBlock(body, path, current, map[string]string{"text": prompt})
	case sdktranslator.FormatOpenAI:
		messages := []json.RawMessage{}
		message, _ := json.Marshal(map[string]string{"role": "system", "content": prompt})
		messages = append(messages, message)
		for _, m := range current.Array() {
			if !settings.StrictMode || !factoryInstruction(m) {
				messages = append(messages, json.RawMessage(m.Raw))
			}
		}
		return sjson.SetBytes(body, path, messages)
	case sdktranslator.FormatOpenAIResponse:
		if !settings.StrictMode && current.String() != "" {
			prompt += "\n\n" + current.String()
		}
		if settings.StrictMode && gjson.GetBytes(body, "input").IsArray() {
			input := []json.RawMessage{}
			for _, m := range gjson.GetBytes(body, "input").Array() {
				if !factoryInstruction(m) {
					input = append(input, json.RawMessage(m.Raw))
				}
			}
			body, err = sjson.SetBytes(body, "input", input)
			if err != nil {
				return nil, err
			}
		}
		return sjson.SetBytes(body, path, prompt)
	}
	return body, nil
}

func factoryInstruction(item gjson.Result) bool {
	role := item.Get("role").String()
	return role == "system" || role == "developer"
}

func factoryPrependBlock(body []byte, path string, current gjson.Result, block map[string]string) ([]byte, error) {
	encoded, err := json.Marshal(block)
	if err != nil {
		return nil, err
	}
	blocks := []json.RawMessage{encoded}
	for _, part := range current.Array() {
		blocks = append(blocks, json.RawMessage(part.Raw))
	}
	return sjson.SetBytes(body, path, blocks)
}

func factoryFullPrompt(base, model string, tools gjson.Result) (string, error) {
	additionsData, err := factoryPrompts.ReadFile("factory_prompts/conditional-additions.json")
	if err != nil {
		return "", err
	}
	remindersData, err := factoryPrompts.ReadFile("factory_prompts/model-reminder-map.json")
	if err != nil {
		return "", err
	}
	var additions map[string]string
	var models map[string][]string
	if err = json.Unmarshal(additionsData, &additions); err != nil {
		return "", err
	}
	if err = json.Unmarshal(remindersData, &models); err != nil {
		return "", err
	}
	flags := map[string]bool{}
	for _, flag := range models[model] {
		flags[flag] = true
	}
	names := map[string]bool{}
	risk := false
	var inspect func(gjson.Result)
	inspect = func(tool gjson.Result) {
		if function := tool.Get("function"); function.IsObject() {
			inspect(function)
			return
		}
		if declarations := tool.Get("functionDeclarations"); declarations.IsArray() {
			for _, d := range declarations.Array() {
				inspect(d)
			}
			return
		}
		name := tool.Get("name").String()
		names[name] = true
		if name == "Execute" || name == "execute-cli" {
			for _, schema := range []string{"input_schema", "parameters", "parametersJsonSchema"} {
				if tool.Get(schema + ".properties.riskLevel").Exists() {
					risk = true
				}
			}
		}
	}
	for _, tool := range tools.Array() {
		inspect(tool)
	}
	appendAddition := func(key string) { base += "\n\n" + additions[key] }
	if flags["plain-prose"] {
		appendAddition("plainProse")
	}
	if names["TodoWrite"] {
		appendAddition("todoWrite")
	}
	for _, pair := range [][2]string{{"no-comments", "noComments"}, {"parallel-tool-calls", "parallelToolCalls"}, {"astra-autonomy", "astraAutonomy"}} {
		if flags[pair[0]] {
			appendAddition(pair[1])
		}
	}
	if flags["gemini-risk-level"] {
		if risk {
			appendAddition("geminiRiskLevel")
		} else {
			appendAddition("geminiNoRiskLevel")
		}
	}
	if flags["gemini-tool-preference"] {
		if names["Grep"] || names["Glob"] || names["LS"] {
			appendAddition("geminiToolPreference")
		} else {
			appendAddition("geminiWithoutSearchTools")
		}
	}
	if flags["gemini-spec-mode"] {
		appendAddition("geminiSpecMode")
	}
	return base, nil
}
