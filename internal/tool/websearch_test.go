package tool

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func fakeWebSearchHits(n int) []WebSearchHit {
	out := make([]WebSearchHit, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, WebSearchHit{
			Title:     "Result " + string(rune('A'+i)),
			URL:       "https://example.com/r" + string(rune('a'+i)),
			Snippet:   "Snippet for result " + string(rune('A'+i)),
			Provider:  "test",
			FetchedAt: "2026-01-02T03:04:05Z",
		})
	}
	return out
}

func TestWebSearchHappyPathCitations(t *testing.T) {
	hits := []WebSearchHit{
		{
			Title:   "Go Modules Reference",
			URL:     "https://go.dev/ref/mod",
			Snippet: "Modules are how Go manages dependencies.",
			Age:     "2 days ago",
		},
		{
			Title:   "pkg.go.dev host",
			URL:     "https://pkg.go.dev/golang.org/x/sync",
			Snippet: "Package sync provides basic synchronization.",
		},
	}
	tc := allowAll(t.TempDir())
	tc.WebSearch = WebSearchSettings{
		Provider: "test",
		Searcher: func(_ context.Context, q WebSearchQuery) ([]WebSearchHit, error) {
			if q.Query != "go modules" {
				t.Fatalf("query = %q", q.Query)
			}
			if q.Limit != 5 {
				t.Fatalf("limit = %d", q.Limit)
			}
			return hits, nil
		},
	}
	res, err := NewWebSearch().Execute(context.Background(), mustJSON(t, map[string]any{
		"query": "go modules",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"# Web search results",
		"Provider: test",
		"Go Modules Reference",
		"https://go.dev/ref/mod",
		"Modules are how Go manages dependencies.",
		"[Go Modules Reference](https://go.dev/ref/mod)",
		"pkg.go.dev host",
	} {
		if !strings.Contains(res.Output, want) {
			t.Errorf("output missing %q\n%s", want, res.Output)
		}
	}
	var meta map[string]any
	if err := json.Unmarshal(res.Metadata, &meta); err != nil {
		t.Fatal(err)
	}
	if meta["provider"] != "test" {
		t.Errorf("meta.provider = %v", meta["provider"])
	}
	if meta["count"] != float64(2) {
		t.Errorf("meta.count = %v", meta["count"])
	}
	if !strings.Contains(res.Title, "2 results") {
		t.Errorf("title = %q", res.Title)
	}
}

func TestWebSearchMissingBackendGuidance(t *testing.T) {
	t.Setenv("BRAVE_API_KEY", "")
	tc := allowAll(t.TempDir())
	// Explicit empty settings, no searcher, no env key.
	tc.WebSearch = WebSearchSettings{}
	_, err := NewWebSearch().Execute(context.Background(), mustJSON(t, map[string]any{
		"query": "anything",
	}), tc)
	if err == nil {
		t.Fatal("expected missing backend error")
	}
	msg := err.Error()
	for _, want := range []string{
		"websearch backend not configured",
		"webSearch",
		"BRAVE_API_KEY",
		"brave",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("guidance missing %q\n%s", want, msg)
		}
	}
	if CodeOf(err) != string(CodePreconditionFailed) {
		t.Errorf("code = %q, want precondition_failed", CodeOf(err))
	}
}

func TestWebSearchUnknownProvider(t *testing.T) {
	tc := allowAll(t.TempDir())
	tc.WebSearch = WebSearchSettings{Provider: "acme-search"}
	_, err := NewWebSearch().Execute(context.Background(), mustJSON(t, map[string]any{
		"query": "x",
	}), tc)
	if err == nil {
		t.Fatal("expected unknown provider error")
	}
	if !strings.Contains(err.Error(), "unknown webSearch.provider") {
		t.Errorf("err = %v", err)
	}
}

func TestWebSearchPermissionDenied(t *testing.T) {
	tc := &Context{
		WorkDir: t.TempDir(),
		Ask:     func(context.Context, AskRequest) error { return errors.New("denied") },
		WebSearch: WebSearchSettings{
			Searcher: func(context.Context, WebSearchQuery) ([]WebSearchHit, error) {
				t.Fatal("searcher must not run after deny")
				return nil, nil
			},
		},
	}
	_, err := NewWebSearch().Execute(context.Background(), mustJSON(t, map[string]any{
		"query": "secret leak",
	}), tc)
	if err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("err = %v", err)
	}
}

