package store

import (
	"encoding/json"
	"strings"
	"testing"

	"relaypine.com/gateway/internal/domain"
)

// flexPrice is half of a model's standard rates, as OpenAI prices Flex.
func flexPrice(m domain.Model) *domain.FlexPrice {
	return &domain.FlexPrice{InputRate: m.InputRate / 2, OutputRate: m.OutputRate / 2, CachedRate: m.CachedRate / 2, CacheWriteRate: m.CacheWriteRate / 2,
		CostInputRate: m.CostInputRate / 2, CostOutputRate: m.CostOutputRate / 2, CostCachedRate: m.CostCachedRate / 2, CostCacheWriteRate: m.CostCacheWriteRate / 2}
}

// A Flex request's price snapshot records the tier it asked for beside every
// price it was admitted under, and its usage the tier OpenAI ran it at. A
// standard request's snapshot is the bare model, as before tiers existed.
func TestMySQLFlexRequestRecordsItsTier(t *testing.T) {
	f := integrationFixture(t)
	if err := f.s.Credit(f.ctx, f.user.TenantID, 1000, "test:flex-credit", "Flex test funds"); err != nil {
		t.Fatal(err)
	}
	flexModel := f.model
	flexModel.Flex = flexPrice(f.model)
	flex := f.reservation(60, 30)
	flex.Model = flexModel
	flex.Request.ServiceTier = domain.ServiceTierFlex
	if err := f.s.Reserve(f.ctx, flex); err != nil {
		t.Fatal(err)
	}
	if pending := requestFromSnapshot(t, f, flex.Request.ID); pending.ServiceTier != domain.ServiceTierFlex || pending.Usage.Known {
		t.Fatalf("pending Flex request %+v", pending)
	}
	usage := domain.Usage{InputTokens: 20, OutputTokens: 10, ServiceTier: domain.ServiceTierFlex, Known: true}
	if err := f.s.Settle(f.ctx, flex.Request.ID, domain.Settlement{Status: "completed", Usage: usage, ChargeMicros: 20, CostMicros: 10}); err != nil {
		t.Fatal(err)
	}
	got := requestFromSnapshot(t, f, flex.Request.ID)
	if got.ServiceTier != domain.ServiceTierFlex || got.Usage.ServiceTier != domain.ServiceTierFlex || got.ChargeMicros != 20 || got.CostStatus != "recorded" {
		t.Fatalf("settled Flex request %+v", got)
	}
	if w := f.wallet(t); w.BalanceMicros != 980 || w.HeldMicros != 0 {
		t.Fatalf("wallet %+v", w)
	}
	var reason string
	if err := f.s.DB.QueryRowContext(f.ctx, "SELECT reason FROM ledger_entries WHERE tenant_id=? AND reference=?", f.user.TenantID, "request:"+flex.Request.ID).Scan(&reason); err != nil || reason != "API usage settlement at Flex rates" {
		t.Fatalf("ledger reason %q: %v", reason, err)
	}

	standard := f.reservation(10, 5)
	if err := f.s.Reserve(f.ctx, standard); err != nil {
		t.Fatal(err)
	}
	var snapshot []byte
	if err := f.s.DB.QueryRowContext(f.ctx, "SELECT price_snapshot FROM requests WHERE id=?", standard.Request.ID).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	var stored, bare map[string]any
	model, _ := json.Marshal(f.model)
	if json.Unmarshal(snapshot, &stored) != nil || json.Unmarshal(model, &bare) != nil || len(stored) != len(bare) || strings.Contains(string(snapshot), "service_tier") {
		t.Fatalf("standard snapshot %s differs from the model %s", snapshot, model)
	}
	if got := requestFromSnapshot(t, f, standard.Request.ID); got.ServiceTier != "" {
		t.Fatalf("standard request recorded tier %q", got.ServiceTier)
	}
	if err := f.s.Settle(f.ctx, standard.Request.ID, domain.Settlement{Status: "completed", Usage: domain.Usage{InputTokens: 1, ServiceTier: "priority", Known: true}, ChargeMicros: 1}); err == nil {
		t.Fatal("a settlement at an unpriced tier was accepted")
	}

	// A Flex reservation needs a Flex price in its snapshot.
	unpriced := f.reservation(10, 5)
	unpriced.Request.ServiceTier = domain.ServiceTierFlex
	if err := f.s.Reserve(f.ctx, unpriced); err == nil {
		t.Fatal("a Flex request without a Flex price was reserved")
	}
	var count int
	if err := f.s.DB.QueryRowContext(f.ctx, "SELECT COUNT(*) FROM requests WHERE id=?", unpriced.Request.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("refused reservation left %d rows: %v", count, err)
	}
}

