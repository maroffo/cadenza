// ABOUTME: Append-only profile mutations with one-tap confirmation (decision 14).
// ABOUTME: The model proposes, the athlete confirms, a transaction applies: never silently.

package store

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strconv"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	mutationsCollection = "profile_events"
	rulesCollection     = "rules"

	MutationRampCap = "ramp_cap"
	MutationRule    = "rule"

	// tierARampCapStore mirrors verdict's Tier A ceiling: the data layer can
	// only tighten safety bounds (decision 13); enforced again at apply time.
	tierARampCapStore = 6.0
)

type Mutations struct {
	client *firestore.Client
}

func NewMutations(client *firestore.Client) *Mutations {
	return &Mutations{client: client}
}

// Mutation is one proposed profile change, with full provenance.
type Mutation struct {
	Kind        string    `firestore:"kind"` // ramp_cap | rule
	NewValue    string    `firestore:"new_value"`
	OldValue    string    `firestore:"old_value,omitempty"`
	Rationale   string    `firestore:"rationale"`
	SourceQuote string    `firestore:"source_quote"`
	SessionID   string    `firestore:"session_id"`
	ToolUseID   string    `firestore:"tool_use_id"`
	Status      string    `firestore:"status"` // proposed | confirmed | rejected
	CreatedAt   time.Time `firestore:"created_at"`
	ResolvedAt  time.Time `firestore:"resolved_at,omitempty"`
}

// MutationID is deterministic on (session, tool_use): an agent-loop retry
// can never create a duplicate proposal.
func MutationID(sessionID, toolUseID string) string {
	sum := sha256.Sum256([]byte(sessionID + "|" + toolUseID))
	return fmt.Sprintf("mut-%x", sum[:8])
}

// Propose appends the event; AlreadyExists means a retry already proposed
// it, which is success.
func (m *Mutations) Propose(ctx context.Context, id string, mut Mutation) error {
	mut.Status = "proposed"
	mut.CreatedAt = time.Now().UTC()
	_, err := m.client.Collection(mutationsCollection).Doc(id).Create(ctx, mut)
	if status.Code(err) == codes.AlreadyExists {
		return nil
	}
	if err != nil {
		return fmt.Errorf("mutation propose: %w", err)
	}
	return nil
}

// Resolve flips a proposed mutation and, on approval, applies it to the
// materialized profile in the SAME transaction. Double taps and replays
// no-op (status guard); the returned Mutation reflects the final state.
func (m *Mutations) Resolve(ctx context.Context, id string, approve bool) (*Mutation, error) {
	ref := m.client.Collection(mutationsCollection).Doc(id)
	var out Mutation
	err := m.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		snap, err := tx.Get(ref)
		if err != nil {
			return err
		}
		if err := snap.DataTo(&out); err != nil {
			return fmt.Errorf("mutation decode: %w", err)
		}
		if out.Status != "proposed" {
			return nil // already resolved: idempotent
		}
		newStatus := "rejected"
		if approve {
			newStatus = "confirmed"
			if err := m.apply(tx, id, &out); err != nil {
				return err
			}
		}
		out.Status = newStatus
		out.ResolvedAt = time.Now().UTC()
		return tx.Set(ref, map[string]any{
			"status": newStatus, "resolved_at": out.ResolvedAt,
		}, firestore.MergeAll)
	})
	if err != nil {
		return nil, fmt.Errorf("mutation resolve: %w", err)
	}
	return &out, nil
}

func (m *Mutations) apply(tx *firestore.Transaction, id string, mut *Mutation) error {
	switch mut.Kind {
	case MutationRampCap:
		cap, err := strconv.ParseFloat(mut.NewValue, 64)
		if err != nil {
			return fmt.Errorf("ramp_cap value %q: %w", mut.NewValue, err)
		}
		// Tier A clamp at apply time: even a confirmed hostile value cannot
		// loosen past the code ceiling.
		if cap <= 0 || cap > tierARampCapStore {
			return fmt.Errorf("ramp_cap %v outside (0, %v]", cap, tierARampCapStore)
		}
		return tx.Set(m.client.Collection(profileCollection).Doc(profileDocID),
			map[string]any{"ramp_cap": cap}, firestore.MergeAll)
	case MutationRule:
		return tx.Set(m.client.Collection(rulesCollection).Doc("rule-"+id),
			map[string]any{
				"text": mut.NewValue, "active": true,
				"source_mutation": id, "created_at": time.Now().UTC(),
			})
	default:
		return fmt.Errorf("unknown mutation kind %q", mut.Kind)
	}
}

// Rule is one confirmed coaching rule, injected into the Opus prefix.
type Rule struct {
	ID   string `firestore:"-"`
	Text string `firestore:"text"`
}

type Rules struct {
	client *firestore.Client
}

func NewRules(client *firestore.Client) *Rules {
	return &Rules{client: client}
}

func (r *Rules) ListActive(ctx context.Context) ([]Rule, error) {
	docs, err := r.client.Collection(rulesCollection).
		Where("active", "==", true).Documents(ctx).GetAll()
	if err != nil {
		return nil, fmt.Errorf("rules list: %w", err)
	}
	out := make([]Rule, 0, len(docs))
	for _, d := range docs {
		var rule Rule
		if err := d.DataTo(&rule); err != nil {
			return nil, fmt.Errorf("rule decode %s: %w", d.Ref.ID, err)
		}
		rule.ID = d.Ref.ID
		out = append(out, rule)
	}
	return out, nil
}

// ActiveTexts returns just the rule texts, sorted by doc id for prefix
// stability (same rules, same bytes, or the prompt cache never hits).
func (r *Rules) ActiveTexts(ctx context.Context) ([]string, error) {
	rules, err := r.ListActive(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rules))
	for _, rule := range rules {
		out = append(out, rule.Text)
	}
	return out, nil
}