func TestWebSearchAskPatterns(t *testing.T) {
	var saw AskRequest
	tc := &Context{
		WorkDir: t.TempDir(),
		Ask: func(_ context.Context, req AskRequest) error {
			saw = req
			return nil
		},
		WebSearch: WebSearchSettings{
			Searcher: func(context.Context, WebSearchQuery) ([]WebSearchHit, error) {
				return nil, nil
			},
		},
	}
	_, err := NewWebSearch().Execute(context.Background(), mustJSON(t, map[string]any{
		"query": "golang context cancel",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if saw.Permission != "websearch" {
		t.Errorf("permission = %q", saw.Permission)
	}
	if len(saw.Patterns) != 1 || saw.Patterns[0] != "golang context cancel" {
		t.Errorf("patterns = %#v", saw.Patterns)
	}
}

func TestWebSearchDomainFiltersAndLimit(t *testing.T) {
	all := []WebSearchHit{
		{Title: "A", URL: "https://pkg.go.dev/foo", Snippet: "a"},
		{Title: "B", URL: "https://github.com/foo/bar", Snippet: "b"},
		{Title: "C", URL: "https://spam.example/x", Snippet: "c"},
		{Title: "D", URL: "https://docs.github.com/en", Snippet: "d"},
		{Title: "E", URL: "https://go.dev/blog", Snippet: "e"},
	}
	tc := allowAll(t.TempDir())
	tc.WebSearch = WebSearchSettings{
		Searcher: func(_ context.Context, q WebSearchQuery) ([]WebSearchHit, error) {
			if q.Limit != 2 {
				t.Fatalf("limit passed to backend = %d", q.Limit)
			}
			if len(q.IncludeDomains) != 2 {
				t.Fatalf("include = %#v", q.IncludeDomains)
			}
			return all, nil
		},
	}
	res, err := NewWebSearch().Execute(context.Background(), mustJSON(t, map[string]any{
		"query":           "x",
		"limit":           2,
		"include_domains": []string{"pkg.go.dev", "github.com"},
		"exclude_domains": []string{"spam.example"},
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	// include keeps pkg.go.dev + github.com (+ docs.github.com subdomain);
	// exclude drops spam; limit 2 keeps first two matches.
	if strings.Contains(res.Output, "spam.example") {
		t.Fatal("excluded domain leaked")
	}
	if !strings.Contains(res.Output, "pkg.go.dev") {
		t.Fatal("expected pkg.go.dev")
	}
	if !strings.Contains(res.Output, "github.com/foo/bar") {
		t.Fatal("expected github.com result")
	}
	// Third include-match (docs.github.com) should be cut by limit.
	if strings.Contains(res.Output, "docs.github.com") {
		t.Fatal("limit should stop at 2 include matches")
	}
	var meta map[string]any
	if err := json.Unmarshal(res.Metadata, &meta); err != nil {
		t.Fatal(err)
	}
	if meta["count"] != float64(2) {
		t.Errorf("count = %v", meta["count"])
	}
}

func TestWebSearchLimitClamp(t *testing.T) {
	tc := allowAll(t.TempDir())
	var sawLimit int
	tc.WebSearch = WebSearchSettings{
		Searcher: func(_ context.Context, q WebSearchQuery) ([]WebSearchHit, error) {
			sawLimit = q.Limit
			return fakeWebSearchHits(websearchMaxLimit), nil
		},
	}
	_, err := NewWebSearch().Execute(context.Background(), mustJSON(t, map[string]any{
		"query": "x",
		"limit": 99,
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if sawLimit != websearchMaxLimit {
		t.Fatalf("limit = %d, want %d", sawLimit, websearchMaxLimit)
	}
}

func TestWebSearchInvalidArgs(t *testing.T) {
	tc := allowAll(t.TempDir())
	tc.WebSearch = WebSearchSettings{
		Searcher: func(context.Context, WebSearchQuery) ([]WebSearchHit, error) {
			t.Fatal("should not search")
			return nil, nil
		},
	}
	cases := []struct {
		name string
		args map[string]any
		sub  string
	}{
		{"empty query", map[string]any{"query": "  "}, "query"},
		{"missing query", map[string]any{}, "query"},
		{"zero limit", map[string]any{"query": "x", "limit": 0}, "limit"},
		{"negative limit", map[string]any{"query": "x", "limit": -1}, "limit"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewWebSearch().Execute(context.Background(), mustJSON(t, tt.args), tc)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.sub) {
				t.Errorf("err = %v, want substring %q", err, tt.sub)
			}
		})
	}
}

func TestWebSearchCancellation(t *testing.T) {
	tc := allowAll(t.TempDir())
	tc.WebSearch = WebSearchSettings{
		Searcher: func(ctx context.Context, _ WebSearchQuery) ([]WebSearchHit, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewWebSearch().Execute(ctx, mustJSON(t, map[string]any{
		"query": "x",
	}), tc)
	if err == nil {
		t.Fatal("expected cancel error")
	}
}

func TestWebSearchTimeout(t *testing.T) {
	tc := allowAll(t.TempDir())
	tc.WebSearch = WebSearchSettings{
		Searcher: func(ctx context.Context, _ WebSearchQuery) ([]WebSearchHit, error) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(5 * time.Second):
				return nil, nil
			}
		},
	}
	start := time.Now()
	_, err := NewWebSearch().Execute(context.Background(), mustJSON(t, map[string]any{
		"query":   "x",
		"timeout": 0.15,
	}), tc)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected timeout")
	}
	if elapsed > 2*time.Second {
		t.Errorf("took %v, want quick timeout", elapsed)
	}
}

func TestWebSearchBraveHTTPBackend(t *testing.T) {
	// Local HTTP server fakes Brave JSON; tool uses baseURL override.
	var sawToken string
	var sawQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawToken = r.Header.Get("X-Subscription-Token")
		sawQuery = r.URL.Query().Get("q")
		if r.URL.Path != "/res/v1/web/search" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"web": {
				"results": [
					{
						"title": "Brave Hit",
						"url": "https://example.com/brave",
						"description": "From brave API",
						"age": "1 day ago"
					},
					{
						"title": "Other Host",
						"url": "https://other.example/x",
						"description": "skip me"
					}
				]
			}
		}`))
	}))
	t.Cleanup(srv.Close)

	// Allow loopback host for SSRF pre-check and use default transport for httptest.
	host := strings.TrimPrefix(strings.TrimPrefix(srv.URL, "https://"), "http://")
	if i := strings.Index(host, "/"); i >= 0 {
		host = host[:i]
	}
	if i := strings.Index(host, ":"); i >= 0 {
		host = host[:i]
	}
	allowWebfetchTestHost(t, host)
	prevTr := webfetchTestTransport
	webfetchTestTransport = http.DefaultTransport
	t.Cleanup(func() { webfetchTestTransport = prevTr })

	t.Setenv("BRAVE_API_KEY", "test-brave-key")
	tc := allowAll(t.TempDir())
	tc.WebSearch = WebSearchSettings{
		Provider:  "brave",
		APIKeyEnv: "BRAVE_API_KEY",
		BaseURL:   srv.URL,
	}
	res, err := NewWebSearch().Execute(context.Background(), mustJSON(t, map[string]any{
		"query":           "golang channels",
		"include_domains": []string{"example.com"},
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if sawToken != "test-brave-key" {
		t.Errorf("token = %q", sawToken)
	}
	if !strings.Contains(sawQuery, "golang channels") {
		t.Errorf("query = %q", sawQuery)
	}
	if !strings.Contains(res.Output, "Brave Hit") || !strings.Contains(res.Output, "https://example.com/brave") {
		t.Errorf("output = %s", res.Output)
	}
	if strings.Contains(res.Output, "other.example") {
		t.Fatal("include_domains should drop other.example")
	}
	if !strings.Contains(res.Output, "Provider: brave") {
		t.Errorf("provider line missing: %s", res.Output)
	}
}

func TestWebSearchNetworkAllowlist(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"web":{"results":[]}}`))
	}))
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(strings.TrimPrefix(srv.URL, "http://"), "https://")
	if i := strings.Index(host, ":"); i >= 0 {
		host = host[:i]
	}
	allowWebfetchTestHost(t, host)

	t.Setenv("BRAVE_API_KEY", "k")
	tc := &Context{
		WorkDir: t.TempDir(),
		Ask:     func(context.Context, AskRequest) error { return nil },
		// API host not on allowlist.
		NetworkAllow: []string{"api.search.brave.com"},
		WebSearch: WebSearchSettings{
			Provider: "brave",
			BaseURL:  srv.URL,
		},
	}
	_, err := NewWebSearch().Execute(context.Background(), mustJSON(t, map[string]any{
		"query": "x",
	}), tc)
	if err == nil {
		t.Fatal("want allowlist error")
	}
	if !strings.Contains(err.Error(), "allowlist") {
		t.Fatalf("err = %v", err)
	}
}

