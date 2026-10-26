package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	authsdk "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	execsdk "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// FactoryExecutor routes configured models to Factory's native inference protocols.
type FactoryExecutor struct{ cfg *config.Config }

func NewFactoryExecutor(cfg *config.Config) *FactoryExecutor { return &FactoryExecutor{cfg: cfg} }
func (e *FactoryExecutor) Identifier() string                { return "factory" }
func (e *FactoryExecutor) Refresh(_ context.Context, auth *authsdk.Auth) (*authsdk.Auth, error) {
	return auth, nil
}

func (e *FactoryExecutor) PrepareRequest(req *http.Request, auth *authsdk.Auth) error {
	if req == nil {
		return fmt.Errorf("factory: nil request")
	}
	if auth == nil || auth.Attributes["api_key"] == "" {
		return statusErr{code: 401, msg: "factory: missing API key"}
	}
	req.Header.Set("Authorization", "Bearer "+auth.Attributes["api_key"])
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Factory-Client", "cli")
	req.Header.Set("X-Client-Version", "0.249.0")
	req.Header.Set("X-Session-Id", uuid.NewString())
	util.ApplyCustomHeadersFromAttrs(req, auth.Attributes)
	return nil
}

func (e *FactoryExecutor) HttpRequest(ctx context.Context, auth *authsdk.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("factory: nil request")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	req = req.WithContext(ctx)
	if err := e.PrepareRequest(req, auth); err != nil {
		return nil, err
	}
	return helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0).Do(req)
}

func (e *FactoryExecutor) prepare(ctx context.Context, auth *authsdk.Auth, req execsdk.Request, opts execsdk.Options, stream bool) ([]byte, translator.Format, string, string, error) {
	if opts.Alt != "" {
		return nil, "", "", "", statusErr{code: 400, msg: "factory: alternate operations are not supported"}
	}
	if auth == nil {
		return nil, "", "", "", statusErr{code: 401, msg: "factory: missing credential"}
	}
	var models []config.FactoryModel
	if err := json.Unmarshal([]byte(auth.Attributes["factory_models"]), &models); err != nil {
		return nil, "", "", "", fmt.Errorf("factory: invalid model configuration")
	}
	model := thinking.ParseSuffix(req.Model).ModelName
	var selected *config.FactoryModel
	for i := range models {
		if models[i].Name == model {
			selected = &models[i]
			break
		}
	}
	if selected == nil {
		return nil, "", "", "", statusErr{code: 400, msg: "factory: model is not configured"}
	}
	protocol := selected.Protocol
	if protocol == "" {
		switch {
		case strings.HasPrefix(model, "claude-"):
			protocol = "claude"
		case strings.HasPrefix(model, "gpt-"), strings.HasPrefix(model, "o1"), strings.HasPrefix(model, "o3"), strings.HasPrefix(model, "o4"):
			protocol = "responses"
		case strings.HasPrefix(model, "gemini-"):
			protocol = "gemini"
		default:
			return nil, "", "", "", statusErr{code: 400, msg: "factory: explicit protocol required for this model"}
		}
	}
	var format translator.Format
	var path, provider string
	switch protocol {
	case "claude":
		format, path, provider = translator.FormatClaude, "/api/llm/a/v1/messages", "anthropic"
	case "responses":
		format, path, provider = translator.FormatOpenAIResponse, "/api/llm/o/v1/responses", "openai"
	case "openai":
		format, path, provider = translator.FormatOpenAI, "/api/llm/o/v1/chat/completions", "openai"
	case "gemini":
		format, path, provider = translator.FormatGemini, "/api/llm/g/v1/generate", "google"
	default:
		return nil, "", "", "", statusErr{code: 400, msg: "factory: unsupported protocol"}
	}
	if selected.APIProvider != "" {
		provider = selected.APIProvider
	}
	translationFormat := format
	if format == translator.FormatOpenAIResponse && opts.SourceFormat != format {
		translationFormat = translator.FormatCodex
	}
	if opts.SourceFormat != format && !translator.HasRequestTransformer(opts.SourceFormat, translationFormat) {
		return nil, "", "", "", statusErr{code: 400, msg: "factory: unsupported request translation"}
	}
	responseFormat := execsdk.ResponseFormatOrSource(opts)
	responseSource := format
	if format == translator.FormatOpenAIResponse {
		responseSource = translator.FormatCodex
	}
	if responseFormat != format {
		supported := translator.HasNonStreamResponseTransformer(responseFormat, responseSource)
		if stream {
			supported = translator.HasStreamResponseTransformer(responseFormat, responseSource)
		}
		if !supported {
			return nil, "", "", "", statusErr{code: 400, msg: "factory: unsupported response translation"}
		}
	}
	original := req.Payload
	if len(opts.OriginalRequest) > 0 {
		original = opts.OriginalRequest
	}
	original, body, err := helps.TranslateRequestPairReturningError(ctx, opts.Headers, e.cfg, opts.SourceFormat, translationFormat, model, original, req.Payload, stream, helps.APIKeyModelIsCompat(req))
	if err != nil {
		return nil, "", "", "", err
	}
	// Reuse the Responses schema translator, without Codex's token-limit restrictions.
	if translationFormat == translator.FormatCodex {
		for _, field := range []string{"temperature", "top_p", "max_tokens", "max_completion_tokens", "max_output_tokens"} {
			if value := gjson.GetBytes(req.Payload, field); value.Exists() {
				target := field
				if strings.HasPrefix(field, "max_") {
					target = "max_output_tokens"
				}
				body, _ = sjson.SetBytes(body, target, value.Value())
			}
		}
	}
	body, err = helps.ApplyRequestThinking(body, req, opts, opts.SourceFormat.String(), format.String(), e.Identifier())
	if err != nil {
		return nil, "", "", "", err
	}
	body = helps.SetStringIfDifferent(body, "model", model)
	if format == translator.FormatGemini {
		body, err = sjson.DeleteBytes(body, "stream")
	} else {
		body, err = sjson.SetBytes(body, "stream", stream)
	}
	if err != nil {
		return nil, "", "", "", err
	}
	if format == translator.FormatClaude && !gjson.GetBytes(body, "max_tokens").Exists() {
		body, _ = sjson.SetBytes(body, "max_tokens", 4096)
	}
	var cloak config.FactoryCloakConfig
	if raw := auth.Attributes["factory_cloak"]; raw != "" {
		if err = json.Unmarshal([]byte(raw), &cloak); err != nil {
			return nil, "", "", "", fmt.Errorf("factory: invalid cloak configuration")
		}
	}
	body, err = helps.ApplyFactoryCloaking(body, format, model, cloak)
	if err != nil {
		return nil, "", "", "", err
	}
	body = helps.NewPayloadFinalizer(e.cfg, e.Identifier(), model, format.String(), "", original, req, opts)(body)
	return body, format, path, provider, nil
}

