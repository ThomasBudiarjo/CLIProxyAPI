package helps

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// FactoryPolicyError must not cool down unrelated credentials or retry a rejected request.
type FactoryPolicyError struct{}

func (FactoryPolicyError) Error() string         { return "factory: content_policy_rejected" }
func (FactoryPolicyError) StatusCode() int       { return 403 }
func (FactoryPolicyError) IsRequestScoped() bool { return true }

// ReadFactorySSE assembles complete SSE events, including multiline data fields.
func ReadFactorySSE(r io.Reader, consume func(string, []byte) error) error {
	const maxFrame = 50 * 1024 * 1024
	scanner := bufio.NewScanner(r)
	scanner.Buffer(nil, maxFrame)
	var event string
	var data []byte
	flush := func() error {
		if len(data) == 0 {
			if event == "error" {
				return fmt.Errorf("factory: empty upstream error event")
			}
			event = ""
			return nil
		}
		err := consume(event, bytes.TrimSuffix(data, []byte("\n")))
		event, data = "", nil
		return err
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := flush(); err != nil {
				return err
			}
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			event = value
		case "data":
			if len(data)+len(value)+1 > maxFrame {
				return fmt.Errorf("factory: upstream SSE event exceeds size limit")
			}
			data = append(data, value...)
			data = append(data, '\n')
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return flush()
}

// FactoryStreamTerminal validates events and detects protocol-specific completion.
// A transport EOF alone is not successful completion.
func FactoryStreamTerminal(protocol translator.Format, event string, data []byte) (bool, error) {
	if bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
		if event == "error" {
			return false, fmt.Errorf("factory: upstream error event")
		}
		return protocol == translator.FormatOpenAI, nil
	}
	if !json.Valid(data) {
		return false, fmt.Errorf("factory: malformed upstream SSE JSON")
	}
	root := gjson.ParseBytes(data)
	kind := root.Get("type").String()
	if event == "error" || kind == "error" || kind == "response.failed" || (root.Get("error").Exists() && root.Get("error").Type != gjson.Null) {
		return false, fmt.Errorf("factory: upstream stream returned an error")
	}
	switch protocol {
	case translator.FormatClaude:
		return kind == "message_stop", nil
	case translator.FormatOpenAIResponse:
		return kind == "response.completed" || kind == "response.incomplete", nil
	case translator.FormatGemini:
		for _, candidate := range root.Get("candidates").Array() {
			if candidate.Get("finishReason").String() != "" {
				return true, nil
			}
		}
	}
	return false, nil
}

// FactoryResponseCollector reconstructs native JSON only for non-stream callers.
// Streaming callers never retain the entire generated response in memory.
type FactoryResponseCollector struct {
	Protocol translator.Format
	body     []byte
	toolJSON map[int]string
}

func (c *FactoryResponseCollector) set(path string, value any) {
	c.body, _ = sjson.SetBytes(c.body, path, value)
}

func (c *FactoryResponseCollector) appendText(path, text string) {
	c.set(path, gjson.GetBytes(c.body, path).String()+text)
}

func (c *FactoryResponseCollector) merge(path string, value gjson.Result) {
	value.ForEach(func(key, v gjson.Result) bool {
		c.set(path+"."+key.String(), v.Value())
		return true
	})
}

func factoryIndex(value gjson.Result) (int, error) {
	i := value.Int()
	if i < 0 || i > 4096 {
		return 0, fmt.Errorf("factory: invalid upstream content index")
	}
	return int(i), nil
}

