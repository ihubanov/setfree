// Package hermes adapts SetFree's normalized gateway into the environment
// and CLI flags Hermes Agent (github.com/NousResearch/hermes-agent, the
// `hermes` binary) reads to talk to a custom OpenAI-compatible endpoint.
//
// Hermes has a real generic "custom" provider (aliases: ollama, vllm,
// llamacpp), but — unlike Codex or Claude Code — it exposes no CLI flag
// that carries a literal base_url or API key; `--base-url`/`--api-key`
// simply don't exist on any hermes subcommand (confirmed against
// hermes_cli/main.py, cli.py, and `hermes --help`/`hermes chat --help`).
// The actual mechanism, read directly out of
// hermes_cli/runtime_provider.py's _resolve_openrouter_runtime (the
// generic fallback resolver "custom" ultimately reaches when no
// custom_providers config entry exists):
//
//   - CUSTOM_BASE_URL — read unconditionally as a base_url candidate, no
//     host gating. (OPENAI_BASE_URL, despite being documented in
//     environment-variables.md, is explicitly no longer consulted — see
//     the comment directly above where CUSTOM_BASE_URL is read.)
//   - The API key comes from _host_derived_api_key: it takes the
//     registrable label of the base_url's hostname (strip "api."/"www.",
//     then the second-to-last label — "api.mindshub.ai" -> "mindshub"),
//     uppercases it, and reads "<LABEL>_API_KEY" from the environment.
//     This is generic by construction, not a MindsHub special case — it's
//     the same lookup that makes DEEPSEEK_API_KEY/GROQ_API_KEY etc. work
//     for their own hosts.
//   - --provider custom (a real, documented top-level flag) makes the
//     request itself resolve through this path; HERMES_INFERENCE_PROVIDER
//     is also set as a defense-in-depth backup, since some auxiliary
//     model calls (aux/naming, background review) resolve their own
//     provider independently of the main session's --provider flag.
//
// Live-verified end to end against a real `hermes` CLI and gateway before
// this shipped — not just read off the source.
//
// This also means the API key never touches argv (no flag carries it),
// matching the claude/codex adapters' security posture, without the
// tradeoff Codex's own env_key indirection needed a config write to avoid.
package hermes

import (
	"context"
	"net/url"
	"strings"
	"unicode"

	"github.com/mindsdb/setfree/internal/adapters"
	"github.com/mindsdb/setfree/internal/envutil"
	"github.com/mindsdb/setfree/internal/gateway"
)

const envProvider = "HERMES_INFERENCE_PROVIDER"
const envCustomBaseURL = "CUSTOM_BASE_URL"

// reservedVendorLabels are handled by hermes' own explicit, host-gated
// candidates (OPENAI_API_KEY for openai.com/azure, OPENROUTER_API_KEY for
// openrouter.ai, OLLAMA_API_KEY for ollama.com) ahead of the generic
// derivation, so _host_derived_api_key refuses to re-derive them — see
// hermes_cli/runtime_provider.py's _host_derived_api_key.
var reservedVendorLabels = map[string]bool{"OPENAI": true, "OPENROUTER": true, "OLLAMA": true}

type adapter struct{}

func init() {
	adapters.Register(adapter{})
}

func (adapter) Name() string          { return "hermes" }
func (adapter) DisplayName() string   { return "Hermes Agent" }
func (adapter) BinaryNames() []string { return []string{"hermes"} }

func (adapter) Build(baseEnv []string, resolved gateway.Resolved) (adapters.Build, error) {
	env, note := Env(baseEnv, resolved)

	args := []string{"--provider", "custom"}
	if resolved.Model != "" {
		args = append(args, "--model", resolved.Model)
	}

	return adapters.Build{Env: env, Args: args, Note: note}, nil
}

// Env builds the environment that routes a Hermes-family process (the CLI
// directly, or the desktop app's spawned backend) through resolved's
// gateway. Exported because the desktop adapter needs the identical
// env — its backend has no way to receive the CLI's --provider/--model
// flags, so it depends entirely on this to route correctly at all.
func Env(baseEnv []string, resolved gateway.Resolved) (env []string, note string) {
	// Hermes' OpenAI-client-based custom provider appends "/chat/completions"
	// itself, so base_url must end in the API version segment — the same
	// requirement the codex adapter has, and gateways are saved as bare
	// hosts, so add it here rather than 404ing on <host>/chat/completions.
	baseURL := strings.TrimRight(resolved.Gateway.BaseURL, "/")
	if !strings.HasSuffix(strings.ToLower(baseURL), "/v1") {
		baseURL += "/v1"
	}

	env = envutil.Set(baseEnv, envProvider, "custom")
	env = envutil.Set(env, envCustomBaseURL, baseURL)

	if keyEnv := hostDerivedAPIKeyEnvName(baseURL); keyEnv != "" {
		env = envutil.Set(env, keyEnv, resolved.Gateway.APIKey)
	} else {
		// No named custom_providers entry exists (SetFree never writes
		// one), and the derivation that stands in for it couldn't name an
		// env var for this host — hermes has no other way to learn the
		// key for a bare `provider: custom` and will send none.
		note = "Hermes couldn't derive a credential env var from this gateway's hostname; it may connect without authentication."
	}
	return env, note
}

// hostDerivedAPIKeyEnvName ports hermes_cli/runtime_provider.py's
// _host_derived_api_key — the env var *name* half of it; Build supplies the
// value directly rather than reading it back out of the environment. Kept
// byte-for-byte faithful to the Python (including accepting a digit
// anywhere in the TLD as "looks like an IP", not just a leading one) so a
// host this rejects is rejected the same way on both sides.
func hostDerivedAPIKeyEnvName(baseURL string) string {
	u, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	hostname := u.Hostname()
	if hostname == "" || hostname == "localhost" || strings.Contains(hostname, ":") {
		return ""
	}

	var labels []string
	for _, l := range strings.Split(hostname, ".") {
		if l != "" {
			labels = append(labels, l)
		}
	}
	if len(labels) == 0 {
		return ""
	}
	for _, ch := range labels[len(labels)-1] {
		if unicode.IsDigit(ch) {
			return "" // last label looks like an IP octet, not a TLD
		}
	}
	for len(labels) > 0 && (labels[0] == "api" || labels[0] == "www") {
		labels = labels[1:]
	}
	if len(labels) < 2 {
		return ""
	}
	vendor := labels[len(labels)-2]

	var sanitized strings.Builder
	for _, ch := range vendor {
		if unicode.IsLetter(ch) || unicode.IsDigit(ch) {
			sanitized.WriteRune(unicode.ToUpper(ch))
		} else {
			sanitized.WriteRune('_')
		}
	}
	s := sanitized.String()
	if s == "" || !unicode.IsUpper(rune(s[0])) || reservedVendorLabels[s] {
		return ""
	}
	return s + "_API_KEY"
}

// DiscoverModels probes gw the way Hermes' own custom-endpoint model picker
// does (hermes_cli/models.py's probe_api_models: GET <base_url>/models with
// a bearer token, reading data[].id) — the plain OpenAI shape, with no
// vendor-prefix rewriting to hedge against, so nativePrefixes is left empty.
func (adapter) DiscoverModels(ctx context.Context, gw gateway.Gateway) adapters.Discovery {
	return adapters.ProbeModels(ctx, gw.BaseURL, map[string]string{
		"Authorization": "Bearer " + gw.APIKey,
	}, nil)
}
