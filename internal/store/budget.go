// ABOUTME: Daily deep-tier call budget: decision 18 enforced mechanically, not by hope.
// ABOUTME: Transactional counter per day; the coach degrades when the budget is spent.

package store

import (
	"context"
	"fmt"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const budgetCollection = "llm_budget"

type Budget struct {
	client *firestore.Client
}

func NewBudget(client *firestore.Client) *Budget {
	return &Budget{client: client}
}

// Spend increments the day's deep-tier counter and reports whether the call
// is within limit. Returns (false, nil) when the budget is exhausted.
func (b *Budget) Spend(ctx context.Context, date string, limit int) (bool, error) {
	ref := b.client.Collection(budgetCollection).Doc(date)
	allowed := false
	err := b.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		count := int64(0)
		snap, err := tx.Get(ref)
		if err != nil && status.Code(err) != codes.NotFound {
			return err
		}
		if snap != nil && snap.Exists() {
			v, err := snap.DataAt("count")
			if err == nil {
				if n, ok := v.(int64); ok {
					count = n
				}
			}
		}
		if count >= int64(limit) {
			allowed = false
			return nil
		}
		allowed = true
		return tx.Set(ref, map[string]any{"count": count + 1})
	})
	if err != nil {
		return false, fmt.Errorf("llm budget: %w", err)
	}
	return allowed, nil
}

// SpentToday reads the day's deep-tier counter (dashboard transparency).
func (b *Budget) SpentToday(ctx context.Context, date string) (int, error) {
	snap, err := b.client.Collection(budgetCollection).Doc(date).Get(ctx)
	if status.Code(err) == codes.NotFound {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("budget read: %w", err)
	}
	if v, err := snap.DataAt("count"); err == nil {
		if n, ok := v.(int64); ok {
			return int(n), nil
		}
	}
	return 0, nil
}
