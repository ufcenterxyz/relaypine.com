package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"relaypine.com/gateway/internal/domain"
	"relaypine.com/gateway/internal/pricing"
	"relaypine.com/gateway/internal/store"
)

func (s *Server) previewModelPrices(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ModelIDs     []string       `json:"model_ids"`
		CredentialID string         `json:"credential_id"`
		Models       []domain.Model `json:"models"`
	}
	if read(r, &input) != nil || len(input.ModelIDs) > store.MaxModelImportItems || len(input.Models) > store.MaxModelImportItems || len(input.Models) > 0 && (input.CredentialID == "" || len(input.ModelIDs) > 0) || len(input.Models) == 0 && input.CredentialID != "" {
		fail(w, 400, "invalid_price_preview", "Choose existing models, or choose a saved credential and up to 400 models to import.")
		return
	}
	if s.Redis == nil {
		fail(w, 503, "price_preview_unavailable", "Price previews are temporarily unavailable.")
		return
	}
	endpoints, err := s.Store.ListEndpoints(r.Context())
	if err != nil {
		s.report(w, err)
		return
	}
	models := input.Models
	kind := "update"
	if len(models) > 0 {
		kind = "import"
		credentials, e := s.Store.ListCredentials(r.Context())
		if e != nil {
			s.report(w, e)
			return
		}
		var endpointID string
		for _, c := range credentials {
			if c.ID == input.CredentialID && c.Enabled {
				endpointID = c.EndpointID
				break
			}
		}
		var chosen domain.Endpoint
		for _, e := range endpoints {
			if e.ID == endpointID && e.Enabled {
				chosen = e
				break
			}
		}
		if chosen.ID == "" {
			fail(w, 400, "credential_unavailable", "Choose an enabled saved credential and upstream.")
			return
		}
		if chosen.IsBackup() {
			// A backup serves existing models under its own catalog names; the
			// catalog itself comes from official or compatible upstreams.
			fail(w, 400, "credential_unavailable", "Import models from an official or compatible upstream. Backup routes serve models that already exist.")
			return
		}
		endpoints = []domain.Endpoint{chosen}
		seen := map[string]bool{}
		for i := range models {
			m := &models[i]
			if seen[m.ID] || m.Provider != chosen.Provider {
				fail(w, 400, "invalid_model_batch", "Each model must have a unique ID and match the credential's provider.")
				return
			}
			seen[m.ID] = true
			// Ignore all client pricing claims in a reference import. The server
			// builds the entire publishable configuration from verified sources.
			m.PricingReference = nil
			m.PricingNote = ""
			m.Version = 0
			m.BillingMode = domain.BillingMetered
			m.InputRate, m.OutputRate, m.CachedRate, m.CacheWriteRate = 0, 0, 0, 0
			m.CostInputRate, m.CostOutputRate, m.CostCachedRate, m.CostCacheWriteRate = 0, 0, 0, 0
			draft := *m
			draft.Enabled = false
			if problem := validateModelInput(&draft); problem != nil {
				fail(w, 400, problem.Code, problem.Message)
				return
			}
		}
	} else {
		models, err = s.Store.ListModels(r.Context())
		if err != nil {
			s.report(w, err)
			return
		}
		if len(input.ModelIDs) > 0 {
			wanted := map[string]bool{}
			for _, id := range input.ModelIDs {
				if wanted[id] {
					fail(w, 400, "invalid_model_batch", "Each model ID must appear only once.")
					return
				}
				wanted[id] = true
			}
			selected := []domain.Model{}
			for _, m := range models {
				if wanted[m.ID] {
					selected = append(selected, m)
					delete(wanted, m.ID)
				}
			}
			if len(wanted) > 0 {
				fail(w, 404, "model_not_found", "A selected model no longer exists.")
				return
			}
			models = selected
		}
	}
	if len(models) == 0 || len(models) > store.MaxModelImportItems {
		fail(w, 400, "invalid_model_batch", "Choose between 1 and 400 models.")
		return
	}
	providers := []string{}
	for _, m := range models {
		providers = append(providers, m.Provider)
	}
	fetch := s.PricingFetcher
	if fetch == nil {
		fetch = pricing.Fetch
	}
	catalog := fetch(r.Context(), providers)
	for _, source := range catalog.Sources {
		if source.Error != "" {
			fail(w, 503, "price_source_unavailable", source.Error)
			return
		}
	}
	preview := pricing.Build(catalog, models, endpoints, kind)
	var id [24]byte
	if _, err = rand.Read(id[:]); err != nil {
		fail(w, 503, "price_preview_unavailable", "Unable to create a price preview.")
		return
	}
	preview.ID = hex.EncodeToString(id[:])
	raw, err := json.Marshal(preview)
	if err == nil {
		err = s.Redis.Set(r.Context(), "rp:price-preview:"+preview.ID, raw, 15*time.Minute).Err()
	}
	if err != nil {
		fail(w, 503, "price_preview_unavailable", "Unable to save the price preview. No model was changed.")
		return
	}
	write(w, 200, preview)
}

