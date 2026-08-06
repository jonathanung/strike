package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jonathanung/strike-cli/internal/sandbox"
)

const (
	websearchDefaultTimeout = 30 * time.Second
	websearchMaxTimeout     = 120 * time.Second
	websearchDefaultLimit   = 5
	websearchMaxLimit       = 10
	websearchMaxBody        = 1 << 20 // 1 MiB API response bound
	websearchUserAgent      = webfetchUserAgent

	websearchProviderBrave = "brave"
	braveDefaultBaseURL    = "https://api.search.brave.com"
	braveDefaultAPIKeyEnv  = "BRAVE_API_KEY"
)

// WebSearchSettings carries config webSearch onto tool execution.
// Empty Provider means auto-detect from the environment (brave when its
// API key env is set). Searcher, when non-nil, replaces the HTTP backend
// (tests / fakes).
type WebSearchSettings struct {
	Provider  string
	APIKeyEnv string
	BaseURL   string
	// Searcher overrides the configured provider backend when non-nil.
	Searcher WebSearchFunc
}

// WebSearchQuery is the normalized request passed to a search backend.
type WebSearchQuery struct {
	Query          string
	Limit          int
	IncludeDomains []string
	ExcludeDomains []string
	Timeout        time.Duration
}

// WebSearchHit is one citation-ready search result.
type WebSearchHit struct {
	Title     string `json:"title"`
	URL       string `json:"url"`
	Snippet   string `json:"snippet"`
	Age       string `json:"age,omitempty"`
	Provider  string `json:"provider"`
	FetchedAt string `json:"fetchedAt"` // RFC3339
}

// WebSearchFunc is a pluggable search backend (production HTTP or test fake).
type WebSearchFunc func(ctx context.Context, q WebSearchQuery) ([]WebSearchHit, error)

type webSearchTool struct{}

// NewWebSearch returns the permissioned web search tool.
func NewWebSearch() Tool { return webSearchTool{} }

func (webSearchTool) Name() string { return "websearch" }

func (webSearchTool) Contract() Contract {
	return staticContract(SideEffectNetwork, IdempotencySafeRetry)
}

func (webSearchTool) Description() string {
	return `Search the public web for programming-related sources and return citation-ready results.

- Takes a query and optional result limit / domain filters
- Returns titles, URLs, snippets, provider identity, and timestamps
- Does NOT fetch full page bodies — use webfetch on a selected URL
- Cite sources in answers using the returned markdown links

Usage notes:
  - Prefer this tool when you need to discover docs, release notes, or error reports without a known URL
  - Optional include_domains / exclude_domains filter result hosts (e.g. ["pkg.go.dev","github.com"])
  - Optional limit (default 5, max 10)
  - Optional timeout in seconds (default 30, max 120)
  - Requires a configured search backend (see webSearch in config); missing setup returns actionable guidance
  - Respects network.allow, permission rules, cancellation, and credential redaction on the audit path`
}

