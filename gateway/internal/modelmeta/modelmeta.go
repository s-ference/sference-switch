package modelmeta

import (
	"strings"
	"unicode"
)

// Model keeps the stable identity used for grouping separate from UI text.
type Model struct {
	ID          string
	DisplayName string
}

// ResolveSference preserves the raw served model ID and supplies a stable,
// server-owned display name.
func ResolveSference(modelID string) Model {
	return Model{
		ID:          modelID,
		DisplayName: sferenceDisplayName(modelID),
	}
}

// sferenceOneMillionContext lists Sference model leaves known to accept a
// 1M-token context window (platform production catalog, verified
// 2026-08-29). Everything else Sference serves is 256k or below, and the
// gateway's [1m] picker twin is only ever minted for these — a twin on a
// smaller model would make Claude Code believe 1M tokens the model
// rejects.
var sferenceOneMillionContext = map[string]bool{
	"glm-5.2":                true,
	"glm-5-2":                true,
	"glm-5.3":                true,
	"glm-5-3":                true,
	"glm-5.3-flash":          true,
	"glm-5-3-flash":          true,
	"kimi-k3":                true,
	"minimax-m3":             true,
	"deepseek-v4-pro":        true,
	"deepseek-v4-flash":      true,
	"deepseek-v4-flash-0731": true,
}

// SferenceContextTokens returns the vendored context window for a Sference
// model id, 0 when the leaf is unknown. Unknown means unknown — callers
// must treat 0 as "do not assume a large window", never as a small one.
func SferenceContextTokens(modelID string) int64 {
	if sferenceOneMillionContext[sferenceLeafKey(modelID)] {
		return 1_048_576
	}
	return 0
}

// sferenceLeafKey extracts the normalized leaf of a Sference model id:
// lowercased, trimmed, underscores folded to dashes ("zai-org/GLM-5.3"
// and "zai-org/GLM_5_3" share one key).
func sferenceLeafKey(modelID string) string {
	leaf := modelID
	if slash := strings.LastIndex(leaf, "/"); slash >= 0 {
		leaf = leaf[slash+1:]
	}
	knownKey := strings.ToLower(strings.TrimSpace(leaf))
	return strings.ReplaceAll(knownKey, "_", "-")
}

// ResolveClaudeFamily normalizes Claude traffic to a family identity. The
// recorded family wins when it is recognized; requestedModel is the fallback.
func ResolveClaudeFamily(recordedFamily, requestedModel string) Model {
	familyID := normalizedClaudeFamily(recordedFamily)
	if familyID == "" || familyID == "other" {
		familyID = familyFromModel(requestedModel)
	}
	if familyID == "" {
		familyID = "other"
	}
	return Model{ID: familyID, DisplayName: titleASCII(familyID)}
}

func sferenceDisplayName(modelID string) string {
	leaf := modelID
	if slash := strings.LastIndex(leaf, "/"); slash >= 0 {
		leaf = leaf[slash+1:]
	}
	knownKey := sferenceLeafKey(modelID)
	switch knownKey {
	case "glm-5.2", "glm-5-2":
		return "GLM 5.2"
	case "kimi-k2.7-code", "kimi-k2-7-code":
		return "Kimi K2.7 Code"
	case "kimi-k3":
		return "Kimi K3"
	case "nvidia-nemotron-3-ultra-550b-a55b":
		return "NVIDIA Nemotron 3 Ultra 550B A55B"
	default:
		return humanizeLeaf(leaf)
	}
}

func humanizeLeaf(leaf string) string {
	var out strings.Builder
	pendingSpace := false
	for _, value := range leaf {
		if value == '-' || value == '_' || unicode.IsSpace(value) || unicode.IsControl(value) {
			pendingSpace = out.Len() > 0
			continue
		}
		if pendingSpace {
			out.WriteByte(' ')
			pendingSpace = false
		}
		out.WriteRune(value)
	}
	value := strings.TrimSpace(out.String())
	if value == "" {
		return "Unknown"
	}
	return value
}

func normalizedClaudeFamily(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "fable", "opus", "sonnet", "haiku", "other":
		return value
	}
	return normalizedFamilyToken(value)
}

func familyFromModel(modelID string) string {
	lower := strings.ToLower(modelID)
	for _, family := range []string{"fable", "opus", "sonnet", "haiku"} {
		if strings.Contains(lower, family) {
			return family
		}
	}
	parts := strings.Split(lower, "-")
	if len(parts) >= 3 && parts[0] == "claude" {
		if family := normalizedFamilyToken(parts[1]); family != "" &&
			!startsWithASCIIDigit(family) {
			return family
		}
	}
	return "other"
}

func normalizedFamilyToken(value string) string {
	if value == "" || len(value) > 64 {
		return ""
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') ||
			(char >= '0' && char <= '9') ||
			char == '-' || char == '_' {
			continue
		}
		return ""
	}
	return value
}

func startsWithASCIIDigit(value string) bool {
	return value != "" && value[0] >= '0' && value[0] <= '9'
}

func titleASCII(value string) string {
	if value == "" {
		return "Other"
	}
	return strings.ToUpper(value[:1]) +
		strings.ReplaceAll(strings.ReplaceAll(value[1:], "-", " "), "_", " ")
}