func (s *Server) selectedPricePreview(r *http.Request, id, kind string, ids []string) (pricing.Preview, []domain.Model, error) {
	var preview pricing.Preview
	if s.Redis == nil || len(id) != 48 || len(ids) < 1 || len(ids) > store.MaxModelImportItems {
		return preview, nil, store.ErrInvalidModelBatch
	}
	if _, err := hex.DecodeString(id); err != nil {
		return preview, nil, store.ErrInvalidModelBatch
	}
	raw, err := s.Redis.Get(r.Context(), "rp:price-preview:"+id).Bytes()
	if err != nil || json.Unmarshal(raw, &preview) != nil || preview.ID != id || preview.Kind != kind || !preview.ExpiresAt.After(time.Now()) {
		return preview, nil, store.ErrConflict
	}
	for _, source := range preview.Sources {
		if source.Error != "" {
			return preview, nil, store.ErrConflict
		}
	}
	wanted := map[string]bool{}
	for _, id := range ids {
		if wanted[id] {
			return preview, nil, store.ErrInvalidModelBatch
		}
		wanted[id] = true
	}
	selected := []domain.Model{}
	for _, item := range preview.Items {
		if wanted[item.ID] {
			selected = append(selected, item.After)
			delete(wanted, item.ID)
		}
	}
	if len(wanted) > 0 {
		return preview, nil, store.ErrInvalidModelBatch
	}
	return preview, selected, nil
}

func (s *Server) applyModelPrices(w http.ResponseWriter, r *http.Request) {
	var input struct {
		PreviewID string   `json:"preview_id"`
		ModelIDs  []string `json:"model_ids"`
	}
	if read(r, &input) != nil {
		fail(w, 400, "invalid_price_preview", "Choose models from a current price preview.")
		return
	}
	_, models, err := s.selectedPricePreview(r, input.PreviewID, "update", input.ModelIDs)
	if err != nil {
		fail(w, 409, "price_preview_expired", "This price preview expired or is invalid. Refresh the latest prices and review them again.")
		return
	}
	edits := make([]store.ModelPriceUpdate, 0, len(models))
	for _, m := range models {
		edits = append(edits, store.ModelPriceUpdate{Model: m, ExpectedVersion: m.Version})
	}
	result, err := s.Store.UpdateModelPrices(r.Context(), edits)
	if errors.Is(err, store.ErrConflict) {
		fail(w, 409, "price_preview_stale", "A model changed after this preview. Refresh prices to review the current configuration; no prices were updated.")
		return
	}
	if err != nil {
		fail(w, 503, "price_update_unavailable", "Price update could not be confirmed. Refresh the model list before trying again.")
		return
	}
	write(w, 200, result)
}
