// Package pricing reads public, official standard text price tables. It never
// sends credentials to the source, infers prices from a model's name, or treats
// unavailable prices as free usage.
package pricing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"relaypine.com/gateway/internal/domain"
)

const AnthropicURL = "https://platform.claude.com/docs/en/about-claude/pricing"
const OpenAIURL = "https://developers.openai.com/api/docs/pricing"

type Rate struct {
	Input, Output, Cached, CacheWrite int64
	// Image rates are set for image generation models only; the text rates
	// then price text input and text output.
	Image                   bool
	ImageInput, ImageOutput int64
	Reference               domain.PriceReference
}
type Source struct {
	Provider  string    `json:"provider"`
	URL       string    `json:"url"`
	CheckedAt time.Time `json:"checked_at"`
	Error     string    `json:"error,omitempty"`
}
type Catalog struct {
	Rates   map[string]Rate
	Sources []Source
}

// Sources and redirects are fixed and allowlisted. Responses are bounded and
// parsed afresh for every preview; a changed or unavailable document fails closed.
func Fetch(ctx context.Context, providers []string) Catalog {
	client := &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	return FetchWithClient(ctx, providers, client)
}
func FetchWithClient(ctx context.Context, providers []string, client *http.Client) Catalog {
	result := Catalog{Rates: map[string]Rate{}, Sources: []Source{}}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, provider := range []string{"anthropic", "openai"} {
		if !contains(providers, provider) {
			continue
		}
		wg.Add(1)
		go func(provider string) {
			defer wg.Done()
			u := AnthropicURL
			if provider == "openai" {
				u = OpenAIURL
			}
			source := Source{Provider: provider, URL: u, CheckedAt: time.Now().UTC()}
			req, _ := http.NewRequestWithContext(ctx, "GET", u+".md", nil)
			req.Header.Set("Accept", "text/markdown")
			req.Header.Set("User-Agent", "RelayPine/1.0 (+https://github.com/ufcenterxyz/relaypine.com)")
			resp, err := client.Do(req)
			var rates map[string]Rate
			if err == nil {
				defer resp.Body.Close()
				if resp.StatusCode != 200 {
					err = errors.New("source unavailable")
				} else {
					var body []byte
					body, err = io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
					if err == nil && len(body) > 1<<20 {
						err = errors.New("source too large")
					}
					if err == nil {
						rates, err = Parse(provider, string(body), source.CheckedAt)
					}
				}
			}
			if err != nil {
				source.Error = "Official pricing could not be verified. Existing prices were not changed."
			}
			mu.Lock()
			defer mu.Unlock()
			result.Sources = append(result.Sources, source)
			if err == nil {
				for id, rate := range rates {
					result.Rates[provider+":"+id] = rate
				}
			}
		}(provider)
	}
	wg.Wait()
	return result
}

var dollars = regexp.MustCompile(`^\$([0-9]+(?:\.[0-9]{1,6})?)(?:\s*/\s*MTok)?(?:<sup>[0-9]+</sup>)?$`)
var claudeName = regexp.MustCompile(`^Claude (Fable|Mythos|Opus|Sonnet|Haiku) ([0-9]+(?:\.[0-9]+)?)$`)
var openaiName = regexp.MustCompile(`^(?:gpt-[a-z0-9.-]+|o[0-9]+(?:-[a-z0-9]+)*)$`)