func (webSearchTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"query": {"type": "string", "description": "Search query (natural language or keywords)"},
			"limit": {"type": "integer", "description": "Maximum results to return (default 5, max 10)"},
			"include_domains": {"type": "array", "items": {"type": "string"}, "description": "Only include results whose host matches these domains (e.g. pkg.go.dev)"},
			"exclude_domains": {"type": "array", "items": {"type": "string"}, "description": "Exclude results whose host matches these domains"},
			"timeout": {"type": "number", "description": "Optional timeout in seconds (max 120)"}
		},
		"required": ["query"]
	}`)
}

type webSearchArgs struct {
	Query          string   `json:"query"`
	Limit          *int     `json:"limit"`
	IncludeDomains []string `json:"include_domains"`
	ExcludeDomains []string `json:"exclude_domains"`
	Timeout        *float64 `json:"timeout"`
}

func (webSearchTool) Execute(ctx context.Context, args json.RawMessage, tc *Context) (Result, error) {
	var a webSearchArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{}, ErrInvalidArgs(fmt.Sprintf("invalid arguments: %v", err))
	}
	query := strings.TrimSpace(a.Query)
	if query == "" {
		return Result{}, ErrInvalidArgs("query is required")
	}

	limit := websearchDefaultLimit
	if a.Limit != nil {
		limit = *a.Limit
	}
	if limit <= 0 {
		return Result{}, ErrInvalidArgs("limit must be a positive integer")
	}
	if limit > websearchMaxLimit {
		limit = websearchMaxLimit
	}

	include := normalizeDomainFilters(a.IncludeDomains)
	exclude := normalizeDomainFilters(a.ExcludeDomains)

	timeout := websearchDefaultTimeout
	if a.Timeout != nil && *a.Timeout > 0 {
		timeout = time.Duration(*a.Timeout * float64(time.Second))
		if timeout > websearchMaxTimeout {
			timeout = websearchMaxTimeout
		}
	}

	settings := webSearchSettingsFrom(tc)
	// Fail closed on missing/unknown backend before Ask so operators are not
	// prompted for a tool that cannot run.
	if err := ensureWebSearchConfigured(settings); err != nil {
		return Result{}, err
	}

	if err := tc.Ask(ctx, AskRequest{
		Permission: "websearch",
		Patterns:   []string{query},
		Always:     []string{"*"},
	}); err != nil {
		return Result{}, err
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	q := WebSearchQuery{
		Query:          query,
		Limit:          limit,
		IncludeDomains: include,
		ExcludeDomains: exclude,
		Timeout:        timeout,
	}

	searcher, provider, err := resolveWebSearchBackend(settings, timeout, networkAllowFrom(tc))
	if err != nil {
		return Result{}, err
	}

	hits, err := searcher(runCtx, q)
	if err != nil {
		if runCtx.Err() != nil {
			if ctx.Err() != nil {
				return Result{}, err
			}
			return Result{}, fmt.Errorf("websearch timed out: %w", err)
		}
		return Result{}, err
	}

	// Client-side domain filters (backends may not support all filters).
	hits = filterWebSearchHits(hits, include, exclude, limit)
	now := time.Now().UTC().Format(time.RFC3339)
	for i := range hits {
		if hits[i].Provider == "" {
			hits[i].Provider = provider
		}
		if hits[i].FetchedAt == "" {
			hits[i].FetchedAt = now
		}
	}

	output := formatWebSearchOutput(query, provider, now, hits)
	meta, _ := json.Marshal(map[string]any{
		"provider":   provider,
		"query":      query,
		"searchedAt": now,
		"count":      len(hits),
		"limit":      limit,
		"results":    hits,
	})
	title := fmt.Sprintf("websearch %q (%d results via %s)", query, len(hits), provider)
	if len(hits) == 0 {
		title = fmt.Sprintf("websearch %q (0 results via %s)", query, provider)
	}
	return Result{
		Title:    title,
		Output:   output,
		Metadata: meta,
	}, nil
}

func webSearchSettingsFrom(tc *Context) WebSearchSettings {
	if tc == nil {
		return WebSearchSettings{}
	}
	return tc.WebSearch
}

// ensureWebSearchConfigured reports missing/unknown backend setup without
// touching the network (so Ask is not required for config guidance).
func ensureWebSearchConfigured(s WebSearchSettings) error {
	if s.Searcher != nil {
		return nil
	}
	provider := strings.ToLower(strings.TrimSpace(s.Provider))
	apiKeyEnv := strings.TrimSpace(s.APIKeyEnv)
	switch provider {
	case "":
		env := apiKeyEnv
		if env == "" {
			env = braveDefaultAPIKeyEnv
		}
		if strings.TrimSpace(os.Getenv(env)) == "" {
			return missingWebSearchBackendError(websearchProviderBrave, env)
		}
		return nil
	case websearchProviderBrave:
		if apiKeyEnv == "" {
			apiKeyEnv = braveDefaultAPIKeyEnv
		}
		if strings.TrimSpace(os.Getenv(apiKeyEnv)) == "" {
			return missingWebSearchBackendError(websearchProviderBrave, apiKeyEnv)
		}
		return nil
	default:
		return ErrPrecondition(fmt.Sprintf(
			"unknown webSearch.provider %q (supported: brave). Set webSearch.provider in config or omit it for auto-detect.",
			provider,
		))
	}
}

// resolveWebSearchBackend picks a searcher and provider id from settings/env.
// allow is the config network.allow list applied to the search API host.
// Callers should run ensureWebSearchConfigured first.
func resolveWebSearchBackend(s WebSearchSettings, timeout time.Duration, allow []string) (WebSearchFunc, string, error) {
	if s.Searcher != nil {
		provider := strings.TrimSpace(s.Provider)
		if provider == "" {
			provider = "test"
		}
		return s.Searcher, provider, nil
	}
	provider := strings.ToLower(strings.TrimSpace(s.Provider))
	apiKeyEnv := strings.TrimSpace(s.APIKeyEnv)
	baseURL := strings.TrimSpace(s.BaseURL)

	switch provider {
	case "":
		// Auto: brave when its key env is present.
		env := apiKeyEnv
		if env == "" {
			env = braveDefaultAPIKeyEnv
		}
		if strings.TrimSpace(os.Getenv(env)) == "" {
			return nil, "", missingWebSearchBackendError(websearchProviderBrave, env)
		}
		provider = websearchProviderBrave
		apiKeyEnv = env
	case websearchProviderBrave:
		if apiKeyEnv == "" {
			apiKeyEnv = braveDefaultAPIKeyEnv
		}
	default:
		return nil, "", ErrPrecondition(fmt.Sprintf(
			"unknown webSearch.provider %q (supported: brave). Set webSearch.provider in config or omit it for auto-detect.",
			provider,
		))
	}

	apiHost, err := webSearchAPIHost(provider, baseURL)
	if err != nil {
		return nil, "", ErrPrecondition(err.Error())
	}
	if err := assertPublicHTTPHost(apiHost); err != nil {
		return nil, "", err
	}
	if err := sandbox.CheckNetworkAllow(apiHost, allow); err != nil {
		return nil, "", err
	}
	apiKey := strings.TrimSpace(os.Getenv(apiKeyEnv))
	if apiKey == "" {
		return nil, "", missingWebSearchBackendError(provider, apiKeyEnv)
	}
	b := newBraveWebSearch(apiKeyEnv, baseURL)
	b.apiKey = apiKey
	b.client = webSearchHTTPClient(timeout, allow)
	return b.Search, provider, nil
}

func missingWebSearchBackendError(provider, apiKeyEnv string) error {
	if apiKeyEnv == "" {
		apiKeyEnv = braveDefaultAPIKeyEnv
	}
	if provider == "" {
		provider = websearchProviderBrave
	}
	msg := fmt.Sprintf(`websearch backend not configured.

