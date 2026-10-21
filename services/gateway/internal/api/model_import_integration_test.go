package api

import (
	"fmt"
	"net/http"
	"testing"

	"relaypine.com/gateway/internal/domain"
	"relaypine.com/gateway/internal/store"
)

func TestModelPublicationRequiresUsablePrices(t *testing.T) {
	f := newAPIFixture(t)
	m := domain.Model{ID: f.prefix + "publish", Name: "Imported Claude", Provider: "anthropic", UpstreamModel: "claude-test", Enabled: true, InputRate: 4000000, OutputRate: 20000000, CostInputRate: 4000000, CostOutputRate: 20000000, MaxOutputTokens: 4096}
	f.models = append(f.models, m.ID)
	for _, field := range []string{"input", "output", "cost_input", "cost_output"} {
		t.Run(field, func(t *testing.T) {
			invalid := m
			switch field {
			case "input":
				invalid.InputRate = 0
			case "output":
				invalid.OutputRate = 0
			case "cost_input":
				invalid.CostInputRate = 0
			case "cost_output":
				invalid.CostOutputRate = 0
			}
			expectStatus(t, f.call(t, "POST", "/api/admin/models", f.operatorCookie, f.s.AppURL, invalid), 400)
			var count int
			if err := f.db.DB.QueryRowContext(f.ctx, "SELECT COUNT(*) FROM models WHERE id=?", m.ID).Scan(&count); err != nil || count != 0 {
				t.Fatalf("invalid model was persisted: count=%d err=%v", count, err)
			}
		})
	}
	// Incomplete drafts may be saved, but cannot be published until priced.
	draft := m
	draft.Enabled, draft.CostInputRate, draft.CostOutputRate = false, 0, 0
	expectStatus(t, f.call(t, "POST", "/api/admin/models", f.operatorCookie, f.s.AppURL, draft), 200)
	draft.Enabled = true
	expectStatus(t, f.call(t, "POST", "/api/admin/models", f.operatorCookie, f.s.AppURL, draft), 400)
	expectStatus(t, f.call(t, "POST", "/api/admin/models", f.operatorCookie, f.s.AppURL, m), 200)
}

func usageOnlyImportModel(id string) domain.Model {
	return domain.Model{ID: id, Name: "Imported model", Provider: "anthropic", UpstreamModel: "claude-test", BillingMode: domain.BillingUsageOnly, Enabled: true, MaxOutputTokens: 4096}
}

