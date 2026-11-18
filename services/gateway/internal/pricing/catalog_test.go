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
	for _, pair := range [][2]string{{"anthropic", "claude-fable-5-1"}, {"anthropic", "claude-haiku-4-5-20251001"}, {"openai", "gpt-6.1-sol"}, {"openai", "gpt-image-2"}} {
		if _, ok := c.Match(pair[0], pair[1]); !ok {
			t.Fatal("live exact model missing", pair)
		}
	}
	if r, _ := c.Match("openai", "gpt-6.1-terra"); r.Flex == nil || r.Flex.Long == nil {
		t.Fatal("live Flex price missing for gpt-6.1-terra")
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

// OpenAI's pricing page renamed its image section "Images" in November 2026
// and added GPT Image 2.6 rows and a Batch table. The fixture keeps the live
// page's shape. The Standard table still prices the supported models, at the
// same rates as under the old heading.
func TestOpenAIImagePricesUnderTheImagesHeading(t *testing.T) {
	images, err := os.ReadFile("testdata/openai-images-2026-11.md")
	if err != nil {
		t.Fatal(err)
	}
	document := fixture(t, "openai") + "\n" + string(images)
	imagePrices := func(t *testing.T, document string) map[string]Rate {
		t.Helper()
		rates, err := Parse("openai", document, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		// The text and Flex prices beside the image section are unaffected.
		if r := rates["gpt-6.1-terra"]; r.Input != 1_000_000 || r.Flex == nil || r.Flex.Input != 500_000 {
			t.Fatalf("text price %+v", r)
		}
		out := map[string]Rate{}
		for id, r := range rates {
			if r.Image {
				out[id] = r
			}
		}
		return out
	}
	rates := imagePrices(t, document)
	if len(rates) != 3 {
		t.Fatalf("image prices for %d models: %v", len(rates), rates)
	}
	for _, id := range []string{"gpt-image-2", "gpt-image-2.5-sunburst", "gpt-image-2.5-flare"} {
		r := rates[id]
		// Standard, not Batch: $5 text input, $1.25 cached, $8 image input, $30 image output.
		if r.Input != 5_000_000 || r.Output != 0 || r.Cached != 1_250_000 || r.ImageInput != 8_000_000 || r.ImageOutput != 30_000_000 || r.Reference.Scope != "standard_image" || r.Reference.Model != id {
			t.Fatalf("%s rate %+v", id, r)
		}
	}
	// A Markdown heading names the section too.
	if rates := imagePrices(t, strings.Replace(document, "\nImages\n", "\n### Images\n", 1)); len(rates) != 3 {
		t.Fatalf("Markdown heading: %d image prices", len(rates))
	}
	// Both headings, or one twice, leave the section ambiguous: image models
	// stay unmatched instead of taking a guessed table.
	for name, changed := range map[string]string{
		"both headings":    strings.Replace(document, "\nImages\n", "\nImage generation models\n\nImages\n", 1),
		"repeated heading": strings.Replace(document, "\nImages\n", "\nImages\n\nImages\n", 1),
		"no heading":       strings.Replace(document, "\nImages\n", "\nImage pricing\n", 1),
	} {
		if rates := imagePrices(t, changed); len(rates) != 0 {
			t.Fatalf("%s: priced %d image models", name, len(rates))
		}
	}
}

// OpenAI's pricing page moved its Flex table out of the Batch pricing data
// section into a "Flex pricing data" section of its own in November 2026. The
// fixture is the live page of 2026-11-18. Both layouts are read; both at once,
// the section twice or a renamed section price no model at Flex, and the
// standard prices stay as they are.
func TestOpenAIFlexPricesUnderTheirOwnHeading(t *testing.T) {
	page, err := os.ReadFile("testdata/openai-2026-11.md")
	if err != nil {
		t.Fatal(err)
	}
	document := string(page)
	flexPrices := func(t *testing.T, document string) map[string]*FlexRate {
		t.Helper()
		rates, err := Parse("openai", document, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if r := rates["gpt-6.1-terra"]; r.Input != 1_000_000 || r.Output != 5_000_000 || r.Cached != 50_000 || r.CacheWrite != 1_250_000 {
			t.Fatalf("standard price %+v", r)
		}
		out := map[string]*FlexRate{}
		for id, r := range rates {
			if r.Flex != nil {
				out[id] = r.Flex
			}
		}
		return out
	}
	flex := flexPrices(t, document)
	f := flex["gpt-6.1-terra"]
	if f == nil || f.Input != 500_000 || f.Output != 2_500_000 || f.Cached != 25_000 || f.CacheWrite != 625_000 ||
		f.Long == nil || *f.Long != (LongRate{Input: 1_000_000, Output: 3_750_000, Cached: 50_000, CacheWrite: 1_250_000}) {
		t.Fatalf("gpt-6.1-terra Flex price %+v", f)
	}
	// The Flex table's own rows only: not the Batch rates of a model it does
	// not list, nor the Fast rates that follow it.
	if f := flex["gpt-6.2-sol"]; f != nil {
		t.Fatalf("gpt-6.2-sol priced at Flex %+v", f)
	}
	if f := flex["gpt-5.4-mini"]; f == nil || f.Input != 375_000 || f.Output != 2_250_000 || f.Cached != 37_500 {
		t.Fatalf("gpt-5.4-mini Flex price %+v", f)
	}
	// The earlier layout, a Flex table inside the Batch section, still reads.
	if f := flexPrices(t, fixture(t, "openai"))["gpt-6.1-terra"]; f == nil || f.Input != 500_000 {
		t.Fatalf("Batch section layout: Flex price %+v", f)
	}
	inBatch := "\n### Batch pricing data\n\n#### Flex\n\n| Model | Input | Output | Cached input |\n| --- | --- | --- | --- |\n| gpt-5 | $0.625 | $5.00 | $0.0625 |\n\n#### Batch\n"
	for name, changed := range map[string]string{
		"both layouts":     strings.Replace(document, "\n### Batch pricing data\n", inBatch, 1),
		"repeated section": strings.Replace(document, "\n### Flex pricing data\n", "\n### Flex pricing data\n\n### Flex pricing data\n", 1),
		"renamed section":  strings.Replace(document, "\n### Flex pricing data\n", "\n### Flex processing data\n", 1),
	} {
		if changed == document {
			t.Fatalf("%s: fixture unchanged", name)
		}
		if flex := flexPrices(t, changed); len(flex) != 0 {
			t.Fatalf("%s: priced %d models at Flex", name, len(flex))
		}
	}
}

// Long context prices come from OpenAI's long context columns and Claude's
// row for longer prompts. Models without them cover every prompt length.
func TestLongContextPrices(t *testing.T) {
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
		provider, id string
		above        int64
		long         *LongRate
	}{
		{"openai", "gpt-5.5", 272000, &LongRate{10_000_000, 45_000_000, 1_000_000, 0}},
		{"openai", "gpt-6.1-sol", 272000, &LongRate{4_000_000, 15_000_000, 200_000, 5_000_000}},
		{"openai", "gpt-5.5-pro", 272000, &LongRate{60_000_000, 270_000_000, 0, 0}},
		{"anthropic", "claude-haiku-5-5", 100000, &LongRate{500_000, 2_500_000, 50_000, 625_000}},
	} {
		r, ok := c.Match(tc.provider, tc.id)
		if !ok || r.Long == nil || *r.Long != *tc.long || r.Reference.MaxInputTokens != tc.above || r.Reference.AllPromptLengths {
			t.Fatalf("wrong long context price %s: %+v %+v", tc.id, r.Long, r.Reference)
		}
		m := ApplyRate(domain.Model{ID: "m", Provider: tc.provider, UpstreamModel: tc.id}, r)
		l := m.LongContext
		if l == nil || l.AboveInputTokens != tc.above || l.InputRate != tc.long.Input || l.CostOutputRate != tc.long.Output || l.CostCacheWriteRate != tc.long.CacheWrite || domain.CheckLongContext(m) != nil {
			t.Fatalf("applied long context price %s: %+v", tc.id, l)
		}
	}
	for _, tc := range []struct {
		provider, id string
		above        int64
	}{
		{"openai", "gpt-5.4-mini", 272000},
		{"openai", "gpt-4.1", 272000},
		{"anthropic", "claude-opus-5-5", 1000000},
		{"anthropic", "claude-sonnet-4-6", 1000000},
		{"anthropic", "claude-sonnet-4-5", 200000},
		{"anthropic", "claude-haiku-4-5-20251001", 200000},
	} {
		r, ok := c.Match(tc.provider, tc.id)
		if !ok || r.Long != nil || !r.Reference.AllPromptLengths || r.Reference.MaxInputTokens != tc.above || ApplyRate(domain.Model{}, r).LongContext != nil {
			t.Fatalf("standard price should cover every prompt length %s: %+v", tc.id, r.Reference)
		}
	}
}

func TestChangedLongContextPricesFailClosed(t *testing.T) {
	for _, tc := range []struct {
		provider string
		change   func(string) string
	}{
		{"openai", func(s string) string { return strings.Replace(s, "Long context: >272K", "Long context: >512K", 1) }},
		{"openai", func(s string) string {
			return strings.Replace(s, "| Long context input |", "| Extended context input |", 1)
		}},
		{"openai", func(s string) string {
			return strings.Replace(s, "| $10.00 | $45.00 | $1.00 |", "| $10.00 | $45.00 | - |", 1)
		}},
		{"openai", func(s string) string {
			return strings.Replace(s, "| $10.00 | $45.00 | $1.00 |", "| $4.00 | $45.00 | $1.00 |", 1)
		}},
		{"anthropic", func(s string) string {
			return strings.Replace(s, "(except Claude Haiku 5.5)", "(except Claude Haiku 5.5 and Claude Sonnet 5.5)", 1)
		}},
		{"anthropic", func(s string) string { return strings.Replace(s, "### Long context pricing", "### Context pricing", 1) }},
		{"anthropic", func(s string) string {
			return strings.Replace(s, "Claude Haiku 5.5 (for prompts over 100,000 tokens)", "Claude Haiku 5.5 (for prompts over 200,000 tokens)", 1)
		}},
		{"anthropic", func(s string) string {
			return strings.Replace(s, "Claude Haiku 5.5 (for prompts over 100,000 tokens)", "Claude Haiku 4.5 (for prompts over 100,000 tokens)", 1)
		}},
		{"anthropic", func(s string) string {
			return strings.Replace(s, "Claude Haiku 5.5 (for prompts up to 100,000 tokens)", "Claude Haiku 5.5 (for prompts up to 100 thousand tokens)", 1)
		}},
	} {
		if rates, err := Parse(tc.provider, tc.change(fixture(t, tc.provider)), time.Now()); err == nil {
			t.Fatalf("accepted a changed long context price: %v", len(rates))
		}
	}
	// A tiered row without long context rates keeps its threshold, so the
	// gateway refuses longer prompts instead of pricing them as standard.
	doc := strings.Replace(fixture(t, "openai"), "| $10.00 | $45.00 | $1.00 | - | - |", "| - | - | - | - | - |", 1)
	rates, err := Parse("openai", doc, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if r := rates["gpt-5.5"]; r.Long != nil || r.Reference.AllPromptLengths || r.Reference.MaxInputTokens != 272000 {
		t.Fatalf("tiered model without long rates: %+v", r)
	}
	// The same holds for a Claude row priced up to a length with no row above it.
	doc = strings.Replace(fixture(t, "anthropic"), "Claude Haiku 5.5 (for prompts over 100,000 tokens)", "Claude Haiku 5.5 (retired)", 1)
	if rates, err = Parse("anthropic", doc, time.Now()); err != nil {
		t.Fatal(err)
	}
	if r := rates["claude-haiku-5-5"]; r.Long != nil || r.Reference.AllPromptLengths || r.Reference.MaxInputTokens != 100000 {
		t.Fatalf("Claude row without a longer price: %+v", r)
	}
}
