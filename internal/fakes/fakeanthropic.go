// ABOUTME: Scripted fake of the Anthropic Messages API over httptest, shared by unit and e2e tests.
// ABOUTME: Tests verify the contract AROUND the model (loop mechanics, fallbacks), never its judgment.

package fakes

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
)

// Scripted is one canned response, consumed in order.
type Scripted interface{ isScripted() }

// Text ends the turn with an assistant text block (stop_reason end_turn).
type Text struct{ S string }

// ToolUse asks for one tool call (stop_reason tool_use).
type ToolUse struct {
	ID, Name string
	Input    json.RawMessage
}

// HTTPErr fails the request with a status code (e.g. 429, 529).
type HTTPErr struct {
	Status int
	Body   string
}

func (Text) isScripted()    {}
func (ToolUse) isScripted() {}
func (HTTPErr) isScripted() {}

// CapturedRequest is the decoded body of one /v1/messages call.
type CapturedRequest struct {
	Model     string            `json:"model"`
	MaxTokens int               `json:"max_tokens"`
	System    json.RawMessage   `json:"system"`
	Messages  []json.RawMessage `json:"messages"`
	Tools     []json.RawMessage `json:"tools"`
	Raw       json.RawMessage   `json:"-"`
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

// Push appends more scripted responses (for multi-phase tests).
func (f *Anthropic) Push(s ...Scripted) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.script = append(f.script, s...)
}

func (f *Anthropic) handle(w http.ResponseWriter, r *http.Request) {
	var req CapturedRequest
	_ = json.NewDecoder(r.Body).Decode(&req)

	f.mu.Lock()
	f.Requests = append(f.Requests, req)
	if len(f.script) == 0 {
		f.mu.Unlock()
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprint(w, `{"type":"error","error":{"type":"api_error","message":"fakeanthropic: script exhausted"}}`)
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
		writeJSON(w, map[string]any{
			"id": "msg_fake", "type": "message", "role": "assistant",
			"model":       req.Model,
			"content":     []any{map[string]any{"type": "text", "text": s.S}},
			"stop_reason": "end_turn",
			"usage":       map[string]any{"input_tokens": 100, "output_tokens": 50},
		})
	case ToolUse:
		writeJSON(w, map[string]any{
			"id": "msg_fake", "type": "message", "role": "assistant",
			"model": req.Model,
			"content": []any{map[string]any{
				"type": "tool_use", "id": s.ID, "name": s.Name, "input": s.Input,
			}},
			"stop_reason": "tool_use",
			"usage":       map[string]any{"input_tokens": 100, "output_tokens": 50},
		})
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
