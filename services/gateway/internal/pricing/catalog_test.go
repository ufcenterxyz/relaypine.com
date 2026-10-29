package pricing

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"relaypine.com/gateway/internal/domain"
)

func fixture(t *testing.T, provider string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + provider + ".md")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
func TestOfficialStandardTablesAndExactMatches(t *testing.T) {
	c := Catalog{Rates: map[string]Rate{}}
	for _, provider := range []string{"anthropic", "openai"} {
		rates, err := Parse(provider, fixture(t, provider), time.Now())
		if err != nil {
			t.Fatal(err)
		}
		for id, r := range rates {
			c.Rates[provider+":"+id] = r
		}
	}
	for _, tc := range []struct {
		provider, id          string
		in, out, cache, write int64
	}{
		{"anthropic", "claude-opus-5-5", 4_000_000, 20_000_000, 200_000, 5_000_000},
		{"anthropic", "claude-fable-5-1", 10_000_000, 50_000_000, 250_000, 12_500_000},
		{"anthropic", "claude-haiku-4-5-20251001", 1_000_000, 5_000_000, 100_000, 1_250_000},
		{"anthropic", "claude-haiku-5-5", 100_000, 500_000, 10_000, 125_000},
		{"openai", "gpt-6.1-sol", 2_000_000, 10_000_000, 100_000, 2_500_000},
		{"openai", "gpt-4o-2024-05-13", 5_000_000, 15_000_000, 0, 0},
	} {
		r, ok := c.Match(tc.provider, tc.id)
		if !ok || r.Input != tc.in || r.Output != tc.out || r.Cached != tc.cache || r.CacheWrite != tc.write {
			t.Fatalf("wrong standard price %s: %+v", tc.id, r)
		}
	}
	for _, id := range []string{"claude-opus-5-5-special", "claude-haiku-4-5-29990101", "claude-fable-6", "gpt-4o-2025-01-01"} {
		if _, ok := c.Match("anthropic", id); ok {
			t.Fatal("guessed unknown model", id)
		}
	}
}
func TestChangedTablesFailClosed(t *testing.T) {
	for _, change := range []func(string) string{
		func(s string) string { return strings.Replace(s, "Base input tokens", "Discounted input", 1) },
		func(s string) string { return strings.Replace(s, "$10 / MTok", "contact us", 1) },
		func(s string) string {
			return strings.Replace(s, "Claude Fable 5.1", "Claude Fable 5.1 (for discounted accounts)", 1)
		},
		func(s string) string { return strings.Replace(s, "$0.10 / MTok", "$0.0000001 / MTok", 1) },
	} {
		if _, err := Parse("anthropic", change(fixture(t, "anthropic")), time.Now()); err == nil {
			t.Fatal("accepted changed/ambiguous table")
		}
	}
}
func TestOfficialEndpointRequiresCanonicalOriginAndPath(t *testing.T) {
	for _, u := range []string{"https://api.anthropic.com", "https://API.ANTHROPIC.COM/v1/", "https://api.anthropic.com:443"} {
		if !OfficialEndpoint("anthropic", u) {
			t.Fatal(u)
		}
	}
	for _, u := range []string{"http://api.anthropic.com", "https://api.anthropic.com.evil.test", "https://evil.test/api.anthropic.com", "https://api.anthropic.com/private", "https://api.anthropic.com:8443", "https://user@api.anthropic.com", "https://api.anthropic.com?proxy=1", "https://api.anthropic.com/%76%31"} {
		if OfficialEndpoint("anthropic", u) {
			t.Fatal(u)
		}
	}
}
func TestPreviewPreservesDisabledModelsAndExplicitManualPrices(t *testing.T) {
	rates, err := Parse("anthropic", fixture(t, "anthropic"), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	c := Catalog{Rates: map[string]Rate{}}
	for id, rate := range rates {
		c.Rates["anthropic:"+id] = rate
	}
	e := []domain.Endpoint{{Provider: "anthropic", BaseURL: "https://api.anthropic.com", Enabled: true}}
	base := domain.Model{ID: "model", Provider: "anthropic", UpstreamModel: "claude-opus-5-5", BillingMode: domain.BillingUsageOnly, Enabled: true, Version: 2}
	unknown := base
	unknown.ID = "unknown"
	unknown.UpstreamModel = "private-model"
	disabled := base
	disabled.ID = "disabled"
	disabled.Enabled = false
	manual := ApplyRate(base, rates["claude-opus-5-5"])
	manual.ID = "manual"
	manual.PricingReference = nil
	manual.InputRate = 10_000_000
	p := Build(c, []domain.Model{base, unknown, disabled, manual}, e, "update")
	if p.Items[0].After.BillingMode != domain.BillingMetered || !p.Items[0].After.Enabled || p.Items[0].After.InputRate != 4_000_000 {
		t.Fatal("legacy model not metered")
	}
	if p.Items[1].Status != "unmatched" || p.Items[1].After.Enabled || p.Items[1].After.PricingNote == "" {
		t.Fatal("unknown model became free")
	}
	if p.Items[2].After.Enabled {
		t.Fatal("disabled model was enabled")
	}
	if p.Items[3].Status != "manual" || p.Items[3].Before.InputRate != 10_000_000 {
		t.Fatal("manual prices not distinguished")
	}
	e[0].BaseURL = "https://compatible.test"
	if Build(c, []domain.Model{base}, e, "import").Items[0].After.Enabled {
		t.Fatal("official rates incorrectly assigned to custom host")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestFetchDoesNotLeakCredentialsAndFailsClosed(t *testing.T) {
	c := FetchWithClient(context.Background(), []string{"anthropic"}, &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != AnthropicURL+".md" || r.Header.Get("Authorization") != "" || r.Header.Get("X-Api-Key") != "" {
			t.Error("unexpected request")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("html error")), Header: http.Header{}}, nil
	})})
	if len(c.Rates) != 0 || len(c.Sources) != 1 || c.Sources[0].Error == "" {
		t.Fatal("invalid source became a catalog")
	}
}
func TestLivePricingCatalog(t *testing.T) {
	if os.Getenv("PRICING_LIVE_TEST") != "1" {
		t.Skip("explicit opt-in live public source check")
	}
	c := Fetch(context.Background(), []string{"anthropic", "openai"})
	for _, s := range c.Sources {
		if s.Error != "" {
			t.Fatal(s.Provider, s.Error)
		}
	}
	for _, pair := range [][2]string{{"anthropic", "claude-fable-5-1"}, {"anthropic", "claude-haiku-4-5-20251001"}, {"openai", "gpt-6.1-sol"}} {
		if _, ok := c.Match(pair[0], pair[1]); !ok {
			t.Fatal("live exact model missing", pair)
		}
	}
	t.Logf("Verified %d public reference prices", len(c.Rates))
}