Configure a search provider in ~/.strike/config.jsonc (or ./.strike/config.jsonc):

  "webSearch": {
    "provider": %q,
    "apiKeyEnv": %q
  }

Then export the API key (Brave Search API: https://brave.com/search/api/):

  export %s=...

Omitting webSearch.provider still works when %s is set (defaults to brave).
Use webfetch when you already have a source URL — search only discovers sources.`,
		provider, apiKeyEnv, apiKeyEnv, apiKeyEnv)
	return ErrPrecondition(msg)
}

func webSearchAPIHost(provider, baseURL string) (string, error) {
	raw := strings.TrimSpace(baseURL)
	if raw == "" {
		switch provider {
		case websearchProviderBrave, "":
			raw = braveDefaultBaseURL
		default:
			return "", fmt.Errorf("no default API host for provider %q; set webSearch.baseURL", provider)
		}
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid webSearch.baseURL: %w", err)
	}
	host := u.Hostname()
	if host == "" {
		return "", fmt.Errorf("webSearch.baseURL must include a host")
	}
	return host, nil
}

func webSearchHTTPClient(timeout time.Duration, allow []string) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: webfetchHTTPTransport(allow),
	}
}

type braveWebSearch struct {
	apiKeyEnv string
	baseURL   string
	apiKey    string
	client    *http.Client
}

func newBraveWebSearch(apiKeyEnv, baseURL string) *braveWebSearch {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = braveDefaultBaseURL
	}
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	return &braveWebSearch{apiKeyEnv: apiKeyEnv, baseURL: baseURL}
}

func (b *braveWebSearch) Search(ctx context.Context, q WebSearchQuery) ([]WebSearchHit, error) {
	if b == nil {
		return nil, fmt.Errorf("brave search backend not initialized")
	}
	if b.client == nil {
		b.client = webSearchHTTPClient(q.Timeout, nil)
	}
	if b.apiKey == "" {
		b.apiKey = strings.TrimSpace(os.Getenv(b.apiKeyEnv))
	}
	if b.apiKey == "" {
		return nil, missingWebSearchBackendError(websearchProviderBrave, b.apiKeyEnv)
	}

	// Fetch extra when domain filters may drop hits.
	count := q.Limit
	if len(q.IncludeDomains) > 0 || len(q.ExcludeDomains) > 0 {
		count = websearchMaxLimit
	}
	if count < 1 {
		count = websearchDefaultLimit
	}
	if count > 20 {
		count = 20 // Brave API max
	}

	query := q.Query
	// Prefer server-side site: narrowing when a single include domain is set.
	if len(q.IncludeDomains) == 1 && !strings.Contains(strings.ToLower(query), "site:") {
		query = query + " site:" + q.IncludeDomains[0]
	}

	u, err := url.Parse(b.baseURL + "/res/v1/web/search")
	if err != nil {
		return nil, fmt.Errorf("brave base URL: %w", err)
	}
	qs := u.Query()
	qs.Set("q", query)
	qs.Set("count", strconv.Itoa(count))
	u.RawQuery = qs.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("X-Subscription-Token", b.apiKey)
	req.Header.Set("User-Agent", websearchUserAgent)

	resp, err := b.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	limited := io.LimitReader(resp.Body, websearchMaxBody+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if len(body) > websearchMaxBody {
		return nil, fmt.Errorf("search response too large")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(body))
		if len(msg) > 200 {
			msg = msg[:200] + "…"
		}
		if msg == "" {
			msg = resp.Status
		}
		return nil, fmt.Errorf("brave search failed: %s", msg)
	}

	var parsed braveSearchResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("brave search decode: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	out := make([]WebSearchHit, 0, len(parsed.Web.Results))
	for _, r := range parsed.Web.Results {
		title := strings.TrimSpace(r.Title)
		link := strings.TrimSpace(r.URL)
		if title == "" || link == "" {
			continue
		}
		out = append(out, WebSearchHit{
			Title:     title,
			URL:       link,
			Snippet:   strings.TrimSpace(r.Description),
			Age:       strings.TrimSpace(r.Age),
			Provider:  websearchProviderBrave,
			FetchedAt: now,
		})
	}
	return out, nil
}

type braveSearchResponse struct {
	Web struct {
		Results []struct {
			Title       string `json:"title"`
			URL         string `json:"url"`
			Description string `json:"description"`
			Age         string `json:"age"`
		} `json:"results"`
	} `json:"web"`
}

func normalizeDomainFilters(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	var out []string
	for _, d := range in {
		d = strings.ToLower(strings.TrimSpace(d))
		d = strings.TrimPrefix(d, "*.")
		d = strings.TrimPrefix(d, ".")
		if d == "" {
			continue
		}
		// Strip accidental scheme/path.
		if i := strings.Index(d, "://"); i >= 0 {
			d = d[i+3:]
		}
		if i := strings.IndexAny(d, "/:"); i >= 0 {
			d = d[:i]
		}
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		if _, ok := seen[d]; ok {
			continue
		}
		seen[d] = struct{}{}
		out = append(out, d)
	}
	return out
}

func filterWebSearchHits(hits []WebSearchHit, include, exclude []string, limit int) []WebSearchHit {
	if limit <= 0 {
		limit = websearchDefaultLimit
	}
	out := make([]WebSearchHit, 0, min(len(hits), limit))
	for _, h := range hits {
		host := hitHost(h.URL)
		if host == "" {
			continue
		}
		if len(include) > 0 && !domainFilterMatch(host, include) {
			continue
		}
		if len(exclude) > 0 && domainFilterMatch(host, exclude) {
			continue
		}
		out = append(out, h)
		if len(out) >= limit {
			break
		}
	}
	return out
}

func hitHost(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

func domainFilterMatch(host string, domains []string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	for _, d := range domains {
		d = strings.ToLower(strings.TrimSpace(d))
		if d == "" {
			continue
		}
		if host == d || strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	return false
}

func formatWebSearchOutput(query, provider, searchedAt string, hits []WebSearchHit) string {
	var b strings.Builder
	b.WriteString("# Web search results\n\n")
	b.WriteString("Query: ")
	b.WriteString(query)
	b.WriteString("\nProvider: ")
	b.WriteString(provider)
	b.WriteString("\nSearched at: ")
	b.WriteString(searchedAt)
	b.WriteString("\nResults: ")
	b.WriteString(strconv.Itoa(len(hits)))
	b.WriteString("\n\n")
	if len(hits) == 0 {
		b.WriteString("No results matched the query and domain filters.\n")
		b.WriteString("Try a broader query, drop include_domains, or webfetch a known URL.\n")
		return b.String()
	}
	b.WriteString("Cite sources in your answer using the markdown links below. Prefer webfetch on a selected URL for full page content.\n\n")
	for i, h := range hits {
		b.WriteString("## ")
		b.WriteString(strconv.Itoa(i + 1))
		b.WriteString(". ")
		b.WriteString(h.Title)
		b.WriteString("\n")
		b.WriteString("- URL: ")
		b.WriteString(h.URL)
		b.WriteString("\n")
		if h.Snippet != "" {
			b.WriteString("- Snippet: ")
			b.WriteString(h.Snippet)
			b.WriteString("\n")
		}
		if h.Age != "" {
			b.WriteString("- Age: ")
			b.WriteString(h.Age)
			b.WriteString("\n")
		}
		b.WriteString("- Cite: [")
		b.WriteString(escapeMarkdownLinkText(h.Title))
		b.WriteString("](")
		b.WriteString(h.URL)
		b.WriteString(")\n\n")
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func escapeMarkdownLinkText(s string) string {
	s = strings.ReplaceAll(s, "[", "\\[")
	s = strings.ReplaceAll(s, "]", "\\]")
	return s
}
