package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

const factoryClaudeSSE = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_test\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-haiku-4-5-20251001\",\"content\":[],\"usage\":{\"input_tokens\":17,\"output_tokens\":0}}}\n\n" +
	"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
	"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"factory answer\"}}\n\n" +
	"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
	"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":3}}\n\n" +
	"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

const factoryResponsesSSE = "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_test\",\"model\":\"gpt-6-luna\",\"output\":[]}}\n\n" +
	"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":\"factory answer\"}\n\n" +
	"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_test\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"gpt-6-luna\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"factory answer\"}]}],\"usage\":{\"input_tokens\":17,\"output_tokens\":3,\"total_tokens\":20}}}\n\n"

const factoryGeminiSSE = "data: {\"modelVersion\":\"gemini-3-flash-preview\",\"candidates\":[{\"index\":0,\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"factory \"}]}}]}\n\n" +
	"data: {\"modelVersion\":\"gemini-3-flash-preview\",\"candidates\":[{\"index\":0,\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"answer\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":17,\"candidatesTokenCount\":3,\"totalTokenCount\":20}}\n\n"

const factoryChatSSE = "data: {\"id\":\"chat_test\",\"object\":\"chat.completion.chunk\",\"model\":\"custom-model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"factory answer\"},\"finish_reason\":null}]}\n\n" +
	"data: {\"id\":\"chat_test\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":17,\"completion_tokens\":3,\"total_tokens\":20}}\n\ndata: [DONE]\n\n"

func factoryTestAuth(url, models, cloak string) *coreauth.Auth {
	return &coreauth.Auth{ID: "factory-test", Provider: "factory", Attributes: map[string]string{
		"api_key": "fk-test", "base_url": url, "factory_models": models, "factory_cloak": cloak,
	}}
}

func TestFactoryModelRoutingAndResponses(t *testing.T) {
	for _, tc := range []struct{ model, protocol, provider, path, systemPath, sse string }{
		{"claude-haiku-4-5-20251001", "", "anthropic", "/api/llm/a/v1/messages", "system", factoryClaudeSSE},
		{"gpt-6-luna", "", "openai", "/api/llm/o/v1/responses", "instructions", factoryResponsesSSE},
		{"gemini-3-flash-preview", "", "google", "/api/llm/g/v1/generate", "systemInstruction", factoryGeminiSSE},
		{"custom-model", "openai", "fireworks", "/api/llm/o/v1/chat/completions", "messages.0", factoryChatSSE},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", tc.model, stream), func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, _ := io.ReadAll(r.Body)
					if r.URL.Path != tc.path || r.Header.Get("x-api-provider") != tc.provider {
						t.Errorf("wrong model routing: %s, provider %s", r.URL.Path, r.Header.Get("x-api-provider"))
					}
					if r.Header.Get("Authorization") != "Bearer fk-test" || r.Header.Get("x-session-id") == "" {
						t.Error("missing Factory authentication/session headers")
					}
					if gjson.GetBytes(body, "model").String() != tc.model {
						t.Errorf("wrong upstream model: %s", body)
					}
					prompt := gjson.GetBytes(body, tc.systemPath).Raw
					if !strings.Contains(prompt, "You are Droid,") || !strings.Contains(string(body), "Keep my instructions") {
						t.Errorf("cloaking lost identity or caller instructions: %s", body)
					}
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, tc.sse)
				}))
				defer server.Close()
				models := fmt.Sprintf(`[{"name":%q,"protocol":%q,"api-provider":%q}]`, tc.model, tc.protocol, tc.provider)
				auth := factoryTestAuth(server.URL, models, `{}`)
				req := coreexecutor.Request{Model: tc.model, Payload: []byte(`{"messages":[{"role":"system","content":"Keep my instructions"},{"role":"user","content":"hello"}],"max_tokens":128}`)}
				opts := coreexecutor.Options{SourceFormat: translator.FormatOpenAI, Stream: stream}
				e := NewFactoryExecutor(&config.Config{})
				if stream {
					result, err := e.ExecuteStream(context.Background(), auth, req, opts)
					if err != nil {
						t.Fatal(err)
					}
					var text strings.Builder
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
						for _, line := range strings.Split(string(chunk.Payload), "\n") {
							data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
							text.WriteString(gjson.Get(data, "choices.0.delta.content").String())
						}
					}
					if text.String() != "factory answer" {
						t.Fatalf("stream text = %q", text.String())
					}
				} else {
					result, err := e.Execute(context.Background(), auth, req, opts)
					if err != nil {
						t.Fatal(err)
					}
					if got := gjson.GetBytes(result.Payload, "choices.0.message.content").String(); got != "factory answer" {
						t.Fatalf("nonstream text = %q, body %s", got, result.Payload)
					}
				}
			})
		}
	}
}

