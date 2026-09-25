package main

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/peterw22/pi-go/internal/agent"
)

func TestGPT6SolAndLunaCatalogAndRequests(t *testing.T) {
	t.Setenv("PI_GO_AGY_HOME", t.TempDir())
	auth := newCodexAuthManagerAt(filepath.Join(t.TempDir(), "auth.json"), defaultCodexAuthEndpoints, http.DefaultClient)
	models, err := configuredModels(auth)
	if err != nil {
		t.Fatal(err)
	}
	catalog := make(map[string]modelInfo)
	for _, model := range models {
		catalog[model.ID] = model
		if model.ID == "gpt-5.5" || model.ID == "gpt-5.5-fast" || strings.HasPrefix(model.ID, "gpt-5.3-codex-spark") {
			t.Fatalf("retired model advertised: %s", model.ID)
		}
	}
	for _, id := range []string{"gpt-6-sol", "gpt-6-luna"} {
		for _, suffix := range []string{"", "-fast"} {
			t.Run(id+suffix, func(t *testing.T) {
				entry, ok := catalog[id+suffix]
				if !ok || entry.Provider != codexProviderID {
					t.Fatalf("missing Codex model: %s", id+suffix)
				}
				model, tier := resolveCodexFastVariant(id + suffix)
				wantTier := ""
				if suffix != "" {
					wantTier = "priority"
				}
				if model != id || tier != wantTier {
					t.Fatalf("model=%q tier=%q", model, tier)
				}
				body, err := buildCodexRequest(agent.Request{Model: model, ServiceTier: tier, Thinking: "high"})
				if err != nil {
					t.Fatal(err)
				}
				if body.Model != id || body.ServiceTier != wantTier {
					t.Fatalf("body=%#v", body)
				}
			})
		}
	}
}
