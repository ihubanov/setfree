package hermes

import (
	"testing"

	"github.com/mindsdb/setfree/internal/adapters"
	"github.com/mindsdb/setfree/internal/detect"
	"github.com/mindsdb/setfree/internal/gateway"
)

func TestHermesDesktopAdapter_IsRegistered(t *testing.T) {
	a, ok := adapters.Find("hermes-desktop")
	if !ok {
		t.Fatal("hermes-desktop adapter is not registered")
	}
	if a.DisplayName() != "Hermes (Desktop)" {
		t.Errorf("DisplayName() = %q", a.DisplayName())
	}
}

// The adapter and detect must search the exact same places, or the landing
// screen could say "installed" while launch says "not found".
func TestDesktopBinaryNames_MatchDetect(t *testing.T) {
	got := desktopAdapter{}.BinaryNames()
	want := detect.HermesDesktopBinaries()
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("index %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

func TestDesktopBuild_SetsSameEnvAsCLI(t *testing.T) {
	build, err := desktopAdapter{}.Build(nil, gateway.Resolved{
		Gateway: gateway.Gateway{BaseURL: "https://api.mindshub.ai", APIKey: "sk-gateway"},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if v, ok := envValue(build.Env, "CUSTOM_BASE_URL"); !ok || v != "https://api.mindshub.ai/v1" {
		t.Errorf("CUSTOM_BASE_URL = %q, %v", v, ok)
	}
	if v, ok := envValue(build.Env, "MINDSHUB_API_KEY"); !ok || v != "sk-gateway" {
		t.Errorf("MINDSHUB_API_KEY = %q, %v", v, ok)
	}
	// Unlike the CLI adapter, the desktop app's spawned backend has no way
	// to receive --provider/--model flags, so Build must not pass any args.
	if len(build.Args) != 0 {
		t.Errorf("Args = %v, want none — the desktop backend can't receive them", build.Args)
	}
}

func TestDesktopBuild_AlwaysNotesTheSettingsPrecedenceCaveat(t *testing.T) {
	build, err := desktopAdapter{}.Build(nil, gateway.Resolved{
		Gateway: gateway.Gateway{BaseURL: "https://api.mindshub.ai", APIKey: "sk-gateway"},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if build.Note == "" {
		t.Error("expected a Note about Settings' saved provider taking precedence over env vars")
	}
}
