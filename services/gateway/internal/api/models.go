package api

import (
	"errors"
	"fmt"
	"net/http"

	"relaypine.com/gateway/internal/domain"
	"relaypine.com/gateway/internal/store"
)

type modelInputProblem struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Single-model editing and batch imports share the same public configuration
// bounds. Missing billing mode is always metered for existing API clients.
func validateModelInput(m *domain.Model) *modelInputProblem {
	m.BillingMode = m.EffectiveBillingMode()
	if !nameOK(m.ID) || len(m.ID) > 64 || !nameOK(m.Name) || !validProvider(m.Provider) || !nameOK(m.UpstreamModel) || m.MaxOutputTokens < 1 || m.MaxOutputTokens > 65536 {
		return &modelInputProblem{"invalid_model", "Enter a model ID, provider and output cap between 1 and 65,536."}
	}
	if m.BillingMode != domain.BillingMetered && m.BillingMode != domain.BillingUsageOnly {
		return &modelInputProblem{"invalid_billing_mode", "Choose metered or usage-only billing for this model."}
	}
	for _, rate := range []int64{m.InputRate, m.OutputRate, m.CachedRate, m.CacheWriteRate, m.CostInputRate, m.CostOutputRate, m.CostCachedRate, m.CostCacheWriteRate} {
		if rate < 0 || rate > 1000000000 {
			return &modelInputProblem{"invalid_price", "Rates must be between 0 and $1,000 per million tokens."}
		}
		if m.BillingMode == domain.BillingUsageOnly && rate != 0 {
			return &modelInputProblem{"invalid_price", "Usage-only models must have zero sales and upstream cost rates."}
		}
	}
	if m.BillingMode == domain.BillingMetered && m.Enabled && (m.InputRate == 0 || m.OutputRate == 0 || m.CostInputRate == 0 || m.CostOutputRate == 0) {
		return &modelInputProblem{"invalid_price", "Enabled metered models require positive input and output rates for both sales and upstream cost."}
	}
	if m.Enabled && m.BillingMode == domain.BillingUsageOnly {
		return &modelInputProblem{"invalid_price", "Enabled models must have metered prices so usage deducts from the customer's balance."}
	}
	return nil
}

// The console dispatcher enforces an authenticated administrator, exact Origin,
// and POST before reaching this route. Imports never update existing models.
func (s *Server) importModels(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Models    []domain.Model `json:"models"`
		PreviewID string         `json:"preview_id"`
		ModelIDs  []string       `json:"model_ids"`
	}
	if read(r, &input) != nil || len(input.Models) > store.MaxModelImportItems || (input.PreviewID != "" && len(input.Models) > 0) {
		fail(w, 400, "invalid_model_batch", "Choose a price preview or a manually priced batch.")
		return
	}
	if input.PreviewID != "" {
		_, models, err := s.selectedPricePreview(r, input.PreviewID, "import", input.ModelIDs)
		if err != nil {
			fail(w, 409, "price_preview_expired", "Refresh reference prices and review the import again.")
			return
		}
		input.Models = models
	} else {
		// Only explicit manual rates can bypass reference lookup; never trust client
		// claims about an official source or enable an unpriced model.
		for i := range input.Models {
			input.Models[i].PricingReference = nil
			input.Models[i].PricingNote = ""
		}
	}
	if len(input.Models) == 0 || len(input.Models) > store.MaxModelImportItems {
		fail(w, 400, "invalid_model_batch", "Choose between 1 and 400 models to import in one request.")
		return
	}
	type itemProblem struct {
		Index int    `json:"index"`
		ID    string `json:"id"`
		modelInputProblem
	}
	problems := []itemProblem{}
	seen := make(map[string]bool, len(input.Models))
	for i := range input.Models {
		m := &input.Models[i]
		if problem := validateModelInput(m); problem != nil {
			problems = append(problems, itemProblem{i, m.ID, *problem})
		}
		if seen[m.ID] {
			problems = append(problems, itemProblem{i, m.ID, modelInputProblem{"duplicate_model", "Each model ID must appear only once in an import."}})
		}
		seen[m.ID] = true
	}
	if len(problems) > 0 {
		write(w, 400, map[string]any{"error": map[string]any{
			"code": "invalid_model_batch", "message": fmt.Sprintf("Model %d: %s No models were imported.", problems[0].Index+1, problems[0].Message), "items": problems,
		}})
		return
	}
	result, err := s.Store.CreateModels(r.Context(), input.Models)
	if err != nil {
		if errors.Is(err, store.ErrInvalidModelBatch) {
			fail(w, 400, "invalid_model_batch", "Review the selected models and try again. No models were imported.")
			return
		}
		// A commit acknowledgement can be lost after the commit succeeded. Never
		// claim that nothing was saved: an identical retry safely skips those IDs.
		w.Header().Set("Retry-After", "1")
		fail(w, 503, "model_import_unavailable", "Import could not be confirmed. Retry safely; existing models will be skipped.")
		return
	}
	write(w, 200, result)
}
