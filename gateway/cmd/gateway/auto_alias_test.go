package gateway

import (
	"testing"
	"time"

	"github.com/sference/sference-switch/gateway/internal/pricing"
)

// snapshotWithSferenceModels builds a pricing snapshot carrying the given
// Sference slugs, the same way loadProviderCatalogCaches populates one from
// the on-disk catalog cache at startup.
func snapshotWithSferenceModels(t *testing.T, slugs ...string) *pricing.Snapshot {
	t.Helper()
	p := pricing.New()
	availability := make([]pricing.AvailabilityModel, 0, len(slugs))
	for _, slug := range slugs {
		availability = append(availability, pricing.AvailabilityModel{
			CanonicalModelID: slug,
			DisplayName:      slug,
		})
	}
	if err := p.ReplaceProviderAvailability(
		pricing.ProviderSference,
		availability,
		sferenceModelAPIsAvailabilitySource,
		time.Now().UTC(),
		"test",
	); err != nil {
		t.Fatalf("seed availability: %v", err)
	}
	return p.Capture()
}

func TestAutoAliasIDNormalizesSlugs(t *testing.T) {
	cases := []struct {
		slug string
		want string
	}{
		{"zai-org/GLM-5.3-Flash", "claude-sference-zai-org-glm-5-3-flash"},
		{"Qwen/Qwen3.8-Flash-Next", "claude-sference-qwen-qwen3-8-flash-next"},
		{"moonshotai/Kimi-K3", "claude-sference-moonshotai-kimi-k3"},
	}
	for _, tc := range cases {
		got, ok := autoAliasID(tc.slug)
		if !ok {
			t.Errorf("autoAliasID(%q) returned ok=false", tc.slug)
			continue
		}
		if got != tc.want {
			t.Errorf("autoAliasID(%q) = %q, want %q", tc.slug, got, tc.want)
		}
	}
}

// A derived id must satisfy the same predicates as a hand-written one, or the
// picker would drop it and config would have rejected it.
func TestAutoAliasIDSatisfiesConfigValidators(t *testing.T) {
	slugs := []string{
		"zai-org/GLM-5.3-Flash",
		"Qwen/Qwen3.8-Flash-Next",
		"deepseek-ai/DeepSeek-V4-Flash",
		"bottlecapai/ThinkingCap-Qwen3.6-27B",
	}
	for _, slug := range slugs {
		id, ok := autoAliasID(slug)
		if !ok {
			t.Errorf("autoAliasID(%q) returned ok=false", slug)
			continue
		}
		if !discoveryPrefixOK(id) {
			t.Errorf("derived id %q fails Claude Code's discovery filter", id)
		}
		if reservedAnthropicModelRe.MatchString(id) {
			t.Errorf("derived id %q collides with a real Anthropic model name", id)
		}
		if !InAliasNamespace(id) {
			t.Errorf("derived id %q is outside the gateway alias namespace", id)
		}
	}
}

func TestAutoAliasIDRejectsUnusableSlugs(t *testing.T) {
	for _, slug := range []string{"", "   ", "///", "---"} {
		if id, ok := autoAliasID(slug); ok {
			t.Errorf("autoAliasID(%q) = %q, want ok=false", slug, id)
		}
	}
}

// The regression this feature exists for: models present in the catalog but
// absent from gateway.yaml were invisible in /model.
func TestDeriveAutoAliasesCoversCatalogModelsAbsentFromConfig(t *testing.T) {
	snapshot := snapshotWithSferenceModels(t,
		"zai-org/GLM-5.2",
		"zai-org/GLM-5.3-Flash",
		"Qwen/Qwen3.8-Flash-Next",
	)
	derived := deriveAutoAliases(snapshot)
	bySlug := map[string]bool{}
	for _, slug := range derived {
		bySlug[slug] = true
	}
	for _, slug := range []string{"zai-org/GLM-5.3-Flash", "Qwen/Qwen3.8-Flash-Next"} {
		if !bySlug[slug] {
			t.Errorf("catalog model %q has no derived alias", slug)
		}
	}
}

func TestDeriveAutoAliasesSkipsExcludedSlugs(t *testing.T) {
	snapshot := snapshotWithSferenceModels(t,
		"zai-org/GLM-5.2",
		"Shovels/ettin-permit-tagger",
		"Qwen/Qwen3-VL-30B-A3B-Instruct",
		"deepseek-ai/DeepSeek-V4-Flash-0731",
	)
	for id, slug := range deriveAutoAliases(snapshot) {
		if autoAliasExcludedSlugs[slug] {
			t.Errorf("excluded slug %q was published as %q", slug, id)
		}
	}
}

