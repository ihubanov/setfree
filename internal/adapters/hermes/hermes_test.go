package hermes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mindsdb/setfree/internal/adapters"
	"github.com/mindsdb/setfree/internal/gateway"
)

func TestHermesAdapter_IsRegistered(t *testing.T) {
	a, ok := adapters.Find("hermes")
	if !ok {
		t.Fatal("hermes adapter is not registered")
	}
	if a.DisplayName() != "Hermes Agent" {
		t.Errorf("DisplayName() = %q", a.DisplayName())
	}
}

func flagValue(args []string, key string) (string, bool) {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == key {
			return args[i+1], true
		}
	}
	return "", false
}

func envValue(env []string, key string) (string, bool) {
	prefix := key + "="
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			return strings.TrimPrefix(kv, prefix), true
		}
	}
	return "", false
}

func TestHostDerivedAPIKeyEnvName(t *testing.T) {
	cases := map[string]string{
		"https://api.mindshub.ai/v1":     "MINDSHUB_API_KEY",
		"https://api.deepseek.com/v1":    "DEEPSEEK_API_KEY",
		"https://api.groq.com/openai/v1": "GROQ_API_KEY",
		"https://api.mistral.ai/v1":      "MISTRAL_API_KEY",
		// Reserved: hermes already has its own host-gated candidate for these.
		"https://api.openai.com/v1":    "",
		"https://openrouter.ai/api/v1": "",
		"https://ollama.com/v1":        "",
		// No usable vendor label.
		"http://127.0.0.1:8000/v1":  "",
		"http://localhost:11434/v1": "",
		"https://single-label/v1":   "",
	}
	for baseURL, want := range cases {
		if got := hostDerivedAPIKeyEnvName(baseURL); got != want {
			t.Errorf("hostDerivedAPIKeyEnvName(%q) = %q, want %q", baseURL, got, want)
		}
	}
}

func TestHostDerivedAPIKeyEnvName_LookalikeHostPicksAttackerLabel(t *testing.T) {
	// Mirrors the Python's own documented threat model: a lookalike host
	// must resolve to the ATTACKER's label, never the spoofed vendor's —
	// so a real DEEPSEEK_API_KEY is never handed to an unrelated host.
	got := hostDerivedAPIKeyEnvName("https://api.deepseek.com.attacker.test/v1")
	if got != "ATTACKER_API_KEY" {
		t.Errorf("got %q, want the attacker's own label, not DEEPSEEK_API_KEY", got)
	}
}

func TestBuild_SetsProviderFlagAndCustomBaseURLEnv(t *testing.T) {
	build, err := adapter{}.Build(nil, gateway.Resolved{
		Gateway: gateway.Gateway{BaseURL: "https://api.mindshub.ai", APIKey: "sk-gateway"},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if v, ok := flagValue(build.Args, "--provider"); !ok || v != "custom" {
		t.Errorf("--provider = %q, %v, want \"custom\"", v, ok)
	}
	if v, ok := envValue(build.Env, "HERMES_INFERENCE_PROVIDER"); !ok || v != "custom" {
		t.Errorf("HERMES_INFERENCE_PROVIDER = %q, %v, want \"custom\"", v, ok)
	}
	// Hermes' OpenAI-client path appends /chat/completions itself.
	if v, ok := envValue(build.Env, "CUSTOM_BASE_URL"); !ok || v != "https://api.mindshub.ai/v1" {
		t.Errorf("CUSTOM_BASE_URL = %q, %v, want the /v1 segment appended", v, ok)
	}
	if v, ok := envValue(build.Env, "MINDSHUB_API_KEY"); !ok || v != "sk-gateway" {
		t.Errorf("MINDSHUB_API_KEY = %q, %v", v, ok)
	}
	if build.Note != "" {
		t.Errorf("Note = %q, want empty when a credential env var was derived", build.Note)
	}

	// The raw API key must never appear on argv — hermes has no flag that
	// would carry it, and this asserts SetFree never adds one either.
	for _, arg := range build.Args {
		if strings.Contains(arg, "sk-gateway") {
			t.Errorf("API key leaked into argv: %q", arg)
		}
	}
}

func TestBuild_DoesNotDoubleV1WhenBaseAlreadyEndsInIt(t *testing.T) {
	build, err := adapter{}.Build(nil, gateway.Resolved{
		Gateway: gateway.Gateway{BaseURL: "https://api.mindshub.ai/v1/", APIKey: "sk-gateway"},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if v, _ := envValue(build.Env, "CUSTOM_BASE_URL"); v != "https://api.mindshub.ai/v1" {
		t.Errorf("CUSTOM_BASE_URL = %q, want /v1 exactly once with no trailing slash", v)
	}
}

func TestBuild_NotesWhenNoCredentialEnvVarCanBeDerived(t *testing.T) {
	build, err := adapter{}.Build(nil, gateway.Resolved{
		Gateway: gateway.Gateway{BaseURL: "http://127.0.0.1:8000", APIKey: "sk-gateway"},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if build.Note == "" {
		t.Error("expected a Note warning that no credential env var could be derived")
	}
}

func TestBuild_ModelOverrideIsOptional(t *testing.T) {
	build, err := adapter{}.Build(nil, gateway.Resolved{
		Gateway: gateway.Gateway{BaseURL: "https://api.mindshub.ai", APIKey: "sk-gateway"},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if _, ok := flagValue(build.Args, "--model"); ok {
		t.Error("--model should be omitted with no model override")
	}

	build, err = adapter{}.Build(nil, gateway.Resolved{
		Gateway: gateway.Gateway{BaseURL: "https://api.mindshub.ai", APIKey: "sk-gateway"},
		Model:   "sonnet",
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if v, ok := flagValue(build.Args, "--model"); !ok || v != "sonnet" {
		t.Errorf("--model = %q, %v", v, ok)
	}
}

func TestDiscoverModels_ParsesOpenAIShapedCatalog(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-gateway" {
			t.Errorf("missing/wrong Authorization header: %q", r.Header.Get("Authorization"))
		}
		w.Write([]byte(`{"data":[{"id":"sonnet"},{"id":"gpt-codex"}]}`))
	}))
	defer srv.Close()

	d := adapter{}.DiscoverModels(context.Background(), gateway.Gateway{BaseURL: srv.URL, APIKey: "sk-gateway"})
	if !d.Supported {
		t.Fatalf("Discovery = %+v, want Supported", d)
	}
	if d.Native {
		t.Error("hermes has no native-prefix concept; Native should always be false")
	}
}

func TestDiscoverModels_UnsupportedEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	d := adapter{}.DiscoverModels(context.Background(), gateway.Gateway{BaseURL: srv.URL, APIKey: "sk-gateway"})
	if d.Supported {
		t.Errorf("expected an unsupported endpoint to report Supported=false, got %+v", d)
	}
}