// The Standard image table yields text and image rates for supported GPT Image
// models only; the Batch table and retiring models are ignored, and a dated
// snapshot matches its model's price.
func TestOpenAIImagePrices(t *testing.T) {
	images, err := os.ReadFile("testdata/openai-images.md")
	if err != nil {
		t.Fatal(err)
	}
	document := fixture(t, "openai") + "\n" + string(images)
	rates, err := Parse("openai", document, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"gpt-image-2", "gpt-image-2.5-sunburst", "gpt-image-2.5-flare"} {
		r := rates[id]
		if !r.Image || r.Input != 5_000_000 || r.Output != 0 || r.Cached != 1_250_000 || r.ImageInput != 8_000_000 || r.ImageOutput != 30_000_000 || r.Reference.Scope != "standard_image" {
			t.Fatalf("%s rate %+v", id, r)
		}
	}
	for _, id := range []string{"gpt-image-1.5", "gpt-image-1-mini", "chatgpt-image-latest"} {
		if _, ok := rates[id]; ok {
			t.Fatalf("retiring model %s priced", id)
		}
	}
	c := Catalog{Rates: map[string]Rate{}}
	for id, r := range rates {
		c.Rates["openai:"+id] = r
	}
	rate, ok := c.Match("openai", "gpt-image-2-2026-04-21")
	if !ok || rate.Reference.Model != "gpt-image-2" {
		t.Fatalf("snapshot match %v %+v", ok, rate.Reference)
	}
	if _, ok := c.Match("openai", "gpt-image-2-preview"); ok {
		t.Fatal("an undated variant matched")
	}
	m := ApplyRate(domain.Model{ID: "image", Provider: "openai", UpstreamModel: "gpt-image-2-2026-04-21"}, rate)
	if !m.IsImage() || m.ImageOutputRate != 30_000_000 || m.CostImageInputRate != 8_000_000 || !HasRates(m) {
		t.Fatalf("applied image rate %+v", m)
	}
	// A changed image table removes image prices, never text prices.
	changed := strings.Replace(document, "| Model | Modality | Input | Output | Cached input |", "| Model | Modality | Input | Output |", 1)
	rates, err = Parse("openai", changed, time.Now())
	if err != nil || len(rates) < 3 {
		t.Fatalf("text prices lost: %v", err)
	}
	if _, ok := rates["gpt-image-2"]; ok {
		t.Fatal("changed image table still priced")
	}
}
