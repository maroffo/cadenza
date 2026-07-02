// ABOUTME: Firestore-backed recipe Book provider: cached, invalidated on write, embed fallback.
// ABOUTME: The coach reads the live book here; if Firestore is unreachable it serves the embed.

package recipes

import (
	"context"
	"log/slog"
	"sync"

	"github.com/maroffo/cadenza/internal/foods"
)

// Source supplies the raw recipe/meal rows the provider builds a Book from;
// satisfied by store.Recipes. Kept minimal so recipes never imports store.
type Source interface {
	ListRecipes(ctx context.Context) ([]Recipe, error)
	ListMeals(ctx context.Context) ([]Meal, error)
}

// BookProvider yields the current recipe Book. Both *Book (static) and *Provider
// (Firestore-backed, mutable) satisfy it, so the coach depends only on this.
type BookProvider interface {
	Book(ctx context.Context) (*Book, error)
}

// Provider builds a Book from a Source, caches it in-process, and rebuilds on
// Invalidate (called after a dashboard write). It never fails the coach: on a
// Source/build error, or before the collection is seeded, it serves the embedded
// seed book. max-instances=1 means a write and the next read share this process,
// so invalidate-then-rebuild is immediately consistent.
type Provider struct {
	src    Source
	cat    *foods.Catalog
	misure map[string]Measure
	seed   *Book // embedded fallback, built strictly at construction

	mu     sync.RWMutex
	cached *Book
}

// NewProvider wires the provider to its Firestore source and the food catalog.
// It strict-loads the embedded book as the fallback: a dirty embed panics here,
// the same guarantee the old MustLoad wiring gave.
func NewProvider(src Source, cat *foods.Catalog) *Provider {
	seed := MustLoad(cat) // strict: the embedded book must be clean
	return &Provider{src: src, cat: cat, misure: seed.misure, seed: seed}
}

// Book returns the cached book, building it from the Source on first use or
// after Invalidate. Errors degrade to the embedded seed, never to the caller.
func (p *Provider) Book(ctx context.Context) (*Book, error) {
	p.mu.RLock()
	b := p.cached
	p.mu.RUnlock()
	if b != nil {
		return b, nil
	}
	return p.rebuild(ctx), nil
}

// Invalidate drops the cache so the next Book() rebuilds from the Source. Call
// it after any recipe/meal write so the coach sees the change immediately.
func (p *Provider) Invalidate() {
	p.mu.Lock()
	p.cached = nil
	p.mu.Unlock()
}

// rebuild loads rows from the Source and builds a fresh Book, caching it on
// success. Any failure (Source error, or an empty/not-yet-seeded collection)
// falls back to the embedded seed WITHOUT caching, so the next call retries.
func (p *Provider) rebuild(ctx context.Context) *Book {
	recipes, rerr := p.src.ListRecipes(ctx)
	meals, merr := p.src.ListMeals(ctx)
	if rerr != nil || merr != nil {
		slog.Warn("recipes: firestore read failed, serving embedded fallback",
			"recipes_err", rerr, "meals_err", merr)
		return p.seed
	}
	if len(recipes) == 0 {
		// Collection not seeded yet: the embed is the source of truth until then.
		slog.Info("recipes: firestore empty, serving embedded fallback")
		return p.seed
	}
	b, problems := buildBook(p.misure, recipes, meals, p.cat)
	if len(problems) > 0 {
		// Quarantined rows are dropped, not fatal: log so a bad dashboard entry
		// is visible without taking the rest of the book down.
		slog.Warn("recipes: quarantined unresolved firestore entries",
			"count", len(problems), "problems", problems)
	}
	p.mu.Lock()
	p.cached = b
	p.mu.Unlock()
	return b
}