func TestUsageOnlyModelPublicationAndLegacyNormalization(t *testing.T) {
	f := newAPIFixture(t)
	m := usageOnlyImportModel(f.prefix + "usage")
	f.models = append(f.models, m.ID)
	w := f.call(t, "POST", "/api/admin/models", f.operatorCookie, f.s.AppURL, m)
	expectStatus(t, w, 200)
	if saved := decodeResponse[domain.Model](t, w); saved.BillingMode != domain.BillingUsageOnly || !saved.Enabled || saved.Version != 1 {
		t.Fatal("usage-only model did not persist its explicit billing mode")
	}
	m.BillingMode, m.InputRate, m.OutputRate, m.CostInputRate, m.CostOutputRate = "", 1, 2, 1, 2
	w = f.call(t, "POST", "/api/admin/models", f.operatorCookie, f.s.AppURL, m)
	expectStatus(t, w, 200)
	if saved := decodeResponse[domain.Model](t, w); saved.BillingMode != domain.BillingMetered || saved.Version != 2 {
		t.Fatal("missing mode was not saved as metered")
	}
	// Historical catalog JSON has no mode. Listing normalizes it without
	// changing its price version or migrating it to usage-only billing.
	if _, err := f.db.DB.ExecContext(f.ctx, "UPDATE models SET config=JSON_REMOVE(config,'$.billing_mode') WHERE id=?", m.ID); err != nil {
		t.Fatal(err)
	}
	models, err := f.db.ListModels(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, saved := range models {
		if saved.ID == m.ID && (saved.BillingMode != domain.BillingMetered || saved.Version != 2 || saved.InputRate != 1) {
			t.Fatal("legacy billing mode normalization changed its price configuration")
		}
	}
}

func TestBatchModelImportCreateOnlyRetryAndResponseOrder(t *testing.T) {
	f := newAPIFixture(t)
	first, existing, last := usageOnlyImportModel(f.prefix+"a"), usageOnlyImportModel(f.prefix+"b"), usageOnlyImportModel(f.prefix+"c")
	f.models = append(f.models, first.ID, existing.ID, last.ID)
	existing.BillingMode = domain.BillingMetered
	existing.Name, existing.InputRate, existing.OutputRate, existing.CostInputRate, existing.CostOutputRate = "Original priced model", 7, 9, 5, 6
	if _, err := f.db.CreateModel(f.ctx, existing); err != nil {
		t.Fatal(err)
	}
	conflict := usageOnlyImportModel(existing.ID)
	conflict.Name, conflict.Provider = "Must not replace original", "openai"
	last.Version = 9000
	input := map[string]any{"models": []domain.Model{last, conflict, first}}
	w := f.call(t, "POST", "/api/admin/models/import", f.operatorCookie, f.s.AppURL, input)
	expectStatus(t, w, 200)
	result := decodeResponse[store.ModelImportResult](t, w)
	if result.CreatedCount != 2 || result.SkippedCount != 1 || len(result.Results) != 3 {
		t.Fatalf("unexpected import result: %+v", result)
	}
	for i, expected := range []struct{ id, status, reason string }{{last.ID, "created", ""}, {existing.ID, "skipped", "already_exists"}, {first.ID, "created", ""}} {
		item := result.Results[i]
		if item.ID != expected.id || item.Status != expected.status || item.Reason != expected.reason {
			t.Fatal("results did not preserve input order or identify conflicts")
		}
	}
	w = f.call(t, "POST", "/api/admin/models/import", f.operatorCookie, f.s.AppURL, input)
	expectStatus(t, w, 200)
	result = decodeResponse[store.ModelImportResult](t, w)
	if result.CreatedCount != 0 || result.SkippedCount != 3 {
		t.Fatal("retry duplicated or overwrote existing models")
	}
	models, err := f.db.ListModels(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, saved := range models {
		switch saved.ID {
		case existing.ID:
			found++
			if saved.Name != existing.Name || saved.Provider != existing.Provider || saved.BillingMode != domain.BillingMetered || saved.InputRate != 7 || saved.OutputRate != 9 || saved.Version != 1 {
				t.Fatal("import changed an existing model's configuration or prices")
			}
		case first.ID, last.ID:
			found++
			if saved.Version != 1 || saved.BillingMode != domain.BillingUsageOnly || !saved.Enabled {
				t.Fatal("new model accepted injected version or lost usage-only mode")
			}
		}
	}
	if found != 3 {
		t.Fatal("created model missing after committed batch")
	}
}

func TestBatchModelImportRejectsInvalidItemsWithoutPartialWrites(t *testing.T) {
	f := newAPIFixture(t)
	first, second := usageOnlyImportModel(f.prefix+"a"), usageOnlyImportModel(f.prefix+"b")
	f.models = append(f.models, first.ID, second.ID)
	for _, tc := range []struct {
		name   string
		change func(*domain.Model)
	}{
		{"unknown billing mode", func(m *domain.Model) { m.BillingMode = "free" }},
		{"missing billing mode defaults metered", func(m *domain.Model) { m.BillingMode = "" }},
		{"usage-only nonzero cache rate", func(m *domain.Model) { m.CostCachedRate = 1 }},
		{"zero cap", func(m *domain.Model) { m.MaxOutputTokens = 0 }},
		{"excessive cap", func(m *domain.Model) { m.MaxOutputTokens = 65537 }},
		{"unsupported provider", func(m *domain.Model) { m.Provider = "other" }},
		{"duplicate input ID", func(m *domain.Model) { m.ID = first.ID }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			invalid := second
			tc.change(&invalid)
			w := f.call(t, "POST", "/api/admin/models/import", f.operatorCookie, f.s.AppURL, map[string]any{"models": []domain.Model{first, invalid}})
			expectStatus(t, w, 400)
			var count int
			if err := f.db.DB.QueryRowContext(f.ctx, "SELECT COUNT(*) FROM models WHERE id IN (?,?)", first.ID, second.ID).Scan(&count); err != nil || count != 0 {
				t.Fatalf("invalid batch partially wrote models: count=%d err=%v", count, err)
			}
		})
	}
}

func TestBatchModelImportAuthorizationAndDiscoverySizeBound(t *testing.T) {
	f := newAPIFixture(t)
	m := usageOnlyImportModel(f.prefix + "auth")
	f.models = append(f.models, m.ID)
	input := map[string]any{"models": []domain.Model{m}}
	for _, tc := range []struct {
		method, origin string
		cookie         *http.Cookie
		status         int
	}{
		{"POST", f.s.AppURL, nil, 401},
		{"POST", f.s.AppURL, f.customerCookie, 401},
		{"POST", "https://foreign.example", f.operatorCookie, 403},
		{"POST", "", f.operatorCookie, 403},
		{"GET", "", f.operatorCookie, 405},
		{"PUT", f.s.AppURL, f.operatorCookie, 405},
	} {
		expectStatus(t, f.call(t, tc.method, "/api/admin/models/import", tc.cookie, tc.origin, input), tc.status)
	}
	for _, invalid := range []any{
		map[string]any{"models": []domain.Model{}},
		map[string]any{"models": make([]domain.Model, 401)},
		map[string]any{"models": []domain.Model{m}, "overwrite": true},
	} {
		expectStatus(t, f.call(t, "POST", "/api/admin/models/import", f.operatorCookie, f.s.AppURL, invalid), 400)
	}
	batch := make([]domain.Model, 400)
	for i := range batch {
		batch[i] = usageOnlyImportModel(fmt.Sprintf("%s-size-%03d", f.prefix, i))
		f.models = append(f.models, batch[i].ID)
	}
	w := f.call(t, "POST", "/api/admin/models/import", f.operatorCookie, f.s.AppURL, map[string]any{"models": batch})
	expectStatus(t, w, 200)
	if result := decodeResponse[store.ModelImportResult](t, w); result.CreatedCount != 400 || result.SkippedCount != 0 || len(result.Results) != 400 {
		t.Fatal("full bounded discovery list could not be imported")
	}
}

func TestConcurrentBatchModelImportsNeverOverwrite(t *testing.T) {
	f := newAPIFixture(t)
	a, b := usageOnlyImportModel(f.prefix+"concurrent-a"), usageOnlyImportModel(f.prefix+"concurrent-b")
	f.models = append(f.models, a.ID, b.ID)
	type outcome struct {
		result store.ModelImportResult
		err    error
	}
	start := make(chan struct{})
	results := make(chan outcome, 2)
	for _, models := range [][]domain.Model{{a, b}, {b, a}} {
		go func(models []domain.Model) {
			<-start
			result, err := f.db.CreateModels(f.ctx, models)
			results <- outcome{result, err}
		}(models)
	}
	close(start)
	created, skipped := 0, 0
	for i := 0; i < 2; i++ {
		got := <-results
		if got.err != nil {
			t.Fatalf("concurrent batch failed: %v", got.err)
		}
		created += got.result.CreatedCount
		skipped += got.result.SkippedCount
	}
	if created != 2 || skipped != 2 {
		t.Fatal("overlapping batches duplicated or replaced existing models")
	}
}

func TestModelImportCannotOverwritePublishedCatalogEntry(t *testing.T) {
	f := newAPIFixture(t)
	m := domain.Model{ID: f.prefix + "import", Name: "Imported Claude", Provider: "anthropic", UpstreamModel: "claude-test", Enabled: true, InputRate: 4000000, OutputRate: 20000000, CostInputRate: 4000000, CostOutputRate: 20000000, MaxOutputTokens: 4096}
	f.models = append(f.models, m.ID)
	input := struct {
		domain.Model
		CreateOnly bool `json:"create_only"`
	}{m, true}
	expectStatus(t, f.call(t, "POST", "/api/admin/models", f.operatorCookie, f.s.AppURL, input), 200)
	input.Name, input.OutputRate = "Overwrite attempt", 1
	expectStatus(t, f.call(t, "POST", "/api/admin/models", f.operatorCookie, f.s.AppURL, input), 409)
	models, err := f.db.ListModels(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, saved := range models {
		if saved.ID == m.ID {
			found = true
			if saved.Name != m.Name || saved.OutputRate != m.OutputRate || saved.Version != 1 {
				t.Fatal("duplicate import modified the published model")
			}
		}
	}
	if !found {
		t.Fatal("imported model missing")
	}
	// Ordinary editing retains the versioned update contract.
	m.OutputRate++
	w := f.call(t, "POST", "/api/admin/models", f.operatorCookie, f.s.AppURL, m)
	expectStatus(t, w, 200)
	if decodeResponse[domain.Model](t, w).Version != 2 {
		t.Fatal("model edit did not advance pricing version")
	}
}
