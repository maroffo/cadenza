// ABOUTME: Maps exercise ids to cached Telegram animation file_ids in Firestore.
// ABOUTME: Re-sending a GIF by file_id skips re-fetching the source on every send.

package store

import (
	"context"
	"fmt"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const mediaCacheCollection = "exercise_media"

type MediaCache struct {
	client *firestore.Client
}

func NewMediaCache(client *firestore.Client) *MediaCache {
	return &MediaCache{client: client}
}

type mediaDoc struct {
	FileID    string    `firestore:"file_id"`
	UpdatedAt time.Time `firestore:"updated_at"`
}

// Get returns the cached Telegram file_id for an exercise. False with nil error
// means there is no cache entry yet (the caller must upload the source and
// Set the resulting file_id). False with non-nil error means the lookup failed
// and the cache state is unknown.
//
// Exercise ids become Firestore document ids verbatim, so they share the dedup
// key charset: an unvalidated id could alter the document path or collide.
func (m *MediaCache) Get(ctx context.Context, exerciseID string) (string, bool, error) {
	if !validDedupKey.MatchString(exerciseID) {
		return "", false, fmt.Errorf("media cache: invalid exercise id %q", exerciseID)
	}
	snap, err := m.client.Collection(mediaCacheCollection).Doc(exerciseID).Get(ctx)
	if status.Code(err) == codes.NotFound {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	var doc mediaDoc
	if err := snap.DataTo(&doc); err != nil {
		return "", false, err
	}
	return doc.FileID, true, nil
}

// Set stores the Telegram file_id for an exercise, overwriting any prior entry.
// An empty fileID is rejected: caching it would make Get return a hit that
// re-sends nothing.
func (m *MediaCache) Set(ctx context.Context, exerciseID, fileID string) error {
	if !validDedupKey.MatchString(exerciseID) {
		return fmt.Errorf("media cache: invalid exercise id %q", exerciseID)
	}
	if fileID == "" {
		return fmt.Errorf("media cache: empty file_id for exercise id %q", exerciseID)
	}
	_, err := m.client.Collection(mediaCacheCollection).Doc(exerciseID).Set(ctx, mediaDoc{
		FileID:    fileID,
		UpdatedAt: time.Now().UTC(),
	})
	return err
}