func TestDeriveAutoAliasesNilSnapshot(t *testing.T) {
	if got := deriveAutoAliases(nil); got != nil {
		t.Errorf("deriveAutoAliases(nil) = %v, want nil", got)
	}
}

// pricing.New() embeds a vendored Sference fallback catalog, so even a
// gateway that has never reached the network publishes a usable picker
// rather than an empty one.
func TestDeriveAutoAliasesUsesEmbeddedFallbackCatalog(t *testing.T) {
	derived := deriveAutoAliases(pricing.New().Capture())
	if len(derived) == 0 {
		t.Fatal("embedded fallback catalog produced no aliases")
	}
	for id, slug := range derived {
		if !InAliasNamespace(id) {
			t.Errorf("fallback alias %q is outside the alias namespace", id)
		}
		if slug == "" {
			t.Errorf("fallback alias %q has an empty slug", id)
		}
	}
}

// Existing installs must keep the exact ids they pinned.
func TestEffectiveModelAliasesConfiguredWins(t *testing.T) {
	snapshot := snapshotWithSferenceModels(t, "zai-org/GLM-5.2", "zai-org/GLM-5.3-Flash")
	configured := map[string]string{"claude-sference-glm-5-2": "zai-org/GLM-5.2"}

	merged := effectiveModelAliases(snapshot, configured)

	if merged["claude-sference-glm-5-2"] != "zai-org/GLM-5.2" {
		t.Errorf("configured alias lost: %v", merged)
	}
	// The configured slug must not also appear under its derived id, or the
	// picker would list one model twice.
	count := 0
	for _, slug := range merged {
		if slug == "zai-org/GLM-5.2" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("slug zai-org/GLM-5.2 published %d times, want 1: %v", count, merged)
	}
	// The unconfigured catalog model is still picked up.
	found := false
	for _, slug := range merged {
		if slug == "zai-org/GLM-5.3-Flash" {
			found = true
		}
	}
	if !found {
		t.Errorf("derived alias for GLM-5.3-Flash missing: %v", merged)
	}
}

// A nil snapshot is the one case with nothing to derive; configured aliases
// must then pass through byte-identically to pre-feature behaviour.
func TestEffectiveModelAliasesFallsBackToConfigured(t *testing.T) {
	configured := map[string]string{"claude-sference-glm-5-2": "zai-org/GLM-5.2"}
	merged := effectiveModelAliases(nil, configured)
	if len(merged) != 1 || merged["claude-sference-glm-5-2"] != "zai-org/GLM-5.2" {
		t.Errorf("merged = %v, want the configured map unchanged", merged)
	}
}

// The load-bearing invariant: everything published to the picker must resolve
// on the request path. A picker entry that cannot route is worse than absent.
func TestEveryPublishedAliasResolves(t *testing.T) {
	snapshot := snapshotWithSferenceModels(t,
		"zai-org/GLM-5.2",
		"zai-org/GLM-5.3-Flash",
		"Qwen/Qwen3.8-Flash-Next",
		"moonshotai/Kimi-K3",
	)
	configured := map[string]string{"claude-sference-glm-5-2": "zai-org/GLM-5.2"}
	effective := effectiveModelAliases(snapshot, configured)

	for _, entry := range aliasModelEntries(effective) {
		id, _ := entry["id"].(string)
		if id == "" {
			t.Fatalf("published entry without an id: %v", entry)
		}
		if effective[id] == "" {
			t.Errorf("published picker id %q does not resolve to a slug", id)
		}
	}
}

// The TLS door's picker injection reads /v1/admin/model-catalog and skips any
// model whose alias is empty, so that endpoint must annotate the same aliases
// /v1/models publishes. Reporting config-only aliases here left new catalog
// models out of Claude Code's picker even though the router routed them.
func TestModelCatalogAnnotatesDerivedAliases(t *testing.T) {
	snapshot := snapshotWithSferenceModels(t,
		"zai-org/GLM-5.2",
		"zai-org/GLM-5.3-Flash",
		"Qwen/Qwen3.8-Flash-Next",
	)
	g := &Gateway{pricing: pricing.New()}
	g.activeConfigFile = nil // no model_aliases configured at all

	models := g.modelCatalogModelsFromSnapshot(snapshot, []modelCatalogModel{
		{Slug: "zai-org/GLM-5.2"},
		{Slug: "zai-org/GLM-5.3-Flash"},
		{Slug: "Qwen/Qwen3.8-Flash-Next"},
	})

	for _, model := range models {
		if model.Alias == "" {
			t.Errorf("catalog model %q has no alias; the picker would skip it", model.Slug)
		}
	}
}
