package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/sference/sference-switch/gateway/internal/modelmeta"
	"github.com/sference/sference-switch/gateway/internal/pricing"
)

const (
	modelCatalogTimeout                       = 3 * time.Second
	modelCatalogPageSize                      = 1000
	modelCatalogMaxPages                      = 50
	modelCatalogMaxBody                       = 8 << 20
	modelCatalogSignedOutReasonNotSignedIn    = "not_signed_in"
	modelCatalogSignedOutReasonSessionExpired = "session_expired"
	sferenceModelAPIsAvailabilitySource       = "sference_model_apis"
)

var errModelCatalogUnauthorized = errors.New("model catalog authorization rejected")
var modelCatalogNow = time.Now

type modelCatalogResponse struct {
	State           string              `json:"state"`
	SignedOutReason string              `json:"signed_out_reason"`
	Models          []modelCatalogModel `json:"models"`
	FetchedAt       string              `json:"fetched_at"`
	Error           string              `json:"error"`
}

type modelCatalogModel struct {
	Slug          string `json:"slug"`
	DisplayName   string `json:"display_name"`
	StorageTarget string `json:"storage_target"`
	Alias         string `json:"alias,omitempty"`
	// AliasOneMillion is the [1m] twin id for a 1M-context model, empty
	// otherwise. It is a second picker entry for the SAME slug, not a
	// second model: the rows stay one-per-slug so slug-keyed consumers
	// (the app's projectModelCatalog) are unaffected, and only the TLS
	// door's picker injection fans it out into its own entry.
	AliasOneMillion string                 `json:"alias_1m,omitempty"`
	Label           string                 `json:"label,omitempty"`
	Available       bool                   `json:"available"`
	Reasoning       *modelCatalogReasoning `json:"reasoning,omitempty"`
}

type modelCatalogReasoning struct {
	Supported  bool                      `json:"supported"`
	Options    []pricing.ReasoningOption `json:"options"`
	Source     string                    `json:"source"`
	LoadedFrom string                    `json:"loaded_from"`
	Revision   string                    `json:"revision"`
	CapturedAt string                    `json:"captured_at"`
	Stale      bool                      `json:"stale"`
}