func (e *FactoryExecutor) send(ctx context.Context, auth *authsdk.Auth, body []byte, path, provider string, reporter *helps.UsageReporter) (*http.Response, error) {
	base := strings.TrimRight(auth.Attributes["base_url"], "/")
	if base == "" {
		base = "https://api.factory.ai"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-api-provider", provider)
	if path == "/api/llm/a/v1/messages" {
		req.Header.Set("anthropic-version", "2023-06-01")
	}
	if err = e.PrepareRequest(req, auth); err != nil {
		return nil, err
	}
	client := reporter.TrackHTTPClient(helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0))
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer e.closeBody(resp.Body)
		data, errRead := io.ReadAll(resp.Body)
		if errRead != nil {
			return nil, errRead
		}
		if resp.StatusCode == http.StatusForbidden && (gjson.GetBytes(data, "error.type").String() == "content_policy_rejected" || gjson.GetBytes(data, "error.code").String() == "content_policy_rejected") {
			return nil, helps.FactoryPolicyError{}
		}
		return nil, newOpenAICompatStatusError(resp.StatusCode, resp.Header, data)
	}
	return resp, nil
}

func (e *FactoryExecutor) closeBody(body io.ReadCloser) {
	if err := body.Close(); err != nil {
		log.WithError(err).Warn("factory: closing upstream response")
	}
}

func (e *FactoryExecutor) Execute(ctx context.Context, auth *authsdk.Auth, req execsdk.Request, opts execsdk.Options) (result execsdk.Response, err error) {
	body, format, path, provider, err := e.prepare(ctx, auth, req, opts, false)
	if err != nil {
		return result, err
	}
	reporter := helps.NewExecutorUsageReporter(ctx, e, req.Model, auth)
	defer reporter.TrackFailure(ctx, &err)
	resp, err := e.send(ctx, auth, body, path, provider, reporter)
	if err != nil {
		return result, err
	}
	defer e.closeBody(resp.Body)
	var data []byte
	if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		collector := helps.FactoryResponseCollector{Protocol: format}
		terminal := false
		err = helps.ReadFactorySSE(resp.Body, func(event string, frame []byte) error {
			done, errFrame := helps.FactoryStreamTerminal(format, event, frame)
			if errFrame != nil {
				return errFrame
			}
			terminal = terminal || done
			return collector.Add(frame)
		})
		if err != nil {
			return result, err
		}
		if !terminal {
			return result, fmt.Errorf("factory: upstream stream ended before completion")
		}
		data, err = collector.Response()
	} else {
		data, err = io.ReadAll(resp.Body)
	}
	if err != nil {
		return result, err
	}
	if !gjson.ValidBytes(data) {
		return result, fmt.Errorf("factory: malformed upstream response")
	}
	if gjson.GetBytes(data, "error").Exists() && gjson.GetBytes(data, "error").Type != gjson.Null {
		return result, fmt.Errorf("factory: upstream returned an error response")
	}
	switch format {
	case translator.FormatClaude:
		reporter.Publish(ctx, helps.ParseClaudeUsage(data))
	case translator.FormatGemini:
		reporter.Publish(ctx, helps.ParseGeminiUsage(data))
	default:
		reporter.Publish(ctx, helps.ParseOpenAIUsage(data))
	}
	var param any
	responseFormat := execsdk.ResponseFormatOrSource(opts)
	if format == translator.FormatOpenAIResponse && responseFormat != format {
		format = translator.FormatCodex
		kind := "response.completed"
		if gjson.GetBytes(data, "status").String() == "incomplete" {
			kind = "response.incomplete"
		}
		data, _ = json.Marshal(map[string]any{"type": kind, "response": json.RawMessage(data)})
	}
	out := translator.TranslateNonStream(ctx, format, responseFormat, req.Model, helps.ApplyPatchOriginalRequest(req, opts), body, data, &param)
	if err := helps.ApplyPatchTranslationError(param); err != nil {
		return result, err
	}
	if len(out) == 0 {
		return result, fmt.Errorf("factory: empty translated response")
	}
	return execsdk.Response{Payload: out, Headers: resp.Header.Clone()}, nil
}

