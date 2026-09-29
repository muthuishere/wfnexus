package store

import (
	"context"

	"github.com/muthuishere/wfnexus/apps/api/internal/model"
)

// The price table (ADR 0020): what each model family costs. Seeded from the
// approximate table the binary carries; edited by the operator from then on.

// ListPrices returns every row, ordered by model.
func (s *Store) ListPrices(ctx context.Context) ([]model.ModelPrice, error) {
	rows, err := s.query(ctx,
		`SELECT model, price_in, price_out, seeded, updated_at FROM model_prices ORDER BY model`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.ModelPrice{}
	for rows.Next() {
		var p model.ModelPrice
		if err := rows.Scan(&p.Model, &p.In, &p.Out, &p.Seeded, &p.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PutPrice sets one family's price. A person's edit: the row stops being seeded.
func (s *Store) PutPrice(ctx context.Context, modelKey string, in, out float64) error {
	_, err := s.exec(ctx, `
		INSERT INTO model_prices (model, price_in, price_out, seeded, updated_at)
		VALUES ($1,$2,$3, false, now())
		ON CONFLICT (model) DO UPDATE SET
			price_in=EXCLUDED.price_in, price_out=EXCLUDED.price_out, seeded=false, updated_at=now()`,
		modelKey, in, out)
	return err
}

func (s *Store) DeletePrice(ctx context.Context, modelKey string) error {
	_, err := s.exec(ctx, `DELETE FROM model_prices WHERE model=$1`, modelKey)
	return err
}

// SeedPrices inserts the families the table does not have. An existing row —
// seeded or edited — is never touched, so a boot cannot undo an operator.
func (s *Store) SeedPrices(ctx context.Context, seed map[string][2]float64) error {
	for k, p := range seed {
		if _, err := s.exec(ctx, `
			INSERT INTO model_prices (model, price_in, price_out, seeded, updated_at)
			VALUES ($1,$2,$3, true, now())
			ON CONFLICT (model) DO NOTHING`, k, p[0], p[1]); err != nil {
			return err
		}
	}
	return nil
}
