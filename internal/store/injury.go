// ABOUTME: Injury registry: Firestore is the truth, named tasks are only wake-ups (decision 17).
// ABOUTME: Opening tightens and needs no confirm; resolving loosens and only the athlete can.

package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const injuriesCollection = "injuries"

// injuryRetention mirrors the other health-data stores: 18 months TTL.
const injuryRetention = 18 * 30 * 24 * time.Hour

// ErrInjuryNotFound marks operations on unknown ids: callers answer the
// athlete honestly instead of confirming actions on ghosts.
var ErrInjuryNotFound = fmt.Errorf("injury not found")

type Injuries struct {
	client *firestore.Client
}

func NewInjuries(client *firestore.Client) *Injuries {
	return &Injuries{client: client}
}

type Injury struct {
	ID             string    `firestore:"-"`
	BodyPart       string    `firestore:"body_part"`
	Pain           int       `firestore:"pain"` // 0-10 athlete-reported
	Notes          string    `firestore:"notes,omitempty"`
	Status         string    `firestore:"status"`                  // open | resolved
	Rev            int       `firestore:"rev"`                     // bumped on resolve/reopen: stale wakeups die
	LastFeedback   string    `firestore:"last_feedback,omitempty"` // better | same | worse
	LastFeedbackAt time.Time `firestore:"last_feedback_at,omitempty"`
	OpenedAt       time.Time `firestore:"opened_at"`
	ResolvedAt     time.Time `firestore:"resolved_at,omitempty"`
	// ExpiresAt drives the TTL policy: injuries are health data outright.
	ExpiresAt time.Time `firestore:"expires_at"`
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

// InjuryID is deterministic on date+body part: re-reporting the same injury
// the same day is idempotent, not a duplicate. The slug is truncated (the id
// rides in 64-byte Telegram callbacks) and backed by a short hash so two
// distinct non-Latin names cannot collide into one doc.
func InjuryID(date, bodyPart string) string {
	slug := strings.Trim(slugRe.ReplaceAllString(strings.ToLower(bodyPart), "-"), "-")
	if len(slug) > 18 {
		slug = strings.Trim(slug[:18], "-")
	}
	sum := sha256.Sum256([]byte(strings.ToLower(bodyPart)))
	if slug == "" {
		return fmt.Sprintf("inj-%s-%x", date, sum[:3])
	}
	return fmt.Sprintf("inj-%s-%s-%x", date, slug, sum[:2])
}

// Open creates the injury; an existing OPEN doc is idempotent success; an
// existing RESOLVED doc is transactionally REOPENED (Rev bump: same-day
// pain-came-back must restore protections, never be swallowed).
func (i *Injuries) Open(ctx context.Context, id string, inj Injury) (*Injury, error) {
	now := time.Now().UTC()
	inj.Status = "open"
	inj.OpenedAt = now
	inj.Rev = 1
	inj.ExpiresAt = now.Add(injuryRetention)
	_, err := i.client.Collection(injuriesCollection).Doc(id).Create(ctx, inj)
	if err == nil {
		if lerr := i.AppendLog(ctx, id, "opened", fmt.Sprintf("%s, dolore %d/10", inj.BodyPart, inj.Pain)); lerr != nil {
			return nil, lerr
		}
		inj.ID = id
		return &inj, nil
	}
	if status.Code(err) != codes.AlreadyExists {
		return nil, fmt.Errorf("injury open: %w", err)
	}
	reopened := false
	ref := i.client.Collection(injuriesCollection).Doc(id)
	terr := i.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		snap, err := tx.Get(ref)
		if err != nil {
			return err
		}
		var cur Injury
		if err := snap.DataTo(&cur); err != nil {
			return err
		}
		if cur.Status == "open" {
			return nil // idempotent re-report
		}
		reopened = true
		return tx.Set(ref, map[string]any{
			"status": "open", "rev": cur.Rev + 1, "pain": inj.Pain,
			"opened_at": now, "last_feedback": "", "expires_at": now.Add(injuryRetention),
		}, firestore.MergeAll)
	})
	if terr != nil {
		return nil, fmt.Errorf("injury reopen: %w", terr)
	}
	if reopened {
		if lerr := i.AppendLog(ctx, id, "reopened", fmt.Sprintf("dolore %d/10", inj.Pain)); lerr != nil {
			return nil, lerr
		}
	}
	return i.Get(ctx, id)
}

