package engine

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"strings"
	"sync"

	"github.com/muthuishere/wfnexus/apps/api/internal/catalog"
	"github.com/muthuishere/wfnexus/apps/api/internal/model"
)

// The price table a step is charged at (ADR 0020). It lives in the database so
// the operator can change it — from `wfx prices`, the System page or the API —
// and is seeded from the approximate table embedded in the binary. The engine
// keeps a copy in memory: a step reads it on every resolve, and a write goes to
// the store first and then replaces the copy.
//
// With no database the seed is the table, and it is read-only.

// priceStore is the part of the store the price table needs. Optional, so a
// test double that knows nothing about prices still satisfies Store.
type priceStore interface {
	ListPrices(ctx context.Context) ([]model.ModelPrice, error)
	PutPrice(ctx context.Context, modelKey string, in, out float64) error
	DeletePrice(ctx context.Context, modelKey string) error
	SeedPrices(ctx context.Context, seed map[string][2]float64) error
}

var ErrNoPriceStore = errors.New("prices are read-only without a database: the built-in approximate table is in use")

type priceTable struct {
	mu sync.RWMutex
	m  map[string]catalog.Price
}

func (e *Engine) priceStore() priceStore {
	if e.store == nil {
		return nil
	}
	ps, _ := e.store.(priceStore)
	return ps
}

// prices is the table a step is priced against right now.
func (e *Engine) prices() map[string]catalog.Price {
	e.priceTab.mu.RLock()
	defer e.priceTab.mu.RUnlock()
	if e.priceTab.m == nil {
		return catalog.Seed()
	}
	return e.priceTab.m
}

// loadPrices seeds the missing families and reads the table back. A failure
// leaves the built-in table in use and says so; it never stops a boot.
func (e *Engine) loadPrices(ctx context.Context) {
	ps := e.priceStore()
	if ps == nil {
		return
	}
	seed := map[string][2]float64{}
	for k, p := range catalog.Seed() {
		seed[k] = [2]float64{p.In, p.Out}
	}
	if err := ps.SeedPrices(ctx, seed); err != nil {
		log.Printf("engine: price table unavailable, using the built-in approximate prices: %v", err)
		return
	}
	if err := e.reloadPrices(ctx); err != nil {
		log.Printf("engine: price table unreadable, using the built-in approximate prices: %v", err)
	}
}

func (e *Engine) reloadPrices(ctx context.Context) error {
	rows, err := e.priceStore().ListPrices(ctx)
	if err != nil {
		return err
	}
	m := make(map[string]catalog.Price, len(rows))
	for _, r := range rows {
		m[r.Model] = catalog.Price{In: r.In, Out: r.Out}
	}
	e.priceTab.mu.Lock()
	e.priceTab.m = m
	e.priceTab.mu.Unlock()
	return nil
}

// ListPrices is the table as the operator sees it. Without a database it is the
// built-in seed, every row marked seeded.
func (e *Engine) ListPrices(ctx context.Context) ([]model.ModelPrice, error) {
	if ps := e.priceStore(); ps != nil {
		return ps.ListPrices(ctx)
	}
	out := []model.ModelPrice{}
	for k, p := range catalog.Seed() {
		out = append(out, model.ModelPrice{Model: k, In: p.In, Out: p.Out, Seeded: true})
	}
	return out, nil
}

// SetPrice sets one family's price per million tokens.
func (e *Engine) SetPrice(ctx context.Context, modelKey string, in, out float64) error {
	modelKey = strings.ToLower(strings.TrimSpace(modelKey))
	if modelKey == "" {
		return fmt.Errorf("a price needs a model")
	}
	for _, v := range []float64{in, out} {
		if v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
			return fmt.Errorf("a price must be a number of dollars per million tokens, 0 or more")
		}
	}
	ps := e.priceStore()
	if ps == nil {
		return ErrNoPriceStore
	}
	if err := ps.PutPrice(ctx, modelKey, in, out); err != nil {
		return err
	}
	return e.reloadPrices(ctx)
}

// DeletePrice removes a family. One the binary ships comes straight back at its
// approximate price — so for those, delete means "reset to the default".
func (e *Engine) DeletePrice(ctx context.Context, modelKey string) error {
	modelKey = strings.ToLower(strings.TrimSpace(modelKey))
	ps := e.priceStore()
	if ps == nil {
		return ErrNoPriceStore
	}
	if err := ps.DeletePrice(ctx, modelKey); err != nil {
		return err
	}
	if p, ok := catalog.Seed()[modelKey]; ok {
		if err := ps.SeedPrices(ctx, map[string][2]float64{modelKey: {p.In, p.Out}}); err != nil {
			return err
		}
	}
	return e.reloadPrices(ctx)
}