func price(cell string, optional bool) (int64, error) {
	cell = strings.TrimSpace(cell)
	if optional && cell == "-" {
		return 0, nil
	}
	m := dollars.FindStringSubmatch(cell)
	if m == nil {
		return 0, errors.New("unrecognized price")
	}
	n, ok := new(big.Rat).SetString(m[1])
	if !ok {
		return 0, errors.New("invalid price")
	}
	n.Mul(n, big.NewRat(1_000_000, 1))
	if !n.IsInt() || !n.Num().IsInt64() {
		return 0, errors.New("price precision")
	}
	v := n.Num().Int64()
	if v < 0 || v > 1_000_000_000 || (!optional && v == 0) {
		return 0, errors.New("price out of range")
	}
	return v, nil
}
func cells(line string) []string {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "|") || !strings.HasSuffix(line, "|") {
		return nil
	}
	parts := strings.Split(strings.Trim(line, "|"), "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

// Parse accepts only documented standard tables. A malformed relevant row
// invalidates the provider result rather than publishing a partial guessed list.
func Parse(provider, document string, checked time.Time) (map[string]Rate, error) {
	start, end := "## Model pricing", "## Cloud platform pricing"
	u, name := AnthropicURL, "Anthropic official reference"
	if provider == "openai" {
		start, end = "### Standard pricing data", "### Batch pricing data"
		u, name = OpenAIURL, "OpenAI official reference"
	} else if provider != "anthropic" {
		return nil, errors.New("unknown provider")
	}
	a := strings.Index(document, start)
	if a < 0 {
		return nil, errors.New("missing standard pricing section")
	}
	section := document[a+len(start):]
	b := strings.Index(section, end)
	if b < 0 {
		return nil, errors.New("missing standard pricing boundary")
	}
	section = section[:b]
	sum := sha256.Sum256([]byte(section))
	hash := hex.EncodeToString(sum[:])
	out := map[string]Rate{}
	var header []string
	for _, line := range strings.Split(section, "\n") {
		c := cells(line)
		if len(c) == 0 {
			continue
		}
		if c[0] == "Model" {
			header = c
			continue
		}
		if strings.HasPrefix(c[0], "-") || strings.HasPrefix(c[0], ":") {
			continue
		}
		if len(header) == 0 || len(c) != len(header) {
			return nil, errors.New("changed price table")
		}
		id := c[0]
		shortClaudeContext := strings.Contains(id, " (for prompts up to 100,000 tokens)")
		indexes := []int{1, 2, 3, -1}
		limit := int64(272000)
		if provider == "anthropic" {
			if len(header) != 6 || header[1] != "Base input tokens" || header[2] != "5m cache writes" || header[3] != "1h cache writes" || header[4] != "Cache hits and refreshes" || header[5] != "Output tokens" {
				return nil, errors.New("changed Claude table headers")
			}
			// Retired and conditional premium rows are not new available tariffs.
			if strings.Contains(id, "retired") || strings.Contains(id, "over 100,000") {
				continue
			}
			if p := strings.Index(id, " ("); p >= 0 {
				annotation := id[p:]
				if annotation != " (for prompts up to 100,000 tokens)" && annotation != " ([limited availability](https://support.claude.com/en/articles/14604842))" {
					return nil, errors.New("unrecognized conditional Claude price")
				}
				id = id[:p]
			}
			match := claudeName.FindStringSubmatch(id)
			if match == nil {
				return nil, errors.New("unrecognized Claude model")
			}
			id = "claude-" + strings.ToLower(match[1]) + "-" + strings.ReplaceAll(match[2], ".", "-")
			indexes = []int{1, 5, 4, 2}
			limit = 200000
			if match[2] != "4.5" && match[2] != "3.5" {
				limit = 1000000
			}
			if shortClaudeContext {
				limit = 100000
			}
		} else {
			if p := strings.Index(id, " (<272K context length)"); p >= 0 {
				id = id[:p]
			}
			if !openaiName.MatchString(id) {
				return nil, errors.New("unrecognized OpenAI model")
			}
			if len(header) == 11 && header[1] == "Short context input" && header[2] == "Short context output" && header[3] == "Short context cached input" && header[4] == "Short context 30m write" {
				indexes[3] = 4
			} else if len(header) != 4 || header[1] != "Input" || header[2] != "Output" || header[3] != "Cached input" {
				return nil, errors.New("changed OpenAI table headers")
			}
		}
		var values [4]int64
		for i, index := range indexes {
			if index < 0 {
				continue
			}
			var err error
			values[i], err = price(c[index], i >= 2)
			if err != nil {
				return nil, fmt.Errorf("invalid %s price: %w", id, err)
			}
		}
		if _, exists := out[id]; exists {
			return nil, errors.New("ambiguous standard price")
		}
		out[id] = Rate{Input: values[0], Output: values[1], Cached: values[2], CacheWrite: values[3], Reference: domain.PriceReference{Provider: provider, Model: id, SourceURL: u, SourceName: name, CheckedAt: checked, Hash: hash, Scope: "standard_text", MaxInputTokens: limit}}
	}
	if len(out) < 3 {
		return nil, errors.New("incomplete standard prices")
	}
	if provider == "openai" {
		// A changed image section removes image prices only. Image models then
		// stay unmatched instead of falling back to a guessed rate.
		if images, err := parseImageRates(document, checked); err == nil {
			for id, rate := range images {
				if _, exists := out[id]; exists {
					return nil, errors.New("ambiguous standard price")
				}
				out[id] = rate
			}
		}
	}
	return out, nil
}

var imageModelName = regexp.MustCompile(`^(?:gpt-image-[a-z0-9.-]+|chatgpt-image-latest)$`)

// parseImageRates reads the Standard table of the "Image generation models"
// section: one Image and one Text row per model, with input, output and
// cached input prices per 1M tokens. Only supported image families are kept.
func parseImageRates(document string, checked time.Time) (map[string]Rate, error) {
	a := strings.Index(document, "Image generation models")
	if a < 0 {
		return nil, errors.New("missing image pricing section")
	}
	section := document[a:]
	table := strings.Index(section, "### Grouped Pricing Table data")
	standard := strings.Index(section, "Standard")
	batch := strings.Index(section, "Batch")
	if table < 0 || standard < 0 || standard > table || batch >= 0 && batch < table {
		return nil, errors.New("changed image pricing section")
	}
	section = section[table:]
	if batch = strings.Index(section, "Batch"); batch >= 0 {
		section = section[:batch]
	}
	sum := sha256.Sum256([]byte(section))
	hash := hex.EncodeToString(sum[:])
	type row struct{ input, output, cached int64 }
	rows := map[string]map[string]row{}
	header := false
	for _, line := range strings.Split(section, "\n") {
		c := cells(line)
		if len(c) == 0 {
			if header && len(rows) > 0 {
				break
			}
			continue
		}
		if c[0] == "Model" {
			if len(c) != 5 || c[1] != "Modality" || c[2] != "Input" || c[3] != "Output" || c[4] != "Cached input" {
				return nil, errors.New("changed image table headers")
			}
			header = true
			continue
		}
		if strings.HasPrefix(c[0], "-") || strings.HasPrefix(c[0], ":") {
			continue
		}
		if !header || len(c) != 5 || !imageModelName.MatchString(c[0]) || c[1] != "Image" && c[1] != "Text" {
			return nil, errors.New("changed image price table")
		}
		input, err := price(c[2], false)
		if err != nil {
			return nil, fmt.Errorf("invalid %s price: %w", c[0], err)
		}
		output, err := price(c[3], true)
		if err != nil {
			return nil, fmt.Errorf("invalid %s price: %w", c[0], err)
		}
		cached, err := price(c[4], true)
		if err != nil {
			return nil, fmt.Errorf("invalid %s price: %w", c[0], err)
		}
		if rows[c[0]] == nil {
			rows[c[0]] = map[string]row{}
		}
		if _, exists := rows[c[0]][c[1]]; exists {
			return nil, errors.New("ambiguous image price")
		}
		rows[c[0]][c[1]] = row{input, output, cached}
	}
	out := map[string]Rate{}
	for id, modalities := range rows {
		image, hasImage := modalities["Image"]
		text, hasText := modalities["Text"]
		if !hasImage || !hasText {
			return nil, errors.New("incomplete image price for " + id)
		}
		if domain.ImageFamily(id) == "" || image.output <= 0 {
			continue
		}
		out[id] = Rate{Input: text.input, Output: text.output, Cached: text.cached, Image: true, ImageInput: image.input, ImageOutput: image.output,
			Reference: domain.PriceReference{Provider: "openai", Model: id, SourceURL: OpenAIURL, SourceName: "OpenAI official reference", CheckedAt: checked, Hash: hash, Scope: "standard_image"}}
	}
	if len(out) == 0 {
		return nil, errors.New("no supported image prices")
	}
	return out, nil
}

// Only these documented dated aliases are equivalent to the standard model's
// rate. Never fuzzy-match a custom model or strip arbitrary dates/suffixes.
var aliases = map[string]string{
	"claude-haiku-4-5-20251001":  "claude-haiku-4-5",
	"claude-sonnet-4-5-20250929": "claude-sonnet-4-5",
	"claude-opus-4-5-20251101":   "claude-opus-4-5",
}

// CanonicalModel resolves only a documented dated alias to its standard model
// ID; every other ID is returned unchanged.
func CanonicalModel(provider, id string) string {
	if provider == "anthropic" {
		if canonical, ok := aliases[id]; ok {
			return canonical
		}
	}
	return id
}

func (c Catalog) Match(provider, id string) (Rate, bool) {
	if r, ok := c.Rates[provider+":"+id]; ok {
		return r, true
	}
	// A dated GPT Image snapshot shares its model's published price.
	if provider == "openai" {
		if canonical := domain.ImagePriceModel(id); canonical != id {
			r, found := c.Rates[provider+":"+canonical]
			return r, found
		}
	}
	if provider == "anthropic" {
		if canonical, ok := aliases[id]; ok {
			r, found := c.Rates[provider+":"+canonical]
			return r, found
		}
	}
	return Rate{}, false
}
func OfficialEndpoint(provider, base string) bool {
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Port() != "" && u.Port() != "443") {
		return false
	}
	host := "api.anthropic.com"
	if provider == "openai" {
		host = "api.openai.com"
	} else if provider != "anthropic" {
		return false
	}
	return strings.EqualFold(u.Hostname(), host) && (u.EscapedPath() == "" || u.EscapedPath() == "/" || u.EscapedPath() == "/v1" || u.EscapedPath() == "/v1/")
}
func contains(items []string, s string) bool {
	for _, v := range items {
		if v == s {
			return true
		}
	}
	return false
}

func ApplyRate(m domain.Model, rate Rate) domain.Model {
	m.BillingMode = domain.BillingMetered
	m.InputRate, m.OutputRate, m.CachedRate, m.CacheWriteRate = rate.Input, rate.Output, rate.Cached, rate.CacheWrite
	m.CostInputRate, m.CostOutputRate, m.CostCachedRate, m.CostCacheWriteRate = rate.Input, rate.Output, rate.Cached, rate.CacheWrite
	m.ImageInputRate, m.ImageOutputRate, m.CostImageInputRate, m.CostImageOutputRate = 0, 0, 0, 0
	if rate.Image {
		m.ImageInputRate, m.ImageOutputRate = rate.ImageInput, rate.ImageOutput
		m.CostImageInputRate, m.CostImageOutputRate = rate.ImageInput, rate.ImageOutput
	}
	m.Kind = domain.ModelKindFor(m.Provider, m.UpstreamModel)
	ref := rate.Reference
	m.PricingReference = &ref
	m.PricingNote = ""
	return m
}
