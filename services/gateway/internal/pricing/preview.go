package pricing

import (
	"relaypine.com/gateway/internal/domain"
	"time"
)

type Item struct {
	ID             string       `json:"id"`
	Name           string       `json:"name"`
	Version        int          `json:"version"`
	Status         string       `json:"status"`
	Reason         string       `json:"reason,omitempty"`
	Before         domain.Model `json:"before"`
	After          domain.Model `json:"after"`
	SourceURL      string       `json:"source_url,omitempty"`
	SourceName     string       `json:"source_name,omitempty"`
	ReferenceModel string       `json:"reference_model,omitempty"`
}
type Preview struct {
	ID        string    `json:"preview_id"`
	Kind      string    `json:"kind"`
	ExpiresAt time.Time `json:"expires_at"`
	CheckedAt time.Time `json:"checked_at"`
	Items     []Item    `json:"items"`
	Sources   []Source  `json:"sources"`
}

func SameRates(a, b domain.Model) bool {
	return a.EffectiveBillingMode() == b.EffectiveBillingMode() && a.InputRate == b.InputRate && a.OutputRate == b.OutputRate && a.CachedRate == b.CachedRate && a.CacheWriteRate == b.CacheWriteRate && a.CostInputRate == b.CostInputRate && a.CostOutputRate == b.CostOutputRate && a.CostCachedRate == b.CostCachedRate && a.CostCacheWriteRate == b.CostCacheWriteRate &&
		a.ImageInputRate == b.ImageInputRate && a.ImageOutputRate == b.ImageOutputRate && a.CostImageInputRate == b.CostImageInputRate && a.CostImageOutputRate == b.CostImageOutputRate
}

// HasRates reports a complete metered price. Image models publish no text
// output price, so they need text input and image input/output rates instead.
func HasRates(m domain.Model) bool {
	if m.EffectiveBillingMode() != domain.BillingMetered {
		return false
	}
	if m.IsImage() {
		return m.InputRate > 0 && m.ImageInputRate > 0 && m.ImageOutputRate > 0 && m.CostInputRate > 0 && m.CostImageInputRate > 0 && m.CostImageOutputRate > 0
	}
	return m.InputRate > 0 && m.OutputRate > 0 && m.CostInputRate > 0 && m.CostOutputRate > 0
}

// Build keeps disabled entries disabled, and never changes a manually priced
// model when no trusted reference exists. Callers explicitly select every edit.
func Build(catalog Catalog, models []domain.Model, endpoints []domain.Endpoint, kind string) Preview {
	now := time.Now().UTC()
	p := Preview{Kind: kind, CheckedAt: now, ExpiresAt: now.Add(15 * time.Minute), Sources: catalog.Sources, Items: []Item{}}
	for _, m := range models {
		m.Kind = domain.ModelKindFor(m.Provider, m.UpstreamModel)
		item := Item{ID: m.ID, Name: m.Name, Version: m.Version, Before: m, After: m, Status: "unmatched"}
		var eligible bool
		for _, e := range endpoints {
			if e.Enabled && e.Provider == m.Provider && OfficialEndpoint(e.Provider, e.BaseURL) && (len(e.Models) == 0 || contains(e.Models, m.ID) || contains(e.Models, m.UpstreamModel)) {
				eligible = true
				break
			}
		}
		rate, found := catalog.Match(m.Provider, m.UpstreamModel)
		switch {
		case !eligible:
			item.Reason = "Official reference prices require an enabled official upstream. Set manual sales and upstream cost rates for compatible third-party hosts."
		case !found:
			item.Reason = "No verified standard price was found for this exact upstream model. Set manual rates before enabling it."
			for _, source := range catalog.Sources {
				if source.Provider == m.Provider && source.Error != "" {
					item.Reason = source.Error
					break
				}
			}
		default:
			item.After = ApplyRate(m, rate)
			item.Status = "matched"
			if m.PricingReference == nil && HasRates(m) && !SameRates(m, item.After) {
				item.Status = "manual"
				item.Reason = "Applying this reference replaces your manual sales and cost rates with the official standard rates."
			}
			item.SourceURL, item.SourceName, item.ReferenceModel = rate.Reference.SourceURL, rate.Reference.SourceName, rate.Reference.Model
		}
		if item.Status == "unmatched" && !HasRates(m) {
			item.After.Enabled = false
			item.After.BillingMode = domain.BillingMetered
			item.After.PricingReference = nil
			item.After.PricingNote = item.Reason
		}
		p.Items = append(p.Items, item)
	}
	return p
}