// Add preserves text, thinking signatures, tool arguments, usage, and stop reasons.
func (c *FactoryResponseCollector) Add(data []byte) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
		return nil
	}
	r := gjson.ParseBytes(data)
	if c.body == nil {
		c.body = []byte(`{}`)
	}
	switch c.Protocol {
	case translator.FormatOpenAIResponse:
		if r.Get("type").String() == "response.completed" || r.Get("type").String() == "response.incomplete" {
			c.body = []byte(r.Get("response").Raw)
		}
	case translator.FormatClaude:
		i, err := factoryIndex(r.Get("index"))
		if err != nil {
			return err
		}
		path := "content." + strconv.Itoa(i)
		switch r.Get("type").String() {
		case "message_start":
			c.body = []byte(r.Get("message").Raw)
		case "content_block_start":
			c.set(path, r.Get("content_block").Value())
		case "content_block_delta":
			delta := r.Get("delta")
			switch delta.Get("type").String() {
			case "text_delta":
				c.appendText(path+".text", delta.Get("text").String())
			case "thinking_delta":
				c.appendText(path+".thinking", delta.Get("thinking").String())
			case "signature_delta":
				c.appendText(path+".signature", delta.Get("signature").String())
			case "input_json_delta":
				if c.toolJSON == nil {
					c.toolJSON = make(map[int]string)
				}
				c.toolJSON[i] += delta.Get("partial_json").String()
			case "citations_delta":
				c.set(path+".citations.-1", delta.Get("citation").Value())
			}
		case "content_block_stop":
			if raw := c.toolJSON[i]; raw != "" {
				if !json.Valid([]byte(raw)) {
					return fmt.Errorf("factory: incomplete upstream tool arguments")
				}
				c.set(path+".input", gjson.Parse(raw).Value())
				delete(c.toolJSON, i)
			}
		case "message_delta":
			r.Get("delta").ForEach(func(k, v gjson.Result) bool { c.set(k.String(), v.Value()); return true })
			c.merge("usage", r.Get("usage"))
		}
	case translator.FormatGemini:
		for _, key := range []string{"modelVersion", "responseId", "createTime", "usageMetadata", "promptFeedback"} {
			if v := r.Get(key); v.Exists() {
				c.set(key, v.Value())
			}
		}
		for _, candidate := range r.Get("candidates").Array() {
			i, err := factoryIndex(candidate.Get("index"))
			if err != nil {
				return err
			}
			path := "candidates." + strconv.Itoa(i)
			candidate.ForEach(func(k, v gjson.Result) bool {
				if k.String() != "content" {
					c.set(path+"."+k.String(), v.Value())
				}
				return true
			})
			if role := candidate.Get("content.role"); role.Exists() {
				c.set(path+".content.role", role.Value())
			}
			for _, part := range candidate.Get("content.parts").Array() {
				c.set(path+".content.parts.-1", part.Value())
			}
		}
	case translator.FormatOpenAI:
		c.set("object", "chat.completion")
		for _, key := range []string{"id", "model", "created", "usage"} {
			if v := r.Get(key); v.Exists() {
				c.set(key, v.Value())
			}
		}
		for _, choice := range r.Get("choices").Array() {
			i, err := factoryIndex(choice.Get("index"))
			if err != nil {
				return err
			}
			path := "choices." + strconv.Itoa(i)
			c.set(path+".index", i)
			if v := choice.Get("finish_reason"); v.Type != gjson.Null {
				c.set(path+".finish_reason", v.Value())
			}
			delta := choice.Get("delta")
			if role := delta.Get("role"); role.Exists() {
				c.set(path+".message.role", role.Value())
			}
			for _, field := range []string{"content", "reasoning_content", "refusal"} {
				if v := delta.Get(field); v.Type == gjson.String {
					c.appendText(path+".message."+field, v.String())
				}
			}
			for _, tool := range delta.Get("tool_calls").Array() {
				j, err := factoryIndex(tool.Get("index"))
				if err != nil {
					return err
				}
				tp := path + ".message.tool_calls." + strconv.Itoa(j)
				for _, field := range []string{"id", "type"} {
					if v := tool.Get(field); v.Exists() {
						c.set(tp+"."+field, v.Value())
					}
				}
				for _, field := range []string{"name", "arguments"} {
					if v := tool.Get("function." + field); v.Exists() {
						c.appendText(tp+".function."+field, v.String())
					}
				}
			}
		}
	}
	return nil
}

func (c *FactoryResponseCollector) Response() ([]byte, error) {
	if !json.Valid(c.body) || len(c.body) <= 2 || len(c.toolJSON) != 0 {
		return nil, fmt.Errorf("factory: incomplete upstream response")
	}
	return c.body, nil
}