func TestWebSearchEmptyResults(t *testing.T) {
	tc := allowAll(t.TempDir())
	tc.WebSearch = WebSearchSettings{
		Searcher: func(context.Context, WebSearchQuery) ([]WebSearchHit, error) {
			return nil, nil
		},
	}
	res, err := NewWebSearch().Execute(context.Background(), mustJSON(t, map[string]any{
		"query": "zzzz-no-match",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "No results") {
		t.Errorf("output = %s", res.Output)
	}
	if !strings.Contains(res.Title, "0 results") {
		t.Errorf("title = %q", res.Title)
	}
}

func TestNormalizeDomainFilters(t *testing.T) {
	got := normalizeDomainFilters([]string{
		" GitHub.com ",
		"*.pkg.go.dev",
		"https://example.com/path",
		"",
		"GitHub.com",
	})
	want := []string{"github.com", "pkg.go.dev", "example.com"}
	if len(got) != len(want) {
		t.Fatalf("got %#v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d]=%q want %q", i, got[i], want[i])
		}
	}
}

func TestDomainFilterMatch(t *testing.T) {
	cases := []struct {
		host    string
		domains []string
		want    bool
	}{
		{"pkg.go.dev", []string{"pkg.go.dev"}, true},
		{"docs.github.com", []string{"github.com"}, true},
		{"evilgithub.com", []string{"github.com"}, false},
		{"example.com", []string{"other.com"}, false},
	}
	for _, tt := range cases {
		if got := domainFilterMatch(tt.host, tt.domains); got != tt.want {
			t.Errorf("domainFilterMatch(%q,%v)=%v want %v", tt.host, tt.domains, got, tt.want)
		}
	}
}

func TestFormatWebSearchOutputCitations(t *testing.T) {
	out := formatWebSearchOutput("q", "brave", "2026-01-01T00:00:00Z", []WebSearchHit{
		{Title: "A [B]", URL: "https://ex.com", Snippet: "s"},
	})
	if !strings.Contains(out, `[A \[B\]](https://ex.com)`) {
		t.Errorf("cite line = %s", out)
	}
}

func TestWebSearchContract(t *testing.T) {
	c := NewWebSearch().(interface{ Contract() Contract }).Contract()
	if c.SideEffect != SideEffectNetwork {
		t.Errorf("sideEffect = %s", c.SideEffect)
	}
	if c.Idempotency != IdempotencySafeRetry {
		t.Errorf("idempotency = %s", c.Idempotency)
	}
}
