// ABOUTME: Tests for the sender: verdict block always appended, HTML mode, plain-text fallback.
// ABOUTME: Uses an httptest fake of the Telegram Bot API (no network).

package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-telegram/bot"

	"github.com/maroffo/cadenza/internal/verdict"
)

type fakeTelegram struct {
	mu       sync.Mutex
	requests []map[string]string
	// failHTMLOnce makes the first HTML sendMessage fail with a parse error.
	failHTMLOnce bool
	// failOnce500 makes the first sendMessage fail with a 500 (non-parse).
	failOnce500 bool
	// failAllParse makes every parse_mode request fail with a parse error.
	failAllParse bool
	// failPlain makes plain (no parse_mode) requests fail too.
	failPlain bool
	failed    bool
}

func (f *fakeTelegram) handler(w http.ResponseWriter, r *http.Request) {
	if !strings.HasSuffix(r.URL.Path, "/sendMessage") {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{}})
		return
	}
	form := map[string]string{}
	ct := r.Header.Get("Content-Type")
	switch {
	case strings.HasPrefix(ct, "application/json"):
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		for k, v := range payload {
			form[k] = fmt.Sprint(v)
		}
	case strings.HasPrefix(ct, "multipart/form-data"):
		_ = r.ParseMultipartForm(1 << 20)
		for k, vs := range r.MultipartForm.Value {
			if len(vs) > 0 {
				form[k] = vs[0]
			}
		}
	default:
		_ = r.ParseForm()
		for k := range r.PostForm {
			form[k] = r.PostForm.Get(k)
		}
	}
	f.mu.Lock()
	f.requests = append(f.requests, form)
	parseFail := form["parse_mode"] != "" &&
		(f.failAllParse || (f.failHTMLOnce && !f.failed))
	plainFail := form["parse_mode"] == "" && f.failPlain
	serverFail := f.failOnce500 && !f.failed
	if parseFail || serverFail {
		f.failed = true
	}
	f.mu.Unlock()

	switch {
	case plainFail:
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": false, "error_code": 400, "description": "Bad Request: chat not found",
		})
		return
	case serverFail:
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": false, "error_code": 500, "description": "Internal Server Error",
		})
		return
	case parseFail:
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": false, "error_code": 400,
			"description": "Bad Request: can't parse entities: unclosed tag",
		})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok": true, "result": map[string]any{"message_id": 1},
	})
}

func newTestSender(t *testing.T, fake *fakeTelegram) *Sender {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(fake.handler))
	t.Cleanup(ts.Close)
	b, err := bot.New("test-token", bot.WithServerURL(ts.URL), bot.WithSkipGetMe())
	if err != nil {
		t.Fatalf("bot.New: %v", err)
	}
	return NewSender(b, 424242)
}

func sampleVerdict() verdict.Verdict {
	in := verdict.Input{
		Today: verdict.Day{
			Date: "2026-06-10",
			HRV:  func() *float64 { v := 58.0; return &v }(),
		},
		Baselines: verdict.Baselines{HRVMean: 68, HRVSD: 6, RestingHR: 47},
		RampCap:   4.0,
	}
	return verdict.Compute(in, verdict.DefaultRules())
}

func TestSendWithVerdict_AppendsBlockAndUsesHTML(t *testing.T) {
	fake := &fakeTelegram{}
	s := newTestSender(t, fake)

	err := s.SendWithVerdict(context.Background(), "<b>Check</b> body", sampleVerdict())
	if err != nil {
		t.Fatalf("SendWithVerdict: %v", err)
	}
	if len(fake.requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(fake.requests))
	}
	req := fake.requests[0]
	if req["parse_mode"] != "HTML" {
		t.Errorf("parse_mode = %q, want HTML", req["parse_mode"])
	}
	if req["chat_id"] != "424242" {
		t.Errorf("chat_id = %q, want 424242", req["chat_id"])
	}
	if !strings.Contains(req["text"], "VERDETTO") {
		t.Errorf("verdict block not appended:\n%s", req["text"])
	}
	if !strings.Contains(req["text"], "Check") {
		t.Errorf("body lost:\n%s", req["text"])
	}
}

func TestSendWithVerdict_PlainFallbackOnParseError(t *testing.T) {
	fake := &fakeTelegram{failHTMLOnce: true}
	s := newTestSender(t, fake)

	err := s.SendWithVerdict(context.Background(), "<b>Check</b> body", sampleVerdict())
	if err != nil {
		t.Fatalf("SendWithVerdict with fallback: %v", err)
	}
	if len(fake.requests) != 2 {
		t.Fatalf("requests = %d, want 2 (HTML attempt + plain retry)", len(fake.requests))
	}
	retry := fake.requests[1]
	if retry["parse_mode"] != "" {
		t.Errorf("fallback parse_mode = %q, want empty (plain text)", retry["parse_mode"])
	}
	if strings.Contains(retry["text"], "<b>") {
		t.Errorf("fallback text still contains HTML tags:\n%s", retry["text"])
	}
	if !strings.Contains(retry["text"], "VERDETTO") {
		t.Errorf("fallback lost the verdict block:\n%s", retry["text"])
	}
}

func TestSend_LongMessageIsChunked(t *testing.T) {
	fake := &fakeTelegram{}
	s := newTestSender(t, fake)

	long := strings.Repeat("riga di analisi\n\n", 400) // > 4096 chars
	if err := s.Send(context.Background(), long); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(fake.requests) < 2 {
		t.Fatalf("requests = %d, want chunked send", len(fake.requests))
	}
	for n, req := range fake.requests {
		if len(req["text"]) > 4096 {
			t.Errorf("chunk %d over Telegram limit: %d", n, len(req["text"]))
		}
	}
}

func TestSend_NonParseErrorNoFallback(t *testing.T) {
	fake := &fakeTelegram{failOnce500: true}
	s := newTestSender(t, fake)

	err := s.Send(context.Background(), "<b>hi</b>")
	if err == nil {
		t.Fatal("Send = nil, want error on 500")
	}
	for _, req := range fake.requests[1:] {
		if req["parse_mode"] == "" {
			t.Fatal("plain fallback attempted on a non-parse error")
		}
	}
}

func TestSend_FallbackFailureSurfaces(t *testing.T) {
	fake := &fakeTelegram{failAllParse: true, failPlain: true}
	s := newTestSender(t, fake)

	err := s.Send(context.Background(), "<b>hi</b>")
	if err == nil {
		t.Fatal("Send = nil, want error when both HTML and plain fail")
	}
	if !strings.Contains(err.Error(), "plain fallback") {
		t.Errorf("err = %v, want plain fallback failure", err)
	}
}

func TestSend_ParseErrorOnLaterChunkFallsBack(t *testing.T) {
	fake := &fakeTelegram{failHTMLOnce: true}
	s := newTestSender(t, fake)

	long := strings.Repeat("riga\n\n", 800) // forces 2+ chunks
	if err := s.Send(context.Background(), long); err != nil {
		t.Fatalf("Send: %v", err)
	}
	// First chunk: HTML 400 + plain retry; remaining chunks HTML ok.
	if len(fake.requests) < 3 {
		t.Fatalf("requests = %d, want at least 3 (failed HTML, plain retry, next chunk)", len(fake.requests))
	}
	if fake.requests[1]["parse_mode"] != "" {
		t.Error("second request should be the plain retry of chunk 1")
	}
}
