package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"relaypine.com/gateway/internal/domain"
)

func TestValidateImageModels(t *testing.T) {
	image := domain.Model{ID: "image", Name: "GPT Image 2", Provider: "openai", UpstreamModel: "gpt-image-2", Enabled: true, BillingMode: domain.BillingMetered, Kind: domain.ModelKindImage, MaxOutputTokens: 1,
		InputRate: 5_000_000, ImageInputRate: 8_000_000, ImageOutputRate: 30_000_000, CostInputRate: 5_000_000, CostImageInputRate: 8_000_000, CostImageOutputRate: 30_000_000}
	if err := validateModel(image); err != nil {
		t.Fatalf("image model without a text output price rejected: %v", err)
	}
	missing := image
	missing.CostImageOutputRate = 0
	if validateModel(missing) == nil {
		t.Fatal("enabled image model without an upstream image output cost accepted")
	}
	text := image
	text.UpstreamModel, text.Kind = "gpt-5.5", ""
	text.OutputRate, text.CostOutputRate = 1, 1
	if validateModel(text) == nil {
		t.Fatal("text model with image rates accepted")
	}
	mislabeled := image
	mislabeled.UpstreamModel = "gpt-5.5"
	if validateModel(mislabeled) == nil {
		t.Fatal("text upstream published as an image model")
	}
}

// A GPT Image model stored before image support has no kind. It is read as an
// image model, and reference image prices can still be applied to it.
func TestStoredImageModelWithoutKindTakesImagePrices(t *testing.T) {
	f := integrationFixture(t)
	id := "legacy-image-" + strings.ReplaceAll(t.Name(), "/", "-")
	legacy := `{"id":"` + id + `","name":"GPT Image 2","provider":"openai","upstream_model":"gpt-image-2-2026-04-21","enabled":false,"billing_mode":"metered","max_output_tokens":4096,"version":1}`
	if _, err := f.s.DB.ExecContext(f.ctx, "INSERT INTO models(id,config,version) VALUES(?,?,1)", id, legacy); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = f.s.DB.ExecContext(context.Background(), "DELETE FROM models WHERE id=?", id) })
	models, err := f.s.ListModels(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	var stored domain.Model
	for _, m := range models {
		if m.ID == id {
			stored = m
		}
	}
	if !stored.IsImage() {
		t.Fatalf("legacy image model read with kind %q", stored.Kind)
	}
	priced := stored
	priced.Kind = ""
	priced.InputRate, priced.CachedRate, priced.ImageInputRate, priced.ImageOutputRate = 5_000_000, 1_250_000, 8_000_000, 30_000_000
	priced.CostInputRate, priced.CostCachedRate, priced.CostImageInputRate, priced.CostImageOutputRate = 5_000_000, 1_250_000, 8_000_000, 30_000_000
	if _, err := f.s.UpdateModelPrices(f.ctx, []ModelPriceUpdate{{Model: priced, ExpectedVersion: 1}}); err != nil {
		t.Fatalf("image prices not applied to a legacy model: %v", err)
	}
	var raw []byte
	if err := f.s.DB.QueryRowContext(f.ctx, "SELECT config FROM models WHERE id=?", id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var saved domain.Model
	_ = json.Unmarshal(raw, &saved)
	if !saved.IsImage() || saved.ImageOutputRate != 30_000_000 || saved.CostImageInputRate != 8_000_000 {
		t.Fatalf("saved %+v", saved)
	}
}
