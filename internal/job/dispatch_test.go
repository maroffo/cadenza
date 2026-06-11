// ABOUTME: Tests for the executor dispatch table: routing and poison on unknown types.
// ABOUTME: Every new task type must add a case here when it lands.

package job

import (
	"context"
	"errors"
	"testing"

	"github.com/maroffo/cadenza/internal/task"
)

func TestDispatch_UnhandledTypeIsPoison(t *testing.T) {
	d := Deps{}
	err := d.Dispatch(context.Background(), task.Envelope{V: 1, Type: "future_thing", ID: "x"})
	if !errors.Is(err, task.ErrPoison) {
		t.Fatalf("err = %v, want ErrPoison (retrying cannot make a handler appear)", err)
	}
}

func TestDispatch_RoutesTelegramUpdate(t *testing.T) {
	dedup := newStubDedup()
	d := Deps{Message: newMessage(&stubInteractor{}, dedup, &stubChats{})}
	env := envelopeFor(t, 21, msgPayload("/test", allowedID))
	if err := d.Dispatch(context.Background(), env); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if !dedup.reserved[env.ID] {
		t.Fatal("telegram_update not routed to Message handler")
	}
}