func TestFactoryPayloadRulesAreFinal(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				if gjson.GetBytes(body, "system").String() != "operator wins" || gjson.GetBytes(body, "max_tokens").Exists() {
					t.Errorf("built-ins overwrote final rules: %s", body)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, factoryClaudeSSE)
			}))
			defer server.Close()
			cfg := &config.Config{Payload: config.PayloadConfig{
				Override: []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "claude-*", Protocol: "claude"}}, Params: map[string]any{"system": "operator wins"}}},
				Filter:   []config.PayloadFilterRule{{Models: []config.PayloadModelRule{{Name: "claude-*", Protocol: "claude"}}, Params: []string{"max_tokens"}}},
			}}
			auth := factoryTestAuth(server.URL, `[{"name":"claude-haiku-4-5-20251001"}]`, `{"strict-mode":true}`)
			req := coreexecutor.Request{Model: "claude-haiku-4-5-20251001", Payload: []byte(`{"system":"caller","messages":[{"role":"user","content":"hi"}],"max_tokens":64}`)}
			opts := coreexecutor.Options{SourceFormat: translator.FormatClaude}
			e := NewFactoryExecutor(cfg)
			if stream {
				res, err := e.ExecuteStream(context.Background(), auth, req, opts)
				if err != nil {
					t.Fatal(err)
				}
				for chunk := range res.Chunks {
					if chunk.Err != nil {
						t.Fatal(chunk.Err)
					}
				}
			} else if _, err := e.Execute(context.Background(), auth, req, opts); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestFactoryRejectsTruncatedAndErrorStreams(t *testing.T) {
	for _, body := range []string{
		strings.Split(factoryClaudeSSE, "event: message_stop")[0],
		"event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"try later\"}}\n\n",
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, body)
		}))
		auth := factoryTestAuth(server.URL, `[{"name":"claude-haiku-4-5-20251001"}]`, `{}`)
		req := coreexecutor.Request{Model: "claude-haiku-4-5-20251001", Payload: []byte(`{"messages":[{"role":"user","content":"hi"}]}`)}
		opts := coreexecutor.Options{SourceFormat: translator.FormatClaude}
		e := NewFactoryExecutor(&config.Config{})
		if _, err := e.Execute(context.Background(), auth, req, opts); err == nil {
			t.Error("nonstream accepted incomplete/error stream")
		}
		result, err := e.ExecuteStream(context.Background(), auth, req, opts)
		if err != nil {
			t.Fatal(err)
		}
		failed := false
		for chunk := range result.Chunks {
			failed = failed || chunk.Err != nil
		}
		if !failed {
			t.Error("stream accepted incomplete/error stream")
		}
		server.Close()
	}
}

func TestFactoryNativeToolsAndTokenCounting(t *testing.T) {
	response := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"type\":\"message\",\"role\":\"assistant\",\"content\":[]}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call_a\",\"name\":\"lookup\",\"input\":{}}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"city\\\":\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"\\\"Jakarta\\\"}\"}}\n\n" +
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if gjson.GetBytes(body, "tools.0.name").String() != "lookup" || gjson.GetBytes(body, "messages.1.content.0.tool_use_id").String() != "previous" {
			t.Error("lost tool definition or conversation")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, response)
	}))
	defer server.Close()
	auth := factoryTestAuth(server.URL, `[{"name":"claude-haiku-4-5-20251001"}]`, `{"strict-mode":true}`)
	req := coreexecutor.Request{Model: "claude-haiku-4-5-20251001", Payload: []byte(`{"system":"caller","tools":[{"name":"lookup","input_schema":{"type":"object","properties":{"city":{"type":"string"}}}}],"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"previous","name":"lookup","input":{"city":"Tokyo"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"previous","content":"sunny"}]}]}`)}
	opts := coreexecutor.Options{SourceFormat: translator.FormatClaude}
	e := NewFactoryExecutor(&config.Config{})
	res, err := e.Execute(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatal(err)
	}
	if gjson.GetBytes(res.Payload, "content.0.input.city").String() != "Jakarta" || gjson.GetBytes(res.Payload, "stop_reason").String() != "tool_use" {
		t.Fatalf("lost tool response: %s", res.Payload)
	}
	stream, err := e.ExecuteStream(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatal(err)
	}
	var frames strings.Builder
	for c := range stream.Chunks {
		if c.Err != nil {
			t.Fatal(c.Err)
		}
		frames.Write(c.Payload)
	}
	if frames.String() != response {
		t.Fatal("native SSE framing/content changed")
	}
	full, err := e.CountTokens(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatal(err)
	}
	auth.Attributes["factory_cloak"] = `{"mode":"never"}`
	bare, err := e.CountTokens(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatal(err)
	}
	if gjson.GetBytes(full.Payload, "input_tokens").Int() <= gjson.GetBytes(bare.Payload, "input_tokens").Int() {
		t.Fatalf("full prompt not counted: %s vs %s", full.Payload, bare.Payload)
	}
}

