// ABOUTME: Tests for the telegram_update handler: dedup compensation, commands, callbacks, poison.
// ABOUTME: Includes the two-producer payload contract test (webhook raw body vs polling marshal).

package job

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/maroffo/cadenza/internal/task"
	"github.com/maroffo/cadenza/internal/verdict"
)

type stubDedup struct {
	reserved map[string]bool
	err      error
	released []string
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

func (s *stubDedup) Release(_ context.Context, key string) error {
	delete(s.reserved, key)
	s.released = append(s.released, key)
	return nil
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
	answered  []string
	buttons   []string
	answerErr error
	calls     []string
}

func (s *stubInteractor) AnswerCallback(_ context.Context, id string) error {
	s.calls = append(s.calls, "answer")
	if s.answerErr != nil {
		return s.answerErr
	}
	s.answered = append(s.answered, id)
	return nil
}

func (s *stubInteractor) SendWithButton(_ context.Context, text, label, data string) error {
	s.buttons = append(s.buttons, data)
	s.plain = append(s.plain, text)
	return nil
}

func (s *stubInteractor) Send(ctx context.Context, body string) error {
	s.calls = append(s.calls, "send")
	return s.stubMessenger.Send(ctx, body)
}

type stubComposer struct {
	body string
	v    verdict.Verdict
	err  error
}

func (s stubComposer) Compose(context.Context) (string, verdict.Verdict, error) {
	return s.body, s.v, s.err
}

const allowedID = int64(424242)

func newMessage(out *stubInteractor, dedup *stubDedup, chats *stubChats) Message {
	return Message{
		AllowedUserID: allowedID,
		Dedup:         dedup,
		Chats:         chats,
		Out:           out,
		Status:        stubComposer{body: "☀️ Check mattutino di prova", v: verdict.Verdict{Kind: verdict.Go}},
	}
}

func envelopeFor(t *testing.T, updateID int64, payload string) task.Envelope {
	t.Helper()
	return task.Envelope{
		V: 1, Type: task.TypeTelegramUpdate,
		ID:      task.TelegramUpdateID(updateID),
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

func TestMessage_StatusSendsComposedWithVerdict(t *testing.T) {
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

func TestMessage_TransientFailureReleasesReservation(t *testing.T) {
	// The bug this pins: Reserve-then-fail must not eat the update. A
	// redelivery after a transient failure must reach the user.
	out := &stubInteractor{}
	dedup := newStubDedup()
	m := newMessage(out, dedup, &stubChats{})
	m.Status = stubComposer{err: errors.New("icu 502")}
	env := envelopeFor(t, 10, msgPayload("/status", allowedID))

	if err := m.Run(context.Background(), env); err == nil {
		t.Fatal("Run = nil, want transient error")
	}
	if len(dedup.released) != 1 {
		t.Fatal("reservation not released after transient failure (redelivery would be lost)")
	}

	// Redelivery with a healthy composer now succeeds.
	m.Status = stubComposer{body: "ok", v: verdict.Verdict{Kind: verdict.Go}}
	if err := m.Run(context.Background(), env); err != nil {
		t.Fatalf("redelivery Run: %v", err)
	}
	if len(out.bodies) != 1 {
		t.Fatalf("redelivery did not reach the user: sends = %d", len(out.bodies))
	}
}

func TestMessage_PoisonKeepsReservation(t *testing.T) {
	dedup := newStubDedup()
	m := newMessage(&stubInteractor{}, dedup, &stubChats{})

	err := m.Run(context.Background(), envelopeFor(t, 11, msgPayload("/start", 666)))
	if !errors.Is(err, task.ErrPoison) {
		t.Fatalf("err = %v, want ErrPoison", err)
	}
	if len(dedup.released) != 0 {
		t.Fatal("poison must keep the reservation: retrying cannot fix it")
	}
}

func TestMessage_ChatsSaveFailureIsTransient(t *testing.T) {
	out := &stubInteractor{}
	dedup := newStubDedup()
	chats := &stubChats{err: errors.New("firestore down")}
	m := newMessage(out, dedup, chats)

	err := m.Run(context.Background(), envelopeFor(t, 12, msgPayload("/start", allowedID)))
	if err == nil || errors.Is(err, task.ErrPoison) {
		t.Fatalf("err = %v, want transient error", err)
	}
	if len(out.plain) != 0 {
		t.Fatal("welcome sent despite failed persistence")
	}
	if len(dedup.released) != 1 {
		t.Fatal("reservation must be released for redelivery")
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
}

func TestMessage_CallbackAnsweredBeforeReply(t *testing.T) {
	out := &stubInteractor{}
	m := newMessage(out, newStubDedup(), &stubChats{})
	cb := fmt.Sprintf(`{"update_id":5,"callback_query":{"id":"cbid","data":"ping:1","from":{"id":%d}}}`, allowedID)

	if err := m.Run(context.Background(), envelopeFor(t, 5, cb)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(out.answered) != 1 || out.answered[0] != "cbid" {
		t.Fatalf("callback not answered: %v (stuck spinner)", out.answered)
	}
	if len(out.calls) < 2 || out.calls[0] != "answer" || out.calls[1] != "send" {
		t.Errorf("order = %v, want answer before send", out.calls)
	}
}

func TestMessage_AnswerFailureSuppressesReply(t *testing.T) {
	out := &stubInteractor{answerErr: errors.New("telegram 500")}
	dedup := newStubDedup()
	m := newMessage(out, dedup, &stubChats{})
	cb := fmt.Sprintf(`{"update_id":13,"callback_query":{"id":"cbid","data":"ping:1","from":{"id":%d}}}`, allowedID)

	if err := m.Run(context.Background(), envelopeFor(t, 13, cb)); err == nil {
		t.Fatal("Run = nil, want answer error")
	}
	if len(out.plain) != 0 {
		t.Fatal("reply sent despite failed answer")
	}
	if len(dedup.released) != 1 {
		t.Fatal("transient callback failure must release the reservation")
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

func TestMessage_NonTextMessageIgnored(t *testing.T) {
	out := &stubInteractor{}
	m := newMessage(out, newStubDedup(), &stubChats{})
	photo := fmt.Sprintf(`{"update_id":14,"message":{"from":{"id":%d},"chat":{"id":%d}}}`, allowedID, allowedID)

	if err := m.Run(context.Background(), envelopeFor(t, 14, photo)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(out.plain)+len(out.bodies) != 0 {
		t.Fatal("non-text message produced a reply")
	}
}

func TestMessage_ForgedChatIDIsPoison(t *testing.T) {
	// from.id matches but chat.id points elsewhere: a forged /start would
	// otherwise redirect every future coaching message.
	out := &stubInteractor{}
	m := newMessage(out, newStubDedup(), &stubChats{})
	forged := fmt.Sprintf(`{"update_id":15,"message":{"text":"/start","from":{"id":%d},"chat":{"id":666}}}`, allowedID)

	err := m.Run(context.Background(), envelopeFor(t, 15, forged))
	if !errors.Is(err, task.ErrPoison) {
		t.Fatalf("err = %v, want ErrPoison", err)
	}
	if len(out.plain)+len(out.bodies) != 0 {
		t.Fatal("forged chat got a reply")
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

// TestMessage_PollingPayloadContract pins the two-producer contract: the dev
// polling path marshals go-telegram's models.Update, and tgUpdate must
// extract the same fields it gets from the raw webhook body.
func TestMessage_PollingPayloadContract(t *testing.T) {
	update := models.Update{
		ID: 99,
		Message: &models.Message{
			Text: "/status",
			From: &models.User{ID: allowedID},
			Chat: models.Chat{ID: allowedID},
		},
	}
	raw, err := json.Marshal(update)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var u tgUpdate
	if err := json.Unmarshal(raw, &u); err != nil {
		t.Fatalf("tgUpdate decode: %v", err)
	}
	if u.UpdateID != 99 || u.Message == nil || u.Message.Text != "/status" ||
		u.Message.From.ID != allowedID || u.Message.Chat.ID != allowedID {
		t.Errorf("contract broken: %+v", u)
	}

	cb := models.Update{
		ID:            100,
		CallbackQuery: &models.CallbackQuery{ID: "cb9", Data: "ping:1", From: models.User{ID: allowedID}},
	}
	raw, _ = json.Marshal(cb)
	var uc tgUpdate
	if err := json.Unmarshal(raw, &uc); err != nil {
		t.Fatalf("callback decode: %v", err)
	}
	if uc.CallbackQuery == nil || uc.CallbackQuery.ID != "cb9" || uc.CallbackQuery.From.ID != allowedID {
		t.Errorf("callback contract broken: %+v", uc)
	}
}