func (e *FactoryExecutor) ExecuteStream(ctx context.Context, auth *authsdk.Auth, req execsdk.Request, opts execsdk.Options) (_ *execsdk.StreamResult, err error) {
	body, format, path, provider, err := e.prepare(ctx, auth, req, opts, true)
	if err != nil {
		return nil, err
	}
	reporter := helps.NewExecutorUsageReporter(ctx, e, req.Model, auth)
	defer reporter.TrackFailure(ctx, &err)
	resp, err := e.send(ctx, auth, body, path, provider, reporter)
	if err != nil {
		return nil, err
	}
	chunks := make(chan execsdk.StreamChunk)
	go func() {
		defer close(chunks)
		defer e.closeBody(resp.Body)
		var param any
		var usage helps.StreamUsageBuffer
		terminal := false
		emit := func(chunk execsdk.StreamChunk) error {
			select {
			case chunks <- chunk:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		errStream := helps.ReadFactorySSE(resp.Body, func(event string, data []byte) error {
			done, errFrame := helps.FactoryStreamTerminal(format, event, data)
			if errFrame != nil {
				return errFrame
			}
			terminal = terminal || done
			line := append([]byte("data: "), data...)
			switch format {
			case translator.FormatClaude:
				usage.ObserveClaudeStream(line)
			case translator.FormatGemini:
				usage.Observe(helps.ParseGeminiStreamUsage(line))
			case translator.FormatOpenAIResponse:
				usage.Observe(helps.ParseCodexUsage(data))
			default:
				usage.ObserveOpenAIStream(line)
			}
			var frames [][]byte
			if format == execsdk.ResponseFormatOrSource(opts) {
				prefix := ""
				if event != "" {
					prefix = "event: " + event + "\n"
				}
				frames = [][]byte{[]byte(prefix + string(line) + "\n\n")}
			} else {
				translationFormat := format
				if format == translator.FormatOpenAIResponse {
					translationFormat = translator.FormatCodex
				}
				frames = translator.TranslateStream(ctx, translationFormat, execsdk.ResponseFormatOrSource(opts), req.Model, helps.ApplyPatchOriginalRequest(req, opts), body, line, &param)
			}
			if errTranslation := helps.ApplyPatchTranslationError(param); errTranslation != nil {
				return errTranslation
			}
			for _, frame := range frames {
				if errEmit := emit(execsdk.StreamChunk{Payload: frame}); errEmit != nil {
					return errEmit
				}
			}
			return nil
		})
		if errStream == nil && !terminal {
			errStream = fmt.Errorf("factory: upstream stream ended before completion")
		}
		if errStream != nil {
			usage.PublishFailure(ctx, reporter, errStream)
			_ = emit(execsdk.StreamChunk{Err: errStream})
			return
		}
		usage.Publish(ctx, reporter)
	}()
	return &execsdk.StreamResult{Chunks: chunks, Headers: resp.Header.Clone()}, nil
}

// CountTokens uses the local tokenizer; Factory does not expose a counting endpoint.
func (e *FactoryExecutor) CountTokens(ctx context.Context, auth *authsdk.Auth, req execsdk.Request, opts execsdk.Options) (execsdk.Response, error) {
	body, format, _, _, err := e.prepare(ctx, auth, req, opts, false)
	if err != nil {
		return execsdk.Response{}, err
	}
	// Count the final native payload, including the full cloak, via Claude's local estimator.
	if format != translator.FormatClaude {
		if !translator.HasRequestTransformer(format, translator.FormatClaude) {
			return execsdk.Response{}, statusErr{code: 400, msg: "factory: local token counting unavailable for this protocol"}
		}
		body = translator.TranslateRequest(format, translator.FormatClaude, req.Model, body, false)
	}
	count, err := helps.CountClaudeInputTokens(body)
	if err != nil {
		return execsdk.Response{}, err
	}
	out := translator.TranslateTokenCount(ctx, translator.FormatOpenAI, execsdk.ResponseFormatOrSource(opts), count, helps.BuildOpenAIUsageJSON(count))
	return execsdk.Response{Payload: out}, nil
}
