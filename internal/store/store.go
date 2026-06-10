// ABOUTME: Firestore client construction; one place that knows the project id.
// ABOUTME: The client honors FIRESTORE_EMULATOR_HOST automatically in dev/tests.

package store

import (
	"context"

	"cloud.google.com/go/firestore"
)

// NewClient connects to Firestore. With FIRESTORE_EMULATOR_HOST set the
// underlying library targets the emulator with fake credentials.
func NewClient(ctx context.Context, projectID string) (*firestore.Client, error) {
	return firestore.NewClient(ctx, projectID)
}