func (g *Gateway) adminModelCatalog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		g.reject(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// The live catalog belongs to the signed-in OAuth session. Do not use
	// sferenceAuthClient here because it can silently fall back to an API key.
	g.authMu.Lock()
	client := g.oauthClient
	g.authMu.Unlock()
	if client == nil {
		writeModelCatalogJSON(w, r.Method, modelCatalogResponse{
			State:           "signed_out",
			SignedOutReason: modelCatalogSignedOutReasonNotSignedIn,
			Models:          []modelCatalogModel{},
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), modelCatalogTimeout)
	defer cancel()
	models, err := g.fetchModelCatalog(ctx, client)
	if err != nil {
		if errors.Is(err, errModelCatalogUnauthorized) {
			writeModelCatalogJSON(w, r.Method, modelCatalogResponse{
				State:           "signed_out",
				SignedOutReason: modelCatalogSignedOutReasonSessionExpired,
				Models:          []modelCatalogModel{},
			})
			return
		}
		writeModelCatalogJSON(w, r.Method, modelCatalogResponse{
			State:  "error",
			Models: []modelCatalogModel{},
			Error:  modelCatalogPublicError(err),
		})
		return
	}
	fetchedAt := modelCatalogNow().UTC()
	if err := g.publishSferenceModelAPIAvailability(models, fetchedAt); err != nil {
		writeModelCatalogJSON(w, r.Method, modelCatalogResponse{
			State:  "error",
			Models: []modelCatalogModel{},
			Error:  modelCatalogPublicError(err),
		})
		return
	}
	snapshot := g.pricing.Capture()
	models = g.modelCatalogModelsFromSnapshot(snapshot, models)
	writeModelCatalogJSON(w, r.Method, modelCatalogResponse{
		State:     "ready",
		Models:    models,
		FetchedAt: fetchedAt.Format(time.RFC3339),
	})
}

func (g *Gateway) publishSferenceModelAPIAvailability(
	models []modelCatalogModel,
	capturedAt time.Time,
) error {
	if len(models) == 0 {
		// A valid empty account list is useful live state, but it is not enough
		// evidence to erase last-observed presentation metadata.
		return nil
	}
	snapshot := g.pricing.Capture()
	availability := make([]pricing.AvailabilityModel, 0, len(models))
	for _, model := range models {
		displayName := strings.TrimSpace(model.DisplayName)
		if displayName == "" {
			displayName = sferenceModelDisplayName(snapshot, model.Slug)
		}
		availability = append(availability, pricing.AvailabilityModel{
			CanonicalModelID: model.Slug,
			DisplayName:      displayName,
		})
	}
	if err := g.pricing.ReplaceProviderAvailability(
		pricing.ProviderSference,
		availability,
		sferenceModelAPIsAvailabilitySource,
		capturedAt,
		availabilityRevision(availability),
	); err != nil {
		return err
	}
	if strings.TrimSpace(g.runtimeConfig().ConfigPath) == "" {
		return nil
	}
	if err := g.persistProviderCatalogCaches(); err != nil {
		// Cache persistence is best effort for this interactive endpoint. The
		// complete live response and its in-memory snapshot remain valid.
		fmt.Fprintf(os.Stderr,
			"[gateway] could not persist Sference Model APIs catalog: %v\n",
			err)
	}
	return nil
}

func (g *Gateway) modelCatalogModelsFromSnapshot(
	snapshot *pricing.Snapshot,
	models []modelCatalogModel,
) []modelCatalogModel {
	// Build a reverse map from slug → alias over the aliases the gateway
	// actually serves: derived from this catalog, unioned with the active
	// config's model_aliases.
	//
	// This must match what /v1/models publishes. The TLS door's picker
	// injection reads THIS endpoint and skips any model without an alias, so
	// reporting config-only aliases here would leave catalog models out of
	// Claude Code's /model picker even though the router routes them.
	// The union can carry two ids per slug: the bare alias and its [1m]
	// 1M-context twin. Group per slug so the primary alias is chosen
	// deterministically (bare preferred — a [1m] twin never replaces the
	// main entry); the twin is published below as its own row so the TLS
	// door injects both picker entries.
	aliasesBySlug := map[string][]string{}
	g.stateMu.RLock()
	configured := map[string]string{}
	if g.activeConfigFile != nil {
		for _, c := range g.activeConfigFile.Clients {
			if c.ProtocolShape != "anthropic" && c.ProtocolShape != "" {
				continue
			}
			for alias, slug := range c.ModelAliases {
				configured[alias] = slug
			}
		}
	}
	g.stateMu.RUnlock()
	for alias, slug := range effectiveModelAliases(snapshot, configured) {
		aliasesBySlug[slug] = append(aliasesBySlug[slug], alias)
	}

	resolved := make([]modelCatalogModel, len(models))
	now := modelCatalogNow().UTC()
	for i, model := range models {
		displayName := sferenceModelDisplayName(snapshot, model.Slug)
		label := displayName
		if label == "" {
			label = model.Slug
		}
		twin, _ := oneMillionTwinAlias(
			snapshot, aliasesBySlug[model.Slug], model.Slug,
		)
		resolved[i] = modelCatalogModel{
			Slug:            model.Slug,
			DisplayName:     displayName,
			StorageTarget:   model.Slug,
			Alias:           primaryAlias(aliasesBySlug[model.Slug]),
			AliasOneMillion: twin,
			Label:           label,
			Available:       true,
			Reasoning: modelCatalogReasoningFromSnapshot(
				snapshot,
				model.Slug,
				now,
			),
		}
	}
	return resolved
}

// primaryAlias picks the id a catalog model publishes as its main picker
// entry: the lexicographically first alias without the [1m] decoration,
// or the first alias at all when every one carries it. The sort keeps the
// choice stable across restarts.
func primaryAlias(aliases []string) string {
	sorted := sortedKeys(aliases)
	for _, id := range sorted {
		if !strings.HasSuffix(id, autoAliasOneMillionSuffix) {
			return id
		}
	}
	if len(sorted) > 0 {
		return sorted[0]
	}
	return ""
}

// oneMillionTwinAlias returns the [1m] twin id for a 1M-context model,
// ok=false when the model is below 1M context, its window is unknown, it
// is absent from the pricing snapshot, or the union carries no twin.
func oneMillionTwinAlias(
	snapshot *pricing.Snapshot,
	aliases []string,
	slug string,
) (string, bool) {
	record, ok := snapshot.Model(pricing.ProviderSference, slug)
	if !ok || sferenceContextWindow(record) < oneMillionContextTokens {
		return "", false
	}
	for _, id := range sortedKeys(aliases) {
		if strings.HasSuffix(id, autoAliasOneMillionSuffix) {
			return id, true
		}
	}
	return "", false
}

func modelCatalogReasoningFromSnapshot(
	snapshot *pricing.Snapshot,
	canonicalID string,
	now time.Time,
) *modelCatalogReasoning {
	capability, ok := snapshot.ModelReasoning(
		pricing.ProviderSference,
		canonicalID,
	)
	if !ok {
		return nil
	}
	freshFrom := snapshot.ModelsDevValidatedAt(pricing.ProviderSference)
	if freshFrom.IsZero() {
		freshFrom = capability.Provenance.CapturedAt
	}
	stale := capability.Provenance.LoadedFrom !=
		pricing.LoadedFromVendoredFallback &&
		!freshFrom.IsZero() &&
		now.Sub(freshFrom) > publicCatalogStaleAfter
	return &modelCatalogReasoning{
		Supported:  capability.Supported,
		Options:    capability.Options,
		Source:     capability.Provenance.Source,
		LoadedFrom: string(capability.Provenance.LoadedFrom),
		Revision:   capability.Provenance.Revision,
		CapturedAt: capability.Provenance.CapturedAt.UTC().Format(
			time.RFC3339,
		),
		Stale: stale,
	}
}

func sferenceModelDisplayName(
	snapshot *pricing.Snapshot,
	canonicalID string,
) string {
	if displayName, ok := snapshot.DisplayName(
		pricing.ProviderSference,
		canonicalID,
	); ok {
		return displayName
	}
	return modelmeta.ResolveSference(canonicalID).DisplayName
}

func writeModelCatalogJSON(w http.ResponseWriter, method string, response modelCatalogResponse) {
	body, _ := json.Marshal(response)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if method != http.MethodHead {
		_, _ = w.Write(body)
	}
}

func (g *Gateway) fetchModelCatalog(ctx context.Context, client *http.Client) ([]modelCatalogModel, error) {
	endpoint, err := modelCatalogEndpoint(g.runtimeConfig().OAuthHost)
	if err != nil {
		return nil, err
	}

	models := make([]modelCatalogModel, 0)
	seenCursors := make(map[string]struct{})
	cursor := ""
	for pageNumber := 0; pageNumber < modelCatalogMaxPages; pageNumber++ {
		pageURL := *endpoint
		query := pageURL.Query()
		if cursor != "" {
			query.Set("cursor", cursor)
		}
		pageURL.RawQuery = query.Encode()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL.String(), nil)
		if err != nil {
			return nil, fmt.Errorf("build model catalog request: %w", err)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("request model catalog: %w", err)
		}
		body, err := readModelCatalogBody(resp)
		if err != nil {
			return nil, err
		}

		pageModels, nextCursor, hasMore, err := decodeModelCatalogPage(body)
		if err != nil {
			return nil, fmt.Errorf("decode model catalog: %w", err)
		}
		models = append(models, pageModels...)
		if !hasMore {
			return models, nil
		}
		if nextCursor == "" {
			return nil, errors.New("model catalog pagination omitted its cursor")
		}
		if _, exists := seenCursors[nextCursor]; exists {
			return nil, errors.New("model catalog returned a repeated pagination cursor")
		}
		seenCursors[nextCursor] = struct{}{}
		cursor = nextCursor
	}
	return nil, errors.New("model catalog exceeded pagination limit")
}

