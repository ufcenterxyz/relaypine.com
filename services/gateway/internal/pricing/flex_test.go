package pricing

import (
	"strings"
	"testing"
	"time"

	"relaypine.com/gateway/internal/domain"
)

// Flex prices come from the Flex table of OpenAI's Batch pricing data, with
// its long context columns, exactly as published: gpt-5.4's cached input is
// $0.13, not half of $0.25. The Batch table and the Fast table are ignored,
// and a model listed in neither the Flex table nor beside a standard price
// has no Flex price.
func TestFlexPricesFromTheFlexTable(t *testing.T) {
	rates, err := Parse("openai", fixture(t, "openai"), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id   string
		flex *FlexRate
	}{
		{"gpt-6.1-terra", &FlexRate{500_000, 2_500_000, 25_000, 625_000, &LongRate{1_000_000, 3_750_000, 50_000, 1_250_000}}},
		{"gpt-6-astra", &FlexRate{5_000_000, 25_000_000, 500_000, 6_250_000, &LongRate{10_000_000, 37_500_000, 1_000_000, 12_500_000}}},
		{"gpt-5.5", &FlexRate{2_500_000, 15_000_000, 250_000, 0, &LongRate{5_000_000, 22_500_000, 500_000, 0}}},
		// No Flex price is published for its prompts over 272K tokens.
		{"gpt-5.5-pro", &FlexRate{15_000_000, 90_000_000, 0, 0, nil}},
		{"gpt-5.4", &FlexRate{1_250_000, 7_500_000, 130_000, 0, &LongRate{2_500_000, 11_250_000, 250_000, 0}}},
		{"gpt-5.4-mini", &FlexRate{375_000, 2_250_000, 37_500, 0, nil}},
		{"o3", &FlexRate{1_000_000, 4_000_000, 250_000, 0, nil}},
	} {
		r, ok := rates[tc.id]
		if !ok || r.Flex == nil {
			t.Fatalf("%s has no Flex price", tc.id)
		}
		got, want := *r.Flex, *tc.flex
		if got.Input != want.Input || got.Output != want.Output || got.Cached != want.Cached || got.CacheWrite != want.CacheWrite || (got.Long == nil) != (want.Long == nil) || got.Long != nil && *got.Long != *want.Long {
			t.Fatalf("%s Flex price %+v %+v, want %+v %+v", tc.id, got, got.Long, want, want.Long)
		}
	}
	// gpt-4.1 has a Batch price only; gpt-4o and Claude models have none.
	for _, id := range []string{"gpt-4.1", "gpt-4o-mini", "gpt-4o", "gpt-5-pro"} {
		if r, ok := rates[id]; !ok || r.Flex != nil {
			t.Fatalf("%s: standard price %v, Flex price %+v", id, ok, r.Flex)
		}
	}
	// The standard prices are unchanged by the Flex table beside them.
	if r := rates["gpt-6.1-terra"]; r.Input != 1_000_000 || r.Output != 5_000_000 || r.Cached != 50_000 || r.CacheWrite != 1_250_000 || r.Long == nil || *r.Long != (LongRate{2_000_000, 7_500_000, 100_000, 2_500_000}) {
		t.Fatalf("gpt-6.1-terra standard price %+v %+v", r, r.Long)
	}

	m := ApplyRate(domain.Model{ID: "terra", Provider: "openai", UpstreamModel: "gpt-6.1-terra", Enabled: true, MaxOutputTokens: 1000}, rates["gpt-6.1-terra"])
	f := m.Flex
	if f == nil || f.InputRate != 500_000 || f.CostInputRate != 500_000 || f.CostOutputRate != 2_500_000 || f.CostCachedRate != 25_000 || f.CostCacheWriteRate != 625_000 ||
		f.LongContext == nil || f.LongContext.AboveInputTokens != 272_000 || f.LongContext.InputRate != 1_000_000 || f.LongContext.CostOutputRate != 3_750_000 {
		t.Fatalf("applied Flex price %+v %+v", f, f.LongContext)
	}
	if err := domain.CheckFlex(m); err != nil {
		t.Fatal(err)
	}
	pro := ApplyRate(domain.Model{ID: "pro", Provider: "openai", UpstreamModel: "gpt-5.5-pro", Enabled: true, MaxOutputTokens: 1000}, rates["gpt-5.5-pro"])
	if pro.Flex == nil || pro.Flex.LongContext != nil || !pro.FlexCovers(272_000) || pro.FlexCovers(272_001) || domain.CheckFlex(pro) != nil {
		t.Fatalf("gpt-5.5-pro Flex price %+v", pro.Flex)
	}
	// A rate without a Flex price clears one applied before.
	if m = ApplyRate(m, rates["gpt-4.1"]); m.Flex != nil {
		t.Fatal("a price without Flex kept the old Flex price")
	}
}

// A changed, ambiguous or inconsistent Flex table removes every Flex price
// and nothing else: the standard prices still import, and no model is
// priced at a guessed Flex rate.
func TestChangedFlexTablesFailClosed(t *testing.T) {
	flexTiered := "#### Flex\n\n| Model | Short context input |"
	for name, change := range map[string]func(string) string{
		"renamed heading": func(s string) string { return strings.Replace(s, "#### Flex\n", "#### Flexible\n", 1) },
		"changed header": func(s string) string {
			return strings.Replace(s, flexTiered, "#### Flex\n\n| Model | Flex input |", 1)
		},
		"duplicate row": func(s string) string {
			row := "| gpt-5.4-nano | $0.10 | $0.625 | $0.01 |\n"
			return strings.Replace(s, row, row+row, 1)
		},
		"above standard": func(s string) string {
			return strings.Replace(s, "| gpt-6-sol | $1.00 | $5.00 | $0.10 |", "| gpt-6-sol | $3.00 | $5.00 | $0.10 |", 1)
		},
		"long above standard long": func(s string) string {
			return strings.Replace(s, "| $0.625 | $1.25 | $1.00 | $3.75 | $0.05 |", "| $0.625 | $1.25 | $2.50 | $3.75 | $0.05 |", 1)
		},
		"unrecognized price": func(s string) string {
			return strings.Replace(s, "| gpt-5.4-mini | $0.375 |", "| gpt-5.4-mini | about $0.375 |", 1)
		},
		"long price without a standard one": func(s string) string {
			s = strings.Replace(s, "| gpt-5.4-mini | $0.375 | $2.25 | $0.0375 |\n", "", 1)
			return strings.Replace(s, "| gpt-6-luna | $0.05 |", "| gpt-5.4-mini | $0.375 | $2.25 | $0.0375 | - | - | $0.75 | $3.375 | $0.075 | - | - |\n| gpt-6-luna | $0.05 |", 1)
		},
		"no heading after the table": func(s string) string {
			s = strings.Replace(s, "### Fast pricing data", "Fast pricing data", 1)
			return s
		},
	} {
		changed := change(fixture(t, "openai"))
		if changed == fixture(t, "openai") {
			t.Fatalf("%s: the fixture did not change", name)
		}
		rates, err := Parse("openai", changed, time.Now())
		if err != nil {
			t.Fatalf("%s: standard prices lost: %v", name, err)
		}
		if r := rates["gpt-6.1-terra"]; r.Input != 1_000_000 || r.Long == nil {
			t.Fatalf("%s: standard price changed %+v", name, r)
		}
		for id, r := range rates {
			if r.Flex != nil {
				t.Fatalf("%s: %s kept a Flex price", name, id)
			}
		}
	}
}
