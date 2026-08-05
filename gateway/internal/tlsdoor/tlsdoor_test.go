package tlsdoor

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchSferenceModels(t *testing.T) {
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/admin/model-catalog" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"state":"ready","models":[
			{"slug":"zai-org/GLM-5.2","display_name":"GLM 5.2","alias":"claude-sference-glm-5-2","available":true},
			{"slug":"moonshotai/Kimi-K3","display_name":"Kimi K3","alias":"claude-sference-kimi-k3","available":true},
			{"slug":"unavailable/model","display_name":"Unavailable","alias":"claude-sference-unavail","available":false}
		]}`)
	}))
	defer admin.Close()

	adminAddr := strings.TrimPrefix(admin.URL, "http://")
	models := fetchSferenceModels(adminAddr)
	if len(models) != 2 {
		t.Fatalf("got %d models, want 2 (unavailable filtered out)", len(models))
	}
	if models[0].Model != "claude-sference-glm-5-2" {
		t.Errorf("first model = %q", models[0].Model)
	}
	if models[0].Name != "[Sference] GLM 5.2" {
		t.Errorf("first name = %q", models[0].Name)
	}
}

func TestFetchSferenceModelsUnreachable(t *testing.T) {
	models := fetchSferenceModels("127.0.0.1:1")
	if models != nil {
		t.Fatalf("got %d models from unreachable admin, want nil", len(models))
	}
}

func TestInjectSferenceModels(t *testing.T) {
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"state":"ready","models":[
			{"slug":"zai-org/GLM-5.2","display_name":"GLM 5.2","alias":"claude-sference-glm-5-2","available":true}
		]}`)
	}))
	defer admin.Close()
	adminAddr := strings.TrimPrefix(admin.URL, "http://")

	body := []byte(`{"additional_model_options":[],"other_field":"preserved"}`)
	injected := injectSferenceModels(body, adminAddr)

	var parsed map[string]interface{}
	if err := json.Unmarshal(injected, &parsed); err != nil {
		t.Fatalf("injected body is not valid JSON: %v", err)
	}
	opts, ok := parsed["additional_model_options"].([]interface{})
	if !ok {
		t.Fatal("additional_model_options missing or not array")
	}
	if len(opts) != 1 {
		t.Fatalf("got %d options, want 1", len(opts))
	}
	entry := opts[0].(map[string]interface{})
	if entry["model"] != "claude-sference-glm-5-2" {
		t.Errorf("model = %q", entry["model"])
	}
	if entry["name"] != "[Sference] GLM 5.2" {
		t.Errorf("name = %q", entry["name"])
	}
	if parsed["other_field"] != "preserved" {
		t.Errorf("other_field = %v, want preserved", parsed["other_field"])
	}
}

func TestInjectSferenceModelsDedupes(t *testing.T) {
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"state":"ready","models":[
			{"slug":"zai-org/GLM-5.2","display_name":"GLM 5.2","alias":"claude-sference-glm-5-2","available":true}
		]}`)
	}))
	defer admin.Close()
	adminAddr := strings.TrimPrefix(admin.URL, "http://")

	body := []byte(`{"additional_model_options":[{"model":"claude-sference-glm-5-2","name":"existing"}]}`)
	injected := injectSferenceModels(body, adminAddr)

	var parsed map[string]interface{}
	json.Unmarshal(injected, &parsed)
	opts := parsed["additional_model_options"].([]interface{})
	if len(opts) != 1 {
		t.Fatalf("got %d options, want 1 (deduped)", len(opts))
	}
}

func TestInjectSferenceModelsEmptyCatalog(t *testing.T) {
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"state":"signed_out","models":[]}`)
	}))
	defer admin.Close()
	adminAddr := strings.TrimPrefix(admin.URL, "http://")

	body := []byte(`{"additional_model_options":[]}`)
	injected := injectSferenceModels(body, adminAddr)
	if string(injected) != string(body) {
		t.Errorf("body modified when catalog is empty")
	}
}

func TestInjectSferenceModelsInvalidJSON(t *testing.T) {
	body := []byte(`not json`)
	injected := injectSferenceModels(body, "127.0.0.1:1")
	if string(injected) != string(body) {
		t.Errorf("body modified on invalid JSON")
	}
}
