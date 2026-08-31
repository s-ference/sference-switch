package modelmeta

import "testing"

func TestResolveSferenceKnownDisplayNames(t *testing.T) {
	tests := map[string]string{
		"zai-org/GLM-5.2":                               "GLM 5.2",
		"moonshotai/Kimi-K2.7-Code":                     "Kimi K2.7 Code",
		"moonshotai/Kimi-K3":                            "Kimi K3",
		"nvidia/NVIDIA-Nemotron-3-Ultra-550B-A55B":      "NVIDIA Nemotron 3 Ultra 550B A55B",
		"another-org/NVIDIA_Nemotron_3_Ultra_550B_A55B": "NVIDIA Nemotron 3 Ultra 550B A55B",
	}
	for modelID, want := range tests {
		t.Run(modelID, func(t *testing.T) {
			got := ResolveSference(modelID)
			if got.ID != modelID || got.DisplayName != want {
				t.Fatalf("ResolveSference(%q) = %+v, want ID preserved and display %q", modelID, got, want)
			}
		})
	}
}

// The [1m] picker-twin gate reads this table: only models known to accept
// a 1M window may resolve above 0, and unknown leaves must stay 0 so a
// twin is never minted for a model we cannot vouch for.
func TestSferenceContextTokens(t *testing.T) {
	tests := map[string]int64{
		"zai-org/GLM-5.3":                     1_048_576,
		"zai-org/GLM_5_3":                     1_048_576,
		"zai-org/GLM-5.3-Flash":               1_048_576,
		"moonshotai/Kimi-K3":                  1_048_576,
		"deepseek-ai/DeepSeek-V4-Flash":       1_048_576,
		"bottlecapai/ThinkingCap-Qwen3.6-27B": 0,
		"Qwen/Qwen3.6-35B-A3B":                0,
		"private-org/Future-Model":            0,
		"":                                    0,
	}
	for modelID, want := range tests {
		if got := SferenceContextTokens(modelID); got != want {
			t.Errorf("SferenceContextTokens(%q) = %d, want %d", modelID, got, want)
		}
	}
}

func TestResolveSferenceHumanizesUnknownLeaf(t *testing.T) {
	got := ResolveSference("private-org/my__new---model\tv1")
	if got.ID != "private-org/my__new---model\tv1" ||
		got.DisplayName != "my new model v1" {
		t.Fatalf("unknown model = %+v", got)
	}
	if got := ResolveSference("org/---___"); got.DisplayName != "Unknown" {
		t.Fatalf("separator-only display = %q, want Unknown", got.DisplayName)
	}
}

func TestResolveClaudeFamilyUsesNormalizedIdentity(t *testing.T) {
	tests := []struct {
		recorded  string
		requested string
		want      Model
	}{
		{"OPUS", "claude-sonnet-4-6", Model{ID: "opus", DisplayName: "Opus"}},
		{"other", "claude-sonnet-4-6", Model{ID: "sonnet", DisplayName: "Sonnet"}},
		{"", "claude-fable-5", Model{ID: "fable", DisplayName: "Fable"}},
		{"", "unknown", Model{ID: "other", DisplayName: "Other"}},
	}
	for _, test := range tests {
		got := ResolveClaudeFamily(test.recorded, test.requested)
		if got != test.want {
			t.Errorf("ResolveClaudeFamily(%q, %q) = %+v, want %+v",
				test.recorded, test.requested, got, test.want)
		}
	}
}

func TestResolveClaudeFamilySupportsCatalogAndFutureFamilies(t *testing.T) {
	tests := []struct {
		name      string
		recorded  string
		requested string
		want      Model
	}{
		{
			name:     "validated catalog family",
			recorded: "newfamily",
			want:     Model{ID: "newfamily", DisplayName: "Newfamily"},
		},
		{
			name:      "generic modern Claude id",
			requested: "claude-newfamily-1",
			want:      Model{ID: "newfamily", DisplayName: "Newfamily"},
		},
		{
			name:      "historical version-first id remains other",
			requested: "claude-3-unknown-20250101",
			want:      Model{ID: "other", DisplayName: "Other"},
		},
		{
			name:      "unsafe recorded family is ignored",
			recorded:  "<script>",
			requested: "custom-model",
			want:      Model{ID: "other", DisplayName: "Other"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ResolveClaudeFamily(test.recorded, test.requested); got != test.want {
				t.Fatalf("ResolveClaudeFamily(%q, %q) = %+v, want %+v",
					test.recorded, test.requested, got, test.want)
			}
		})
	}
}