// A Flex price is official only with its reference: a reviewed price update
// stores it, an edit that keeps the rates keeps it, a client can never set
// one, a manual price drops it, and customers see its sales rates only.
func TestMySQLFlexPricesFollowTheirReference(t *testing.T) {
	f := integrationFixture(t)
	referenced := f.model
	referenced.Flex = flexPrice(f.model)
	referenced.PricingReference = &domain.PriceReference{Provider: "openai", Model: "test-model", SourceURL: "https://developers.openai.com/api/docs/pricing", SourceName: "OpenAI official reference", Scope: "standard_text", MaxInputTokens: 272000, AllPromptLengths: true}
	if _, err := f.s.UpdateModelPrices(f.ctx, []ModelPriceUpdate{{Model: referenced, ExpectedVersion: f.model.Version}}); err != nil {
		t.Fatal(err)
	}
	stored := f.findModel(t)
	if stored.Flex == nil || *stored.Flex != *referenced.Flex || stored.PricingReference == nil {
		t.Fatalf("price update stored %+v", stored.Flex)
	}

	// An edit that keeps the rates keeps the reference and its Flex price,
	// whatever Flex price the client sends.
	edit := stored
	edit.Name = "Renamed model"
	edit.Flex = &domain.FlexPrice{InputRate: 1, OutputRate: 1, CostInputRate: 1, CostOutputRate: 1}
	saved, err := f.s.SaveModel(f.ctx, edit)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Flex == nil || *saved.Flex != *referenced.Flex || saved.PricingReference == nil || saved.Name != "Renamed model" {
		t.Fatalf("edit kept %+v", saved.Flex)
	}

	// Customers see the Flex sales rates, never the upstream cost.
	customer := f.user
	customer.Operator = false
	view, err := f.s.Snapshot(f.ctx, customer)
	if err != nil {
		t.Fatal(err)
	}
	seen := false
	for _, m := range view.Models {
		if m.ID == f.model.ID {
			seen = true
			if fx := m.Flex; fx == nil || fx.InputRate != referenced.Flex.InputRate || fx.CostInputRate != 0 || fx.CostOutputRate != 0 || fx.CostCachedRate != 0 || fx.CostCacheWriteRate != 0 {
				t.Fatalf("customer view of the Flex price %+v", fx)
			}
		}
	}
	if !seen {
		t.Fatal("customer cannot see the model")
	}

	// Changed rates are manual rates: the reference and the Flex price go.
	changed := saved
	changed.InputRate++
	if saved, err = f.s.SaveModel(f.ctx, changed); err != nil {
		t.Fatal(err)
	}
	if saved.Flex != nil || saved.PricingReference != nil {
		t.Fatalf("changed rates kept the Flex price %+v", saved.Flex)
	}
	if stored = f.findModel(t); stored.Flex != nil {
		t.Fatalf("stored model kept the Flex price %+v", stored.Flex)
	}
}

func (f *fixture) findModel(t *testing.T) domain.Model {
	t.Helper()
	models, err := f.s.ListModels(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range models {
		if m.ID == f.model.ID {
			return m
		}
	}
	t.Fatal("model missing")
	return domain.Model{}
}
