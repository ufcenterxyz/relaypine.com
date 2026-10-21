// pricing previews and applies reviewed reference-price backfills using the
// gateway's normal environment. It never changes wallets or historical usage.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"relaypine.com/gateway/internal/domain"
	"relaypine.com/gateway/internal/pricing"
	"relaypine.com/gateway/internal/store"
)

func main() {
	previewPath := flag.String("out", "", "Write a price preview, including original model configurations, to this private file")
	applyPath := flag.String("apply", "", "Apply a previously reviewed, unexpired preview file")
	scope := flag.String("scope", "usage-only", "Select usage-only legacy models or all models: usage-only|all")
	ids := flag.String("ids", "", "Optional comma-separated model IDs within the selected scope")
	flag.Parse()
	if (*previewPath == "") == (*applyPath == "") || (*scope != "usage-only" && *scope != "all") {
		fatal("Choose exactly one of -out or -apply, and a valid -scope.")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	db, err := store.Open(ctx, os.Getenv("MYSQL_DSN"), os.Getenv("MASTER_KEY"), store.Config{SuperAdminEmail: os.Getenv("SUPERADMIN_EMAIL")})
	if err != nil {
		fatal("Could not open the configured database; check the gateway environment.")
	}
	defer db.DB.Close()
	selected := func(m domain.Model) bool {
		if *scope == "usage-only" && m.EffectiveBillingMode() != domain.BillingUsageOnly {
			return false
		}
		if *ids == "" {
			return true
		}
		for _, id := range strings.Split(*ids, ",") {
			if id == m.ID {
				return true
			}
		}
		return false
	}
	if *previewPath != "" {
		models, err := db.ListModels(ctx)
		if err != nil {
			fatal("Could not read models.")
		}
		endpoints, err := db.ListEndpoints(ctx)
		if err != nil {
			fatal("Could not read upstream definitions.")
		}
		chosen := []domain.Model{}
		providers := []string{}
		for _, m := range models {
			if selected(m) {
				chosen = append(chosen, m)
				providers = append(providers, m.Provider)
			}
		}
		preview := pricing.Build(pricing.Fetch(ctx, providers), chosen, endpoints, "update")
		if len(preview.Sources) == 0 && len(chosen) > 0 {
			fatal("No verifiable source.")
		}
		// Never convert a transient source/network failure into a destructive
		// migration. Unknown exact IDs may be disabled only after sources verify.
		for _, source := range preview.Sources {
			if source.Error != "" {
				fatal(source.Provider + ": " + source.Error)
			}
		}
		raw, _ := json.MarshalIndent(preview, "", "  ")
		f, err := os.OpenFile(*previewPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			fatal("Preview output must be a new writable file.")
		}
		if _, err = f.Write(raw); err != nil {
			f.Close()
			fatal("Could not write preview.")
		}
		if err = f.Close(); err != nil {
			fatal("Could not close preview.")
		}
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"preview_file": *previewPath, "model_count": len(chosen), "expires_at": preview.ExpiresAt, "sources": preview.Sources, "items": preview.Items})
		return
	}
	file, err := os.Open(*applyPath)
	if err != nil {
		fatal("Could not open preview file.")
	}
	raw, err := io.ReadAll(io.LimitReader(file, (4<<20)+1))
	file.Close()
	if err != nil || len(raw) > 4<<20 {
		fatal("Could not read bounded preview file.")
	}
	var preview pricing.Preview
	if json.Unmarshal(raw, &preview) != nil || preview.Kind != "update" || !preview.ExpiresAt.After(time.Now()) || len(preview.Items) > store.MaxModelImportItems {
		fatal("Invalid or expired preview; generate a fresh preview.")
	}
	for _, source := range preview.Sources {
		if source.Error != "" {
			fatal("Cannot apply a preview with unverified sources.")
		}
	}
	edits := []store.ModelPriceUpdate{}
	for _, item := range preview.Items {
		if selected(item.Before) {
			edits = append(edits, store.ModelPriceUpdate{Model: item.After, ExpectedVersion: item.Version})
		}
	}
	if len(edits) == 0 {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"updated_count": 0})
		return
	}
	result, err := db.UpdateModelPrices(ctx, edits)
	if err != nil {
		fatal("Price update could not be confirmed or a model version changed. Read current models before retrying.")
	}
	_ = json.NewEncoder(os.Stdout).Encode(result)
}
func fatal(message string) { fmt.Fprintln(os.Stderr, message); os.Exit(1) }
