package gateway

import (
	"regexp"
	"strings"

	"github.com/sference/sference-switch/gateway/internal/pricing"
)

// Automatic alias derivation.
//
// Claude Code's model picker only renders ids matching /(claude|anthropic)/i,
// so a raw Sference slug like "zai-org/GLM-5.3-Flash" can never be a picker
// entry — something must mint an Anthropic-shaped id for it. That used to be
// the hand-written model_aliases map in gateway.yaml, which meant every new
// catalog model was invisible in /model until a human edited YAML on every
// install. Sference Switch exists to integrate with Claude Code without that
// step, so aliases are derived from the catalog the gateway already holds.
//
// The derivation is a pure function of a pricing snapshot: no network call, no
// startup ordering. loadProviderCatalogCaches populates the Sference slice from
// ~/.sference/switch/catalogs/sference-public.json before any listener binds,
// so the first request already sees the full catalog.
//
// Every consumer of the alias map reads the union (derived ∪ configured) so a
// published picker entry is always routable — a picker entry the request path
// cannot resolve would be worse than no entry at all.

// autoAliasPrefix is the namespace for derived ids. It is one of
// aliasNamespacePrefixes, so InAliasNamespace already recognises them and an
// alias that disappears from the catalog produces the usual actionable
// unknown-alias error rather than a silent default-model route.
const autoAliasPrefix = "claude-sference-"

// autoAliasNonAlphanumeric collapses every run of characters that cannot
// appear in a model id. Slugs carry vendor prefixes and punctuation
// ("Qwen/Qwen3.8-Flash-Next") that must survive as a stable, readable id.
var autoAliasNonAlphanumeric = regexp.MustCompile(`[^a-z0-9]+`)

// autoAliasExcludedSlugs are catalog models kept out of the picker. They are
// reachable by raw slug and in the app's per-model override list; this only
// governs what is published to /model, where an unfiltered list would bury the
// chat models a user is choosing between.
//
// Matching is exact on the canonical slug: a substring rule would silently
// swallow future models that merely share a vendor or a word.
var autoAliasExcludedSlugs = map[string]bool{
	// Task-specific internal tooling, not a coding-harness chat model.
	"Shovels/ettin-permit-tagger": true,
	// Vision model; Claude Code's picker selects the main loop model.
	"Qwen/Qwen3-VL-30B-A3B-Instruct": true,
	// Dated pin of a model already published under its undated slug;
	// listing both invites picking the stale one by mistake.
	"deepseek-ai/DeepSeek-V4-Flash-0731": true,
}

// autoAliasID derives the picker id for a Sference slug, returning ok=false
// when no valid id can be formed.
//
// The result is validated with the same predicates that gate hand-written
// aliases (discoveryPrefixOK, reservedAnthropicModelRe) rather than a parallel
// set of rules, so a derived alias can never be something config would reject.
func autoAliasID(slug string) (string, bool) {
	trimmed := strings.TrimSpace(slug)
	if trimmed == "" {
		return "", false
	}
	normalized := autoAliasNonAlphanumeric.ReplaceAllString(
		strings.ToLower(trimmed), "-")
	normalized = strings.Trim(normalized, "-")
	if normalized == "" {
		return "", false
	}
	id := autoAliasPrefix + normalized
	if !discoveryPrefixOK(id) || reservedAnthropicModelRe.MatchString(id) {
		return "", false
	}
	return id, true
}

// deriveAutoAliases returns alias id -> slug for every catalog model that
// should appear in the picker.
//
// Ordering note: pricing.Snapshot.Models sorts by canonical id, so a slug
// collision (two slugs normalising to the same id) resolves to the
// lexicographically first slug on every run rather than varying by map
// iteration order.
func deriveAutoAliases(snapshot *pricing.Snapshot) map[string]string {
	if snapshot == nil {
		return nil
	}
	records := snapshot.Models(pricing.ProviderSference)
	if len(records) == 0 {
		return nil
	}
	out := make(map[string]string, len(records))
	for _, record := range records {
		slug := strings.TrimSpace(record.CanonicalModelID)
		if slug == "" || autoAliasExcludedSlugs[slug] {
			continue
		}
		id, ok := autoAliasID(slug)
		if !ok {
			continue
		}
		if _, taken := out[id]; taken {
			continue
		}
		out[id] = slug
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// effectiveModelAliases merges derived aliases with the client's configured
// model_aliases. Configured entries win: a user who pinned an id to a slug
// keeps exactly that mapping, and existing installs see no id churn.
//
// Returns configured unchanged when nothing is derived, so behaviour with an
// empty or unavailable catalog is identical to before this feature.
func effectiveModelAliases(
	snapshot *pricing.Snapshot,
	configured map[string]string,
) map[string]string {
	derived := deriveAutoAliases(snapshot)
	if len(derived) == 0 {
		return configured
	}
	merged := make(map[string]string, len(derived)+len(configured))
	for id, slug := range derived {
		merged[id] = slug
	}
	// A configured id overrides a derived one. Also drop any derived alias
	// pointing at a slug the config already publishes under a different id,
	// so one model never appears twice in the picker.
	configuredSlugs := make(map[string]bool, len(configured))
	for _, slug := range configured {
		configuredSlugs[slug] = true
	}
	for id, slug := range merged {
		if _, isConfigured := configured[id]; isConfigured {
			continue
		}
		if configuredSlugs[slug] {
			delete(merged, id)
		}
	}
	for id, slug := range configured {
		merged[id] = slug
	}
	return merged
}
