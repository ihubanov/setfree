package hermes

import (
	"context"

	"github.com/mindsdb/setfree/internal/adapters"
	"github.com/mindsdb/setfree/internal/detect"
	"github.com/mindsdb/setfree/internal/gateway"
)

// desktopAdapter launches the Hermes Agent desktop app (Electron;
// apps/desktop in NousResearch/hermes-agent) with SetFree's gateway
// environment in place. The app spawns its own Python backend
// (`hermes serve`-style) as a child process inheriting this launcher's
// environment wholesale, so the same Env this package builds for the CLI
// routes the desktop app's backend too — confirmed directly against
// electron/main.ts (env: {...process.env, ...}) and electron/backend-env.ts
// (never touches provider-related vars).
//
// One real caveat, surfaced via Note rather than silently possibly not
// working: unlike the CLI (where --provider always wins), the desktop
// backend calls resolve_runtime_provider() with no explicit args at all
// (gateway/run.py), and that path treats a provider already saved in
// ~/.hermes/config.yaml as authoritative over any environment variable.
// These env vars only take effect if the user hasn't already picked a
// different provider through the desktop app's own Settings screen.
type desktopAdapter struct{}

func init() {
	adapters.Register(desktopAdapter{})
}

func (desktopAdapter) Name() string          { return "hermes-desktop" }
func (desktopAdapter) DisplayName() string   { return "Hermes (Desktop)" }
func (desktopAdapter) BinaryNames() []string { return detect.HermesDesktopBinaries() }

func (desktopAdapter) Build(baseEnv []string, resolved gateway.Resolved) (adapters.Build, error) {
	env, note := Env(baseEnv, resolved)
	if note == "" {
		note = "If Hermes already has a provider selected in Settings, that saved choice wins over this launch's gateway — reset it in Settings first, or use `setfree hermes` (the CLI), which always overrides it."
	}
	return adapters.Build{Env: env, Note: note}, nil
}

func (desktopAdapter) DiscoverModels(ctx context.Context, gw gateway.Gateway) adapters.Discovery {
	return adapter{}.DiscoverModels(ctx, gw)
}
