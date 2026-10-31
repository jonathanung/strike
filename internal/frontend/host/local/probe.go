package local

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/jonathanung/strike/internal/frontend/host"
	"github.com/jonathanung/strike/internal/product/auth"
	"github.com/jonathanung/strike/internal/product/config"
)

var _ host.KeyProber = authAdapter{}

// probeTimeout bounds one credential probe so a dead endpoint cannot hang
// the Providers page.
const probeTimeout = 10 * time.Second

// probeTracker remembers the last decisive probe outcome per provider
// (verified / rejected) for the life of the daemon. A stored key that has
// never probed valid is reported as host.KeyStored, never as connected
// (#1531). Outcomes live in memory only: after a daemon restart a key is
// unverified again, which is the honest default.
type probeTracker struct {
	mu     sync.Mutex
	states map[string]string
}

func newProbeTracker() *probeTracker {
	return &probeTracker{states: map[string]string{}}
}

// get returns the recorded decisive state ("" when none). Nil-safe so a
// zero-value authAdapter degrades to "no recorded probe".
func (t *probeTracker) get(provider string) string {
	if t == nil {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.states[provider]
}

func (t *probeTracker) set(provider, state string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.states == nil {
		t.states = map[string]string{}
	}
	t.states[provider] = state
}

func (t *probeTracker) clear(provider string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.states, provider)
}

// probeDialects select how a stored key is presented to an endpoint.
const (
	dialectOpenAI    = "openai"    // Authorization: Bearer <key>
	dialectAnthropic = "anthropic" // x-api-key + anthropic-version
	dialectGoogle    = "google"    // x-goog-api-key
)

// builtinProbeBase mirrors factory.builtinDefaultBaseURL for the key
// providers (google has no factory default; its adapter default is used).
func builtinProbeBase(provider string) (baseURL, dialect string, ok bool) {
	switch provider {
	case "anthropic":
		return "https://api.anthropic.com", dialectAnthropic, true
	case "openai":
		return "https://api.openai.com/v1", dialectOpenAI, true
	case "xai":
		return "https://api.x.ai/v1", dialectOpenAI, true
	case "google":
		return "https://generativelanguage.googleapis.com/v1beta", dialectGoogle, true
	case "kimi":
		return "https://api.moonshot.cn/v1", dialectOpenAI, true
	case "deepseek":
		return "https://api.deepseek.com/v1", dialectOpenAI, true
	}
	return "", "", false
}

// keyProbeRequest builds the minimal authenticated request that proves a
// key: listing models with the credential presented. A bad key is refused
// (401/403) by every supported vendor, while an unauthenticated catalog
// listing (the old "Test") proves nothing (#1531).
func keyProbeRequest(baseURL, dialect, key string, extra map[string]string) (string, http.Header, error) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return "", nil, fmt.Errorf("no endpoint configured")
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", nil, fmt.Errorf("endpoint %q is not an http(s) URL", baseURL)
	}
	var probeURL string
	if dialect == dialectAnthropic && !strings.HasSuffix(base, "/v1") {
		// Anthropic bases are origins (…/v1 is appended), like MessagesURL.
		probeURL = base + "/v1/models"
	} else {
		probeURL = base + "/models"
	}
	header := http.Header{}
	for k, v := range extra {
		if strings.TrimSpace(k) != "" {
			header.Set(k, v)
		}
	}
	switch dialect {
	case dialectAnthropic:
		header.Set("x-api-key", key)
		header.Set("anthropic-version", "2023-06-01")
	case dialectGoogle:
		header.Set("x-goog-api-key", key)
	default: // openai-compatible (incl. "responses")
		header.Set("Authorization", "Bearer "+key)
	}
	return probeURL, header, nil
}

// classifyProbe maps an HTTP status onto a probe outcome. Only 2xx proves
// the key; 401/403 prove it is wrong; anything else is inconclusive.
func classifyProbe(status int) string {
	switch {
	case status >= 200 && status < 300:
		return host.ProbeValid
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return host.ProbeInvalid
	default:
		return host.ProbeUnreachable
	}
}

