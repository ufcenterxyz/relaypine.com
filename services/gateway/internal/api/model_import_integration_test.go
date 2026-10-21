package api

import (
	"testing"

	"relaypine.com/gateway/internal/domain"
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
