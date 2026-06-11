// ABOUTME: Scripted fake of the Anthropic Messages API over httptest, shared by unit and e2e tests.
// ABOUTME: Tests verify the contract AROUND the model (loop mechanics, fallbacks), never its judgment.

package fakes

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
)

// Scripted is one canned response, consumed in order.
type Scripted interface{ isScripted() }

// Text ends the turn with an assistant text block (stop_reason end_turn).
type Text struct{ S string }

// ToolUse asks for tool calls; multiple entries in one response model the
// API's parallel tool use. Thinking, when set, prepends a thinking block:
// the real API requires it replayed verbatim on continuation.
type ToolUse struct {
	Calls    []ToolCall
	Thinking string
}

type ToolCall struct {
	ID, Name string
	Input    json.RawMessage
}

// Stop ends the turn with an arbitrary stop_reason (pause_turn, refusal,
// max_tokens) and optional text content.
type Stop struct {
	Reason string
	S      string
}

// HTTPErr fails the request with a status code (e.g. 429, 529).
type HTTPErr struct {
	Status int
	Body   string
}

func (Text) isScripted()    {}
func (ToolUse) isScripted() {}
func (Stop) isScripted()    {}
func (HTTPErr) isScripted() {}

// Call makes single-tool scripting terse.
func Call(id, name string, input string) ToolUse {
	return ToolUse{Calls: []ToolCall{{ID: id, Name: name, Input: json.RawMessage(input)}}}
}

// CapturedRequest is the decoded body of one /v1/messages call. Raw carries
// the full untouched body for whole-request scans (e.g. cache_control).
type CapturedRequest struct {
	Model     string            `json:"model"`
	MaxTokens int               `json:"max_tokens"`
	System    json.RawMessage   `json:"system"`
	Messages  []json.RawMessage `json:"messages"`
	Tools     []json.RawMessage `json:"tools"`
	Raw       []byte            `json:"-"`
}

type Anthropic struct {
	mu       sync.Mutex
	script   []Scripted
	Requests []CapturedRequest
	srv      *httptest.Server
}

// NewAnthropic starts the fake; point the SDK at URL() with a dummy key.
func NewAnthropic(script ...Scripted) *Anthropic {
	f := &Anthropic{script: script}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	return f
}

func (f *Anthropic) URL() string { return f.srv.URL }
func (f *Anthropic) Close()      { f.srv.Close() }

func (f *Anthropic) handle(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var req CapturedRequest
	_ = json.Unmarshal(raw, &req)
	req.Raw = raw

	f.mu.Lock()
	f.Requests = append(f.Requests, req)
	if len(f.script) == 0 {
		f.mu.Unlock()
		// 400: terminal for the SDK (no retries), so a script bug fails the
		// test fast instead of inflating request counts through backoff.
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(w, `{"type":"error","error":{"type":"invalid_request_error","message":"fakeanthropic: script exhausted"}}`)
		return
	}
	next := f.script[0]
	f.script = f.script[1:]
	f.mu.Unlock()

	switch s := next.(type) {
	case HTTPErr:
		w.WriteHeader(s.Status)
		body := s.Body
		if body == "" {
			body = `{"type":"error","error":{"type":"api_error","message":"scripted error"}}`
		}
		_, _ = fmt.Fprint(w, body)
	case Text:
		f.writeMessage(w, req.Model, "end_turn", []any{textBlock(s.S)})
	case Stop:
		var content []any
		if s.S != "" {
			content = append(content, textBlock(s.S))
		}
		f.writeMessage(w, req.Model, s.Reason, content)
	case ToolUse:
		content := make([]any, 0, len(s.Calls)+1)
		if s.Thinking != "" {
			content = append(content, map[string]any{
				"type": "thinking", "thinking": s.Thinking, "signature": "fake-sig",
			})
		}
		for _, c := range s.Calls {
			content = append(content, map[string]any{
				"type": "tool_use", "id": c.ID, "name": c.Name, "input": c.Input,
			})
		}
		f.writeMessage(w, req.Model, "tool_use", content)
	}
}

func textBlock(s string) map[string]any {
	return map[string]any{"type": "text", "text": s}
}

func (f *Anthropic) writeMessage(w http.ResponseWriter, model, stopReason string, content []any) {
	if content == nil {
		content = []any{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id": "msg_fake", "type": "message", "role": "assistant",
		"model":       model,
		"content":     content,
		"stop_reason": stopReason,
		"usage":       map[string]any{"input_tokens": 100, "output_tokens": 50},
	})
}