// runKeyProbe executes one probe request and classifies the answer. The
// response body is drained (bounded) and discarded — it is never surfaced,
// so a vendor error page cannot leak into the UI or logs.
func runKeyProbe(ctx context.Context, client *http.Client, probeURL string, header http.Header) host.KeyProbeResult {
	if client == nil {
		client = http.DefaultClient
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, probeURL, nil)
	if err != nil {
		return host.KeyProbeResult{Status: host.ProbeUnreachable, Detail: "could not build the probe request"}
	}
	req.Header = header
	resp, err := client.Do(req)
	if err != nil {
		return host.KeyProbeResult{Status: host.ProbeUnreachable, Detail: "could not reach the provider (network error or timeout)"}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	switch classifyProbe(resp.StatusCode) {
	case host.ProbeValid:
		return host.KeyProbeResult{Status: host.ProbeValid, Detail: "the provider accepted the stored key"}
	case host.ProbeInvalid:
		return host.KeyProbeResult{Status: host.ProbeInvalid, Detail: fmt.Sprintf("the provider refused the stored key (HTTP %d)", resp.StatusCode)}
	default:
		return host.KeyProbeResult{Status: host.ProbeUnreachable, Detail: fmt.Sprintf("the provider answered HTTP %d, which neither proves nor refutes the key", resp.StatusCode)}
	}
}

// probeTarget resolves the endpoint, dialect, stored key, and extra headers
// for one provider. A non-nil result short-circuits the probe (no key,
// OAuth-only, unknown endpoint, …).
func (a authAdapter) probeTarget(provider string) (probeURL string, header http.Header, result *host.KeyProbeResult) {
	fail := func(status, detail string) *host.KeyProbeResult {
		return &host.KeyProbeResult{Status: status, Detail: detail}
	}
	var baseURL, dialect, key string
	var extra map[string]string
	if cp, ok := a.customs.Get(provider); ok {
		resolved := config.ResolveCustom(cp)
		baseURL = resolved.BaseURL
		dialect = string(resolved.API)
		if dialect == string(config.WireAnthropic) {
			dialect = dialectAnthropic
		} else {
			dialect = dialectOpenAI
		}
		extra = resolved.Headers
		k, has := auth.APIKeyEnv(provider, a.store, resolved.APIKeyEnv)
		if !has || k == "" {
			return "", nil, fail(host.ProbeNoKey, "no API key is stored for this provider")
		}
		key = k
	} else {
		base, d, known := builtinProbeBase(provider)
		if !known {
			return "", nil, fail(host.ProbeUnsupported, "this provider has no API key to test")
		}
		baseURL, dialect = base, d
		envName := ""
		if ep, ok := a.customs.Endpoint(provider); ok {
			resolved := config.ResolveEndpoint(ep)
			if resolved.BaseURL != "" {
				baseURL = resolved.BaseURL
			}
			envName = resolved.APIKeyEnv
			extra = resolved.Headers
		}
		if envName != "" {
			k, has := auth.APIKeyEnv(provider, a.store, envName)
			if has && k != "" {
				key = k
			}
		}
		if key == "" {
			k, has := auth.APIKey(provider, a.store)
			if has && k != "" {
				key = k
			}
		}
		if key == "" {
			if cred, ok := a.store.Get(provider); ok && cred.Type == auth.TypeOAuth {
				return "", nil, fail(host.ProbeUnsupported, "signed in with OAuth — there is no API key to test")
			}
			return "", nil, fail(host.ProbeNoKey, "no API key is stored for this provider")
		}
	}
	u, h, err := keyProbeRequest(baseURL, dialect, key, extra)
	if err != nil {
		return "", nil, fail(host.ProbeUnreachable, err.Error())
	}
	return u, h, nil
}

// ProbeAPIKey implements host.KeyProber: it presents the provider's stored
// API key to the provider's own models endpoint and reports the verdict.
// Decisive outcomes (valid / invalid) are recorded so Statuses can report
// KeyVerified / KeyRejected until the key changes.
func (a authAdapter) ProbeAPIKey(ctx context.Context, provider string) host.KeyProbeResult {
	provider = config.CanonicalProviderID(provider)
	probeURL, header, early := a.probeTarget(provider)
	if early != nil {
		return *early
	}
	client := a.probeClient
	if client == nil {
		client = &http.Client{}
	}
	res := runKeyProbe(ctx, client, probeURL, header)
	switch res.Status {
	case host.ProbeValid:
		a.probes.set(provider, host.KeyVerified)
	case host.ProbeInvalid:
		a.probes.set(provider, host.KeyRejected)
	}
	return res
}

// keyStateFor reports the KeyState for one provider: a decisive probe
// outcome when one is recorded, otherwise host.KeyStored when an API key
// is present (stored or from the environment), else "".
func (a authAdapter) keyStateFor(provider string) string {
	if state := a.probes.get(provider); state != "" {
		return state
	}
	if !a.acceptsAPIKey(provider) {
		return ""
	}
	var key string
	if cp, ok := a.customs.Get(provider); ok {
		resolved := config.ResolveCustom(cp)
		k, has := auth.APIKeyEnv(provider, a.store, resolved.APIKeyEnv)
		if has {
			key = k
		}
	} else {
		envName := ""
		if ep, ok := a.customs.Endpoint(provider); ok {
			envName = config.ResolveEndpoint(ep).APIKeyEnv
		}
		if envName != "" {
			if k, has := auth.APIKeyEnv(provider, a.store, envName); has {
				key = k
			}
		}
		if key == "" {
			if k, has := auth.APIKey(provider, a.store); has {
				key = k
			}
		}
	}
	if key != "" {
		return host.KeyStored
	}
	return ""
}
