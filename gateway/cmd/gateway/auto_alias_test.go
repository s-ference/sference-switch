package gateway

import (
	"strings"
	"testing"
	"time"

	"github.com/sference/sference-switch/gateway/internal/pricing"
)

// snapshotWithSferenceModels builds a pricing snapshot carrying the given
// Sference slugs, the same way loadProviderCatalogCaches populates one from
// the on-disk catalog cache at startup. Context windows are absent, as
// they are in the real cache.
func snapshotWithSferenceModels(t *testing.T, slugs ...string) *pricing.Snapshot {
	t.Helper()
	models := make([]pricing.AvailabilityModel, 0, len(slugs))
	for _, slug := range slugs {
		models = append(models, pricing.AvailabilityModel{
			CanonicalModelID: slug,
			DisplayName:      slug,
		})
	}
	return snapshotWithSferenceModelsWithContext(t, models...)
}

// snapshotWithSferenceModelsWithContext seeds availability with explicit
// context windows so tests can exercise the [1m] twin derivation.
func snapshotWithSferenceModelsWithContext(
	t *testing.T,
	models ...pricing.AvailabilityModel,
) *pricing.Snapshot {
	t.Helper()
	p := pricing.New()
	if err := p.ReplaceProviderAvailability(
		pricing.ProviderSference,
		models,
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

// A [1m] twin must satisfy the same predicates as the alias it decorates,
// or the picker would drop it and config would have rejected it.
func TestAutoAliasOneMillionIDSatisfiesConfigValidators(t *testing.T) {
	base, ok := autoAliasID("zai-org/GLM-5.3")
	if !ok {
		t.Fatal("autoAliasID(\"zai-org/GLM-5.3\") returned ok=false")
	}
	twin, ok := autoAliasOneMillionID(base)
	if !ok {
		t.Fatal("autoAliasOneMillionID returned ok=false")
	}
	if twin != base+"[1m]" {
		t.Errorf("twin = %q, want %q+[1m]", twin, base)
	}
	if !discoveryPrefixOK(twin) {
		t.Errorf("twin id %q fails Claude Code's discovery filter", twin)
	}
	if reservedAnthropicModelRe.MatchString(twin) {
		t.Errorf("twin id %q collides with a real Anthropic model name", twin)
	}
	if !InAliasNamespace(twin) {
		t.Errorf("twin id %q is outside the gateway alias namespace", twin)
	}
	if _, ok := autoAliasOneMillionID(""); ok {
		t.Error("autoAliasOneMillionID(\"\") returned ok=true")
	}
}

// Only 1M-context models get a [1m] twin: on a smaller model the twin would
// make Claude Code believe 1M tokens the model rejects.
func TestDeriveAutoAliasesEmitsOneMillionTwins(t *testing.T) {
	snapshot := snapshotWithSferenceModelsWithContext(t,
		pricing.AvailabilityModel{
			CanonicalModelID: "zai-org/GLM-5.3",
			DisplayName:      "GLM 5.3",
			ContextTokens:    1_048_576,
		},
		pricing.AvailabilityModel{
			CanonicalModelID: "bottlecapai/ThinkingCap-Qwen3.6-27B",
			DisplayName:      "ThinkingCap",
			ContextTokens:    262_144,
		},
	)
	derived := deriveAutoAliases(snapshot)
	if derived["claude-sference-zai-org-glm-5-3[1m]"] != "zai-org/GLM-5.3" {
		t.Errorf("1M model has no [1m] twin: %v", derived)
	}
	if _, present := derived["claude-sference-bottlecapai-thinkingcap-qwen3-6-27b[1m]"]; present {
		t.Error("262k model published a [1m] twin")
	}
}

// The live catalog fetch and on-disk cache carry no context_tokens today, so
// the vendored modelmeta table is what real installs rely on: a known 1M
// model with a context-less record still gets its twin, an unknown model
// never does.
func TestDeriveAutoAliasesTwinsUseVendoredContextTable(t *testing.T) {
	snapshot := snapshotWithSferenceModels(t,
		"zai-org/GLM-5.3",
		"private-org/Brand-New-Model",
	)
	derived := deriveAutoAliases(snapshot)
	if derived["claude-sference-zai-org-glm-5-3[1m]"] != "zai-org/GLM-5.3" {
		t.Errorf("known 1M model without live context evidence has no twin: %v", derived)
	}
	if _, present := derived["claude-sference-private-org-brand-new-model[1m]"]; present {
		t.Error("unknown model published a [1m] twin")
	}
}

// A configured alias owns the slug's bare entry, but the [1m] twin is a
// distinct picker option and must survive the duplicate suppression.
func TestEffectiveModelAliasesKeepsTwinUnderConfiguredAlias(t *testing.T) {
	snapshot := snapshotWithSferenceModels(t, "zai-org/GLM-5.3")
	configured := map[string]string{"claude-sference-glm-53": "zai-org/GLM-5.3"}

	merged := effectiveModelAliases(snapshot, configured)

	if merged["claude-sference-glm-53"] != "zai-org/GLM-5.3" {
		t.Errorf("configured alias lost: %v", merged)
	}
	if merged["claude-sference-zai-org-glm-5-3[1m]"] != "zai-org/GLM-5.3" {
		t.Errorf("[1m] twin was suppressed under the configured alias: %v", merged)
	}
	if merged["claude-sference-zai-org-glm-5-3"] != "" {
		t.Errorf("derived bare alias duplicates the configured entry: %v", merged)
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
	// picker would list one model twice. Its [1m] twin is exempt: that is a
	// distinct 1M-context option for the same model, not a duplicate.
	count := 0
	for id, slug := range merged {
		if slug == "zai-org/GLM-5.2" &&
			!strings.HasSuffix(id, autoAliasOneMillionSuffix) {
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

// A 1M model lists once, as its [1m] id — the bare entry would offer the
// model at a fifth of its real window. The bare id stays routable; this is a
// publish-layer filter only.
func TestAliasModelEntriesPublishesOnlyOneMillionFor1MModels(t *testing.T) {
	snapshot := snapshotWithSferenceModelsWithContext(t,
		pricing.AvailabilityModel{
			CanonicalModelID: "zai-org/GLM-5.3",
			DisplayName:      "GLM 5.3",
			ContextTokens:    1_048_576,
		},
		pricing.AvailabilityModel{
			CanonicalModelID: "bottlecapai/ThinkingCap-Qwen3.6-27B",
			DisplayName:      "ThinkingCap",
			ContextTokens:    262_144,
		},
	)
	aliases := effectiveModelAliases(snapshot, nil)
	published := map[string]bool{}
	for _, entry := range aliasModelEntries(aliases) {
		id, _ := entry["id"].(string)
		published[id] = true
	}
	if !published["claude-sference-zai-org-glm-5-3[1m]"] {
		t.Errorf("1M model's [1m] id not published: %v", published)
	}
	if published["claude-sference-zai-org-glm-5-3"] {
		t.Errorf("bare id of a 1M model published alongside its [1m] id: %v", published)
	}
	if !published["claude-sference-bottlecapai-thinkingcap-qwen3-6-27b"] {
		t.Errorf("sub-1M model's bare id not published: %v", published)
	}
	if _, twin := published["claude-sference-bottlecapai-thinkingcap-qwen3-6-27b[1m]"]; twin {
		t.Errorf("262k model published a [1m] id: %v", published)
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

// The door reads alias_1m to inject the second picker entry, so the admin
// endpoint must publish the twin alongside the bare alias — on the SAME row.
// Rows stay one-per-slug: the app's projectModelCatalog dedupes by slug, so a
// second row would be silently dropped there while doubling the model list
// everywhere else.
func TestModelCatalogPublishesOneMillionTwinAlias(t *testing.T) {
	snapshot := snapshotWithSferenceModelsWithContext(t,
		pricing.AvailabilityModel{
			CanonicalModelID: "zai-org/GLM-5.3",
			DisplayName:      "GLM 5.3",
			ContextTokens:    1_048_576,
		},
		pricing.AvailabilityModel{
			CanonicalModelID: "bottlecapai/ThinkingCap-Qwen3.6-27B",
			DisplayName:      "ThinkingCap",
			ContextTokens:    262_144,
		},
	)
	g := &Gateway{pricing: pricing.New()}
	g.activeConfigFile = nil

	models := g.modelCatalogModelsFromSnapshot(snapshot, []modelCatalogModel{
		{Slug: "zai-org/GLM-5.3"},
		{Slug: "bottlecapai/ThinkingCap-Qwen3.6-27B"},
	})

	if len(models) != 2 {
		t.Fatalf("rows = %d, want one per slug: %+v", len(models), models)
	}
	bySlug := map[string]modelCatalogModel{}
	for _, model := range models {
		if model.Alias == "" {
			t.Errorf("catalog row %q has no alias; the picker would skip it", model.Slug)
		}
		if strings.HasSuffix(model.Alias, autoAliasOneMillionSuffix) {
			t.Errorf("row %q published the [1m] twin as its primary alias %q",
				model.Slug, model.Alias)
		}
		bySlug[model.Slug] = model
	}
	oneMillion := bySlug["zai-org/GLM-5.3"]
	if oneMillion.Alias != "claude-sference-zai-org-glm-5-3" {
		t.Errorf("bare alias = %q", oneMillion.Alias)
	}
	if oneMillion.AliasOneMillion != "claude-sference-zai-org-glm-5-3[1m]" {
		t.Errorf("alias_1m = %q, want the [1m] twin", oneMillion.AliasOneMillion)
	}
	if smaller := bySlug["bottlecapai/ThinkingCap-Qwen3.6-27B"]; smaller.AliasOneMillion != "" {
		t.Errorf("262k model published alias_1m = %q, want empty", smaller.AliasOneMillion)
	}
}