func TestFactoryStreamCancellation(t *testing.T) {
	disconnected := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: ping\ndata: {\"type\":\"ping\"}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(disconnected)
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	auth := factoryTestAuth(server.URL, `[{"name":"claude-haiku-4-5-20251001"}]`, `{}`)
	res, err := NewFactoryExecutor(&config.Config{}).ExecuteStream(ctx, auth, coreexecutor.Request{Model: "claude-haiku-4-5-20251001", Payload: []byte(`{"messages":[]}`)}, coreexecutor.Options{SourceFormat: translator.FormatClaude})
	if err != nil {
		t.Fatal(err)
	}
	<-res.Chunks
	cancel()
	for range res.Chunks {
	}
	<-disconnected
}

func TestFactoryHTTPPolicyAndRateLimit(t *testing.T) {
	for _, code := range []int{403, 429} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(code)
			fmt.Fprint(w, `{"error":{"type":"content_policy_rejected"}}`)
		}))
		auth := factoryTestAuth(server.URL, `[{"name":"claude-haiku-4-5-20251001"}]`, `{}`)
		_, err := NewFactoryExecutor(&config.Config{}).Execute(context.Background(), auth, coreexecutor.Request{Model: "claude-haiku-4-5-20251001", Payload: []byte(`{"messages":[]}`)}, coreexecutor.Options{SourceFormat: translator.FormatClaude})
		var status coreexecutor.StatusError
		if !errors.As(err, &status) || status.StatusCode() != code {
			t.Fatalf("status lost: %v", err)
		}
		var scoped coreexecutor.RequestScopedError
		if got := errors.As(err, &scoped) && scoped.IsRequestScoped(); got != (code == 403) {
			t.Fatalf("wrong policy scope: %v", err)
		}
		if code == 429 {
			var limited statusErr
			if !errors.As(err, &limited) || limited.RetryAfter() == nil || limited.RetryAfter().Seconds() != 7 {
				t.Fatalf("lost Retry-After: %v", err)
			}
		}
		server.Close()
	}
}

func TestFactoryNativeResponsesParameters(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if gjson.GetBytes(body, "max_output_tokens").Int() != 173 || gjson.GetBytes(body, "temperature").Float() != 0.7 || gjson.GetBytes(body, "input.0.type").String() != "function_call_output" {
			t.Errorf("native Responses fields changed: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"resp_native","object":"response","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":2}}`)
	}))
	defer server.Close()
	auth := factoryTestAuth(server.URL, `[{"name":"gpt-6-luna"}]`, `{"strict-mode":true}`)
	res, err := NewFactoryExecutor(&config.Config{}).Execute(context.Background(), auth, coreexecutor.Request{Model: "gpt-6-luna", Payload: []byte(`{"instructions":"caller","max_output_tokens":173,"temperature":0.7,"input":[{"type":"function_call_output","call_id":"call_a","output":"42"}]}`)}, coreexecutor.Options{SourceFormat: translator.FormatOpenAIResponse})
	if err != nil {
		t.Fatal(err)
	}
	if gjson.GetBytes(res.Payload, "id").String() != "resp_native" {
		t.Fatalf("invalid native response: %s", res.Payload)
	}
}