func (i *Injuries) Get(ctx context.Context, id string) (*Injury, error) {
	snap, err := i.client.Collection(injuriesCollection).Doc(id).Get(ctx)
	if status.Code(err) == codes.NotFound {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("injury get: %w", err)
	}
	var inj Injury
	if err := snap.DataTo(&inj); err != nil {
		return nil, fmt.Errorf("injury decode: %w", err)
	}
	inj.ID = snap.Ref.ID
	return &inj, nil
}

// ListOpen feeds the verdict and the reconcile healer.
func (i *Injuries) ListOpen(ctx context.Context) ([]Injury, error) {
	docs, err := i.client.Collection(injuriesCollection).
		Where("status", "==", "open").
		OrderBy(firestore.DocumentID, firestore.Asc).Documents(ctx).GetAll()
	if err != nil {
		return nil, fmt.Errorf("injuries list: %w", err)
	}
	out := make([]Injury, 0, len(docs))
	for _, d := range docs {
		var inj Injury
		if err := d.DataTo(&inj); err != nil {
			return nil, fmt.Errorf("injury decode %s: %w", d.Ref.ID, err)
		}
		inj.ID = d.Ref.ID
		out = append(out, inj)
	}
	return out, nil
}

// RecordFeedback stores the athlete's check-in answer (append-only log too).
// Unknown ids return ErrInjuryNotFound: MergeAll on a ghost would otherwise
// CREATE a phantom injury doc.
func (i *Injuries) RecordFeedback(ctx context.Context, id, feedback string) error {
	ref := i.client.Collection(injuriesCollection).Doc(id)
	err := i.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		_, err := tx.Get(ref)
		if status.Code(err) == codes.NotFound {
			return ErrInjuryNotFound
		}
		if err != nil {
			return err
		}
		return tx.Set(ref, map[string]any{
			"last_feedback": feedback, "last_feedback_at": time.Now().UTC(),
		}, firestore.MergeAll)
	})
	if err != nil {
		if errors.Is(err, ErrInjuryNotFound) {
			return ErrInjuryNotFound
		}
		return fmt.Errorf("injury feedback: %w", err)
	}
	return i.AppendLog(ctx, id, "feedback", feedback)
}

// Resolve closes the injury and bumps Rev so any in-flight wakeup task for
// the old revision no-ops. Idempotent on double tap; ghosts are named.
func (i *Injuries) Resolve(ctx context.Context, id string) error {
	ref := i.client.Collection(injuriesCollection).Doc(id)
	err := i.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		snap, err := tx.Get(ref)
		if status.Code(err) == codes.NotFound {
			return ErrInjuryNotFound
		}
		if err != nil {
			return err
		}
		var inj Injury
		if err := snap.DataTo(&inj); err != nil {
			return err
		}
		if inj.Status == "resolved" {
			return nil
		}
		return tx.Set(ref, map[string]any{
			"status": "resolved", "resolved_at": time.Now().UTC(), "rev": inj.Rev + 1,
		}, firestore.MergeAll)
	})
	if err != nil {
		if errors.Is(err, ErrInjuryNotFound) {
			return ErrInjuryNotFound
		}
		return fmt.Errorf("injury resolve: %w", err)
	}
	return i.AppendLog(ctx, id, "resolved", "")
}

// AppendLog writes the next sequenced entry; Create surfaces collisions.
// The log is tiny (a handful of entries per injury), so counting documents
// beats a descending key scan (which Firestore does not support).
func (i *Injuries) AppendLog(ctx context.Context, id, kind, note string) error {
	logCol := i.client.Collection(injuriesCollection).Doc(id).Collection("log")
	docs, err := logCol.Documents(ctx).GetAll()
	if err != nil {
		return fmt.Errorf("injury log seq: %w", err)
	}
	seq := len(docs) + 1
	_, err = logCol.Doc(fmt.Sprintf("%06d", seq)).Create(ctx, map[string]any{
		"ts": time.Now().UTC(), "kind": kind, "note": note,
		"expires_at": time.Now().UTC().Add(injuryRetention),
	})
	if err != nil {
		return fmt.Errorf("injury log append: %w", err)
	}
	return nil
}
