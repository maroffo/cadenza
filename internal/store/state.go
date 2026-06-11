// ABOUTME: Chat state: the chat id persisted at /start (bots cannot initiate without it).
// ABOUTME: Singleton doc for the single-athlete v1.

package store

import (
	"context"
	"fmt"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	stateCollection = "state"
	chatDocID       = "chat"
)

type Chats struct {
	client *firestore.Client
}

func NewChats(client *firestore.Client) *Chats {
	return &Chats{client: client}
}

type chatDoc struct {
	ChatID        int64     `firestore:"chat_id"`
	UserID        int64     `firestore:"user_id"`
	StartedAt     time.Time `firestore:"started_at"`
	ActiveSession string    `firestore:"active_session,omitempty"`
}

func (c *Chats) Save(ctx context.Context, chatID, userID int64) error {
	_, err := c.client.Collection(stateCollection).Doc(chatDocID).Set(ctx, chatDoc{
		ChatID:    chatID,
		UserID:    userID,
		StartedAt: time.Now().UTC(),
	})
	if err != nil {
		return fmt.Errorf("state/chat set: %w", err)
	}
	return nil
}

// SetActiveSession points the chat at its current conversation.
func (c *Chats) SetActiveSession(ctx context.Context, sessionID string) error {
	_, err := c.client.Collection(stateCollection).Doc(chatDocID).
		Set(ctx, map[string]any{"active_session": sessionID}, firestore.MergeAll)
	if err != nil {
		return fmt.Errorf("state/chat active session: %w", err)
	}
	return nil
}

// ActiveSession returns the current conversation id ("" = none yet).
func (c *Chats) ActiveSession(ctx context.Context) (string, error) {
	snap, err := c.client.Collection(stateCollection).Doc(chatDocID).Get(ctx)
	if status.Code(err) == codes.NotFound {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("state/chat get: %w", err)
	}
	var doc chatDoc
	if err := snap.DataTo(&doc); err != nil {
		return "", fmt.Errorf("state/chat decode: %w", err)
	}
	return doc.ActiveSession, nil
}

// Get returns (chatID, userID); (0, 0, nil) when /start never happened.
func (c *Chats) Get(ctx context.Context) (int64, int64, error) {
	snap, err := c.client.Collection(stateCollection).Doc(chatDocID).Get(ctx)
	if status.Code(err) == codes.NotFound {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, fmt.Errorf("state/chat get: %w", err)
	}
	var doc chatDoc
	if err := snap.DataTo(&doc); err != nil {
		return 0, 0, fmt.Errorf("state/chat decode: %w", err)
	}
	return doc.ChatID, doc.UserID, nil
}