func modelCatalogEndpoint(oauthHost string) (*url.URL, error) {
	endpoint, err := url.Parse(strings.TrimRight(oauthHost, "/") + "/v1/models")
	if err != nil {
		return nil, fmt.Errorf("build model catalog endpoint: %w", err)
	}
	if endpoint.Scheme != "http" && endpoint.Scheme != "https" {
		return nil, errors.New("build model catalog endpoint: unsupported scheme")
	}
	if endpoint.Host == "" {
		return nil, errors.New("build model catalog endpoint: missing host")
	}
	return endpoint, nil
}

func readModelCatalogBody(resp *http.Response) ([]byte, error) {
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, modelCatalogMaxBody+1))
	closeErr := resp.Body.Close()
	if readErr != nil {
		return nil, fmt.Errorf("read model catalog response: %w", readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close model catalog response: %w", closeErr)
	}
	if len(body) > modelCatalogMaxBody {
		return nil, errors.New("model catalog response is too large")
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, errModelCatalogUnauthorized
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("model catalog upstream returned a non-success status")
	}
	return body, nil
}

func decodeModelCatalogPage(body []byte) ([]modelCatalogModel, string, bool, error) {
	var page struct {
		Data []json.RawMessage `json:"data"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&page); err != nil {
		return nil, "", false, err
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, "", false, err
	}

	models := make([]modelCatalogModel, 0, len(page.Data))
	for _, item := range page.Data {
		model, keep, err := decodeModelCatalogItem(item)
		if err != nil {
			return nil, "", false, err
		}
		if keep {
			models = append(models, model)
		}
	}
	// Sference /v1/models returns all models in one page (no pagination).
	return models, "", false, nil
}

func decodeModelCatalogItem(raw json.RawMessage) (modelCatalogModel, bool, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return modelCatalogModel{}, false, errors.New("model entry was not an object")
	}

	// Sference /v1/models uses "id" as the model slug (e.g. "zai-org/GLM-5.2").
	var slug string
	rawID, exists := fields["id"]
	if !exists || json.Unmarshal(rawID, &slug) != nil {
		return modelCatalogModel{}, false, nil
	}
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return modelCatalogModel{}, false, nil
	}

	displayName := ""
	if rawDisplayName, exists := fields["display_name"]; exists {
		_ = json.Unmarshal(rawDisplayName, &displayName)
		displayName = strings.TrimSpace(displayName)
	}

	return modelCatalogModel{
		Slug:        slug,
		DisplayName: displayName,
	}, true, nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("response contained multiple JSON values")
		}
		return err
	}
	return nil
}

func modelCatalogPublicError(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "Sference model catalog request timed out"
	}
	return "Unable to load the Sference model catalog"
}
