package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"relaypine.com/gateway/internal/domain"
)

// ModelPriceUpdate is a reviewed configuration update. ExpectedVersion prevents
// a stale price preview from erasing a concurrent administrative edit.
type ModelPriceUpdate struct {
	Model           domain.Model `json:"model"`
	ExpectedVersion int          `json:"expected_version"`
}
type ModelPriceResult struct {
	UpdatedCount int                     `json:"updated_count"`
	Results      []ModelImportItemResult `json:"results"`
}

func (s *Store) UpdateModelPrices(ctx context.Context, edits []ModelPriceUpdate) (ModelPriceResult, error) {
	result := ModelPriceResult{Results: []ModelImportItemResult{}}
	if len(edits) == 0 || len(edits) > MaxModelImportItems {
		return result, ErrInvalidModelBatch
	}
	ordered := append([]ModelPriceUpdate(nil), edits...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Model.ID < ordered[j].Model.ID })
	for i, e := range ordered {
		if e.ExpectedVersion < 1 || e.Model.ID == "" || i > 0 && e.Model.ID == ordered[i-1].Model.ID {
			return result, ErrInvalidModelBatch
		}
		if err := validateModel(e.Model); err != nil {
			return result, err
		}
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	for _, e := range ordered {
		var raw []byte
		var version int
		if err = tx.QueryRowContext(ctx, "SELECT config,version FROM models WHERE id=? FOR UPDATE", e.Model.ID).Scan(&raw, &version); err != nil {
			return result, dbError(err)
		}
		if version != e.ExpectedVersion {
			return result, fmt.Errorf("%w: price preview is stale", ErrConflict)
		}
		var current domain.Model
		if err = json.Unmarshal(raw, &current); err != nil {
			return result, err
		}
		// Only price metadata, billing mode and disabling an unpriced model may
		// change. Publication names, routing aliases and limits remain untouched.
		m := current
		m.BillingMode = e.Model.EffectiveBillingMode()
		m.Kind = domain.ModelKindFor(m.Provider, m.UpstreamModel)
		m.InputRate, m.OutputRate, m.CachedRate, m.CacheWriteRate = e.Model.InputRate, e.Model.OutputRate, e.Model.CachedRate, e.Model.CacheWriteRate
		m.CostInputRate, m.CostOutputRate, m.CostCachedRate, m.CostCacheWriteRate = e.Model.CostInputRate, e.Model.CostOutputRate, e.Model.CostCachedRate, e.Model.CostCacheWriteRate
		m.ImageInputRate, m.ImageOutputRate, m.CostImageInputRate, m.CostImageOutputRate = e.Model.ImageInputRate, e.Model.ImageOutputRate, e.Model.CostImageInputRate, e.Model.CostImageOutputRate
		m.LongContext = e.Model.LongContext
		m.PricingReference, m.PricingNote = e.Model.PricingReference, e.Model.PricingNote
		m.Enabled = current.Enabled && e.Model.Enabled
		m.Version = version + 1
		if err = validateModel(m); err != nil {
			return result, err
		}
		raw, err = json.Marshal(m)
		if err != nil {
			return result, err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE models SET config=?,version=? WHERE id=? AND version=?", raw, m.Version, m.ID, version); err != nil {
			return result, err
		}
		result.UpdatedCount++
		result.Results = append(result.Results, ModelImportItemResult{ID: m.ID, Status: "updated"})
	}
	if err = tx.Commit(); err != nil {
		return ModelPriceResult{}, err
	}
	return result, nil
}
