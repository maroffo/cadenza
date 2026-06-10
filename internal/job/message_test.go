// ABOUTME: Tests for the telegram_update handler: dedup, commands, callbacks, allowlist poison.
// ABOUTME: All dependencies stubbed; the /status path reuses real Morning.Compose.

package job

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/maroffo/cadenza/internal/icu"
	"github.com/maroffo/cadenza/internal/task"
)

type stubDedup struct {
	reserved map[string]bool
	err      error
}

func newStubDedup() *stubDedup { return &stubDedup{reserved: map[string]bool{}} }

func (s *stubDedup) Reserve(_ context.Context, key string, _ time.Duration) (bool, error) {
	if s.err != nil {
		return false, s.err
	}
	if s.reserved[key] {
		return false, nil
	}
	s.reserved[key] = true
	return true, nil
}

type stubChats struct {
	chatID, userID int64
	err            error
}

func (s *stubChats) Save(_ context.Context, chatID, userID int64) error {
	if s.err != nil {
		return s.err
	}
	s.chatID, s.userID = chatID, userID
	return nil
}

type stubInteractor struct {
	stubMessenger
	answered []string
	buttons  []string
}

func (s *stubInteractor) AnswerCallback(_ context.Context, id string) error {
	s.answered = append(s.answered, id)
	return nil
}

func (s *stubInteractor) SendWithButton(_ context.Context, text, label, data string) error {
	s.buttons = append(s.buttons, data)
	s.plain = append(s.plain, text)
	return nil
}

const allowedID = int64(424242)

func newMessage(out *stubInteractor, dedup *stubDedup, chats *stubChats) Message {
	return Message{
		AllowedUserID: allowedID,
		Dedup:         dedup,
		Chats:         chats,
		Out:           out,
		Morning: newMorning(stubWellness{days: []icu.Wellness{
			green("2026-06-09"), green("2026-06-10"),
		}}, &stubMessenger{}, newStubRuns()),
	}
}

func envelopeFor(t *testing.T, updateID int64, payload string) task.Envelope {
	t.Helper()
	return task.Envelope{
		V: 1, Type: task.TypeTelegramUpdate,
		ID:      fmt.Sprintf("tg-update-%d", updateID),
		Payload: json.RawMessage(payload),
	}
}

func msgPayload(text string, fromID int64) string {
	return fmt.Sprintf(`{"update_id":1,"message":{"text":%q,"from":{"id":%d},"chat":{"id":%d}}}`, text, fromID, fromID)
}

func TestMessage_StartPersistsChatAndWelcomes(t *testing.T) {
	out := &stubInteractor{}
	chats := &stubChats{}
	m := newMessage(out, newStubDedup(), chats)

	err := m.Run(context.Background(), envelopeFor(t, 1, msgPayload("/start", allowedID)))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if chats.chatID != allowedID || chats.userID != allowedID {
		t.Errorf("chat not persisted: %+v", chats)
	}
	if len(out.plain) != 1 || !strings.Contains(out.plain[0], "Cadenza attivo") {
		t.Errorf("welcome missing: %v", out.plain)
	}
}

func TestMessage_StatusSendsMorningWithVerdict(t *testing.T) {
	out := &stubInteractor{}
	m := newMessage(out, newStubDedup(), &stubChats{})

	err := m.Run(context.Background(), envelopeFor(t, 2, msgPayload("/status", allowedID)))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(out.bodies) != 1 {
		t.Fatalf("verdict sends = %d, want 1", len(out.bodies))
	}
	if !strings.Contains(out.bodies[0], "Check mattutino") {
		t.Errorf("status body unexpected:\n%s", out.bodies[0])
	}
}

func TestMessage_DuplicateUpdateIsNoop(t *testing.T) {
	out := &stubInteractor{}
	dedup := newStubDedup()
	m := newMessage(out, dedup, &stubChats{})
	env := envelopeFor(t, 3, msgPayload("/status", allowedID))

	if err := m.Run(context.Background(), env); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	if err := m.Run(context.Background(), env); err != nil {
		t.Fatalf("replay Run: %v", err)
	}
	if got := len(out.bodies) + len(out.plain); got != 1 {
		t.Fatalf("sends = %d, want exactly 1 (Telegram retry must no-op)", got)
	}
}

func TestMessage_TestCommandSendsButton(t *testing.T) {
	out := &stubInteractor{}
	m := newMessage(out, newStubDedup(), &stubChats{})

	if err := m.Run(context.Background(), envelopeFor(t, 4, msgPayload("/test", allowedID))); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(out.buttons) != 1 || out.buttons[0] != "ping:1" {
		t.Errorf("buttons = %v", out.buttons)
	}
	if len(out.buttons[0]) > 64 {
		t.Error("callback_data over Telegram's 64-byte limit")
	}
}

func TestMessage_CallbackAnsweredThenHandled(t *testing.T) {
	out := &stubInteractor{}
	m := newMessage(out, newStubDedup(), &stubChats{})
	cb := fmt.Sprintf(`{"update_id":5,"callback_query":{"id":"cbid","data":"ping:1","from":{"id":%d}}}`, allowedID)

	if err := m.Run(context.Background(), envelopeFor(t, 5, cb)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(out.answered) != 1 || out.answered[0] != "cbid" {
		t.Fatalf("callback not answered: %v (stuck spinner)", out.answered)
	}
	if len(out.plain) != 1 || !strings.Contains(out.plain[0], "Bottone") {
		t.Errorf("ack message missing: %v", out.plain)
	}
}

func TestMessage_FreeTextGetsHonestNotice(t *testing.T) {
	out := &stubInteractor{}
	m := newMessage(out, newStubDedup(), &stubChats{})

	if err := m.Run(context.Background(), envelopeFor(t, 6, msgPayload("come sto?", allowedID))); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(out.plain) != 1 || !strings.Contains(out.plain[0], "prossima versione") {
		t.Errorf("notice = %v", out.plain)
	}
}

func TestMessage_ForeignSenderIsPoison(t *testing.T) {
	out := &stubInteractor{}
	m := newMessage(out, newStubDedup(), &stubChats{})

	err := m.Run(context.Background(), envelopeFor(t, 7, msgPayload("/start", 666)))
	if !errors.Is(err, task.ErrPoison) {
		t.Fatalf("err = %v, want ErrPoison (defense in depth behind the webhook)", err)
	}
	if len(out.plain)+len(out.bodies) != 0 {
		t.Fatal("foreign sender got a reply")
	}
}

func TestMessage_UnparseablePayloadIsPoison(t *testing.T) {
	m := newMessage(&stubInteractor{}, newStubDedup(), &stubChats{})
	env := task.Envelope{V: 1, Type: task.TypeTelegramUpdate, ID: "tg-update-8", Payload: json.RawMessage(`{broken`)}
	if err := m.Run(context.Background(), env); !errors.Is(err, task.ErrPoison) {
		t.Fatalf("err = %v, want ErrPoison", err)
	}
}

func TestMessage_DedupErrorPropagates(t *testing.T) {
	dedup := newStubDedup()
	dedup.err = errors.New("firestore down")
	m := newMessage(&stubInteractor{}, dedup, &stubChats{})

	err := m.Run(context.Background(), envelopeFor(t, 9, msgPayload("/status", allowedID)))
	if err == nil || errors.Is(err, task.ErrPoison) {
		t.Fatalf("err = %v, want transient (retryable) error", err)
	}
}
