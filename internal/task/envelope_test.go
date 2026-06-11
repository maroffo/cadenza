// ABOUTME: Tests for envelope validation and the in-process Local enqueuer.
// ABOUTME: The envelope shape is load-bearing for every milestone after M2.

package task

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestEnvelopeValidate(t *testing.T) {
	valid := Envelope{V: 1, Type: TypeMorningCheck, ID: "morning-2026-06-10"}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid envelope rejected: %v", err)
	}
	for name, e := range map[string]Envelope{
		"wrong version": {V: 2, Type: TypeMorningCheck, ID: "x"},
		"zero version":  {Type: TypeMorningCheck, ID: "x"},
		"empty type":    {V: 1, ID: "x"},
		"empty id":      {V: 1, Type: TypeMorningCheck},
	} {
		if err := e.Validate(); err == nil {
			t.Errorf("%s: Validate() = nil, want error", name)
		}
	}
}

func TestEnvelopeRoundTripsJSON(t *testing.T) {
	e := Envelope{V: 1, Type: TypeTelegramUpdate, ID: "tg-update-7", Payload: json.RawMessage(`{"update_id":7}`)}
	raw, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back Envelope
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Type != e.Type || back.ID != e.ID || string(back.Payload) != string(e.Payload) {
		t.Errorf("round trip mismatch: %+v vs %+v", back, e)
	}
}

func TestLocalEnqueuer_DispatchesInProcess(t *testing.T) {
	var got Envelope
	l := Local{Dispatch: func(_ context.Context, e Envelope) error {
		got = e
		return nil
	}}
	e := Envelope{V: 1, Type: TypeWatchdog, ID: "watchdog-2026-06-10"}
	if err := l.Enqueue(context.Background(), e); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if got.ID != e.ID {
		t.Errorf("dispatched id = %q, want %q", got.ID, e.ID)
	}
}

func TestLocalEnqueuer_ValidatesBeforeDispatch(t *testing.T) {
	called := false
	l := Local{Dispatch: func(context.Context, Envelope) error {
		called = true
		return nil
	}}
	err := l.Enqueue(context.Background(), Envelope{V: 1, Type: "", ID: "x"})
	if err == nil {
		t.Fatal("invalid envelope accepted")
	}
	if called {
		t.Fatal("dispatch called for invalid envelope")
	}
}

func TestLocalEnqueuer_PropagatesDispatchError(t *testing.T) {
	boom := errors.New("boom")
	l := Local{Dispatch: func(context.Context, Envelope) error { return boom }}
	err := l.Enqueue(context.Background(), Envelope{V: 1, Type: TypeWatchdog, ID: "x"})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
}
