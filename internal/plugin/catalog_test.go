package plugin

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseCatalog_ValidAndRejects(t *testing.T) {
	raw := `{
  "schemaVersion": 1,
  "registry": "https://example.com/strike-plugins",
  "packages": [{
    "id": "acme.pack",
    "name": "Acme",
    "description": "demo",
    "versions": [{
      "version": "1.0.0",
      "url": "https://example.com/acme-1.0.0.tar.gz",
      "digest": "sha256:` + strings.Repeat("ab", 32) + `",
      "capabilities": ["agents"],
      "strike": {"min": "0.2.0"}
    }]
  }]
}`
	c, err := ParseCatalog([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if c.Packages[0].ID != "acme.pack" {
		t.Fatalf("%+v", c)
	}
	aps := `{
  "schemaVersion": 1,
  "registry": "https://example.com/strike-plugins",
  "packages": [{
    "id": "acme.example",
    "name": "Acme Example",
    "versions": [{
      "version": "1.0.0",
      "url": "https://example.com/acme-1.0.0.tar.gz",
      "digest": "sha256:` + strings.Repeat("ab", 32) + `",
      "$schema": "agent-plugins:1.0.0"
    }]
  }]
}`
	got, err := ParseCatalog([]byte(aps))
	if err != nil {
		t.Fatal(err)
	}
	if got.Packages[0].Versions[0].Schema != CatalogAPSSchema {
		t.Fatalf("schema=%q", got.Packages[0].Versions[0].Schema)
	}
	legacySchema := `{
  "schemaVersion": 1,
  "packages": [{
    "id": "acme.pack",
    "versions": [{
      "version": "1.0.0",
      "url": "https://example.com/a.tar.gz",
      "digest": "sha256:` + strings.Repeat("ab", 32) + `",
      "manifestSchema": 1
    }]
  }]
}`
	if _, err := ParseCatalog([]byte(legacySchema)); err != nil {
		t.Fatalf("legacy manifestSchema must remain optional: %v", err)
	}
	if _, err := ParseCatalog([]byte(`{
  "schemaVersion": 1,
  "packages": [{
    "id": "acme.pack",
    "versions": [{
      "version": "1.0.0",
      "url": "https://example.com/a.tar.gz",
      "digest": "sha256:` + strings.Repeat("ab", 32) + `",
      "$schema": "not-a-real-schema"
    }]
  }]
}`)); err == nil {
		t.Fatal("expected unsupported $schema")
	}
	// Unknown field rejected.
	if _, err := ParseCatalog([]byte(`{"schemaVersion":1,"packages":[],"extra":true}`)); err == nil {
		t.Fatal("expected unknown field error")
	}
	// Future schema rejected.
	if _, err := ParseCatalog([]byte(`{"schemaVersion":99,"packages":[{"id":"acme.pack","versions":[{"version":"1.0.0","url":"https://x/a","digest":"sha256:` + strings.Repeat("a", 64) + `"}]}]}`)); err == nil {
		t.Fatal("expected schema version error")
	}
}

func TestCatalogSearchAndLatest(t *testing.T) {
	c := Catalog{
		SchemaVersion: 1,
		Registry:      "https://reg.example",
		Packages: []CatalogPackage{
			{
				ID: "acme.pack", Name: "Acme", Description: "review helpers",
				Versions: []CatalogVersion{
					{Version: "1.0.0", URL: "https://x/1", Digest: "sha256:" + strings.Repeat("a", 64)},
					{Version: "1.2.0", URL: "https://x/2", Digest: "sha256:" + strings.Repeat("b", 64)},
					{Version: "1.1.0", URL: "https://x/3", Digest: "sha256:" + strings.Repeat("c", 64)},
				},
			},
			{
				ID: "other.theme", Name: "Theme",
				Versions: []CatalogVersion{
					{Version: "0.1.0", URL: "https://x/t", Digest: "sha256:" + strings.Repeat("d", 64)},
				},
			},
		},
	}
	v, err := c.Packages[0].LatestVersion()
	if err != nil || v.Version != "1.2.0" {
		t.Fatalf("latest: %v %+v", err, v)
	}
	hits := c.Search("review")
	if len(hits) != 1 || hits[0].ID != "acme.pack" || hits[0].Version.Version != "1.2.0" {
		t.Fatalf("search: %+v", hits)
	}
	hits = c.Search("")
	if len(hits) != 2 {
		t.Fatalf("all: %+v", hits)
	}
}

func TestSanitizeArchivePath_Traversal(t *testing.T) {
	bad := []string{"../etc/passwd", "/etc/passwd", `..\windows`, "foo/../../bar", "C:/windows"}
	for _, p := range bad {
		if _, err := sanitizeArchivePath(p); err == nil {
			t.Fatalf("expected reject %q", p)
		}
	}
	ok, err := sanitizeArchivePath("agents/a.md")
	if err != nil || ok != "agents/a.md" {
		t.Fatalf("got %q %v", ok, err)
	}
}

func TestExtractArchive_ZipSlipRejected(t *testing.T) {
	// Craft zip with ../ escape.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("../escape.md")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("nope")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	if err := extractArchive(buf.Bytes(), dest); err == nil {
		t.Fatal("expected zip-slip rejection")
	}
}

func TestExtractArchive_TarGzRoundTrip(t *testing.T) {
	files := map[string]string{
		"plugin.json":          apsManifest("acme.pack"),
		"skills/demo/SKILL.md": validSkillMD("demo"),
	}
	data := mustTarGz(t, files)
	dest := t.TempDir()
	if err := extractArchive(data, dest); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadManifest(dest); err != nil {
		t.Fatal(err)
	}
}

func TestExtractArchive_FlattenSingleRoot(t *testing.T) {
	files := map[string]string{
		"acme.pack/plugin.json":          apsManifest("acme.pack"),
		"acme.pack/skills/demo/SKILL.md": validSkillMD("demo"),
	}
	data := mustTarGz(t, files)
	dest := t.TempDir()
	if err := extractArchive(data, dest); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadManifest(dest); err != nil {
		t.Fatal(err)
	}
}

func TestCatalogInstall_PinsVersionAndDigest(t *testing.T) {
	bundle := map[string]string{
		"plugin.json":          apsManifest("acme.catalog"),
		"skills/demo/SKILL.md": validSkillMD("from-catalog"),
	}
	// Bump version in manifest to 1.0.0 (manifest helper uses 1.0.0 already).
	archive := mustTarGz(t, bundle)
	sum := sha256.Sum256(archive)
	dig := "sha256:" + hex.EncodeToString(sum[:])
	contentDig, err := contentDigestOfMap(bundle)
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/catalog.json"):
			cat := Catalog{
				SchemaVersion: 1,
				Registry:      "http://" + r.Host + "/strike-plugins",
				Packages: []CatalogPackage{{
					ID:   "acme.catalog",
					Name: "Catalog Pack",
					Versions: []CatalogVersion{{
						Version:       "1.0.0",
						URL:           "http://" + r.Host + "/artifacts/acme-1.0.0.tar.gz",
						Digest:        dig,
						ContentDigest: contentDig,
						Capabilities:  []string{"skills"},
					}},
				}},
			}
			_ = json.NewEncoder(w).Encode(cat)
		case strings.Contains(r.URL.Path, "acme-1.0.0.tar.gz"):
			_, _ = w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	home := t.TempDir()
	res, err := Install(context.Background(), InstallOptions{
		Scope:           ScopeGlobal,
		GlobalRoot:      filepath.Join(home, ".strike"),
		CatalogRegistry: srv.URL + "/strike-plugins",
		CatalogPackage:  "acme.catalog",
		CatalogVersion:  "1.0.0",
		HTTPClient:      srv.Client(),
		StrikeVersion:   "0.2.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Source.Type != SourceCatalog || res.Source.Version != "1.0.0" {
		t.Fatalf("source: %+v", res.Source)
	}
	if res.Source.Digest != dig {
		t.Fatalf("artifact digest: %s want %s", res.Source.Digest, dig)
	}
	if res.Digest != contentDig {
		t.Fatalf("content digest: %s want %s", res.Digest, contentDig)
	}
	lf, err := ReadLockfile(filepath.Join(home, ".strike", "plugins.lock.json"))
	if err != nil {
		t.Fatal(err)
	}
	e := lf.Plugins["acme.catalog"]
	if e.Source == nil || e.Source.Type != SourceCatalog || e.Trust != nil {
		t.Fatalf("lock entry must pin catalog provenance and not grant trust: %+v", e)
	}
	if e.Source.Registry == "" || e.Source.Package != "acme.catalog" || e.Source.URL == "" {
		t.Fatalf("incomplete provenance: %+v", e.Source)
	}
}

func TestCatalogInstall_APSTarball(t *testing.T) {
	bundle := testdataFiles(t, "aps/example-pack")
	archive := mustTarGz(t, bundle)
	sum := sha256.Sum256(archive)
	dig := "sha256:" + hex.EncodeToString(sum[:])
	contentDig, err := contentDigestOfMap(bundle)
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/catalog.json"):
			cat := Catalog{
				SchemaVersion: 1,
				Registry:      "http://" + r.Host + "/strike-plugins",
				Packages: []CatalogPackage{{
					ID:   "acme.example",
					Name: "Acme Example",
					Versions: []CatalogVersion{{
						Version:       "1.0.0",
						URL:           "http://" + r.Host + "/artifacts/acme-example-1.0.0.tar.gz",
						Digest:        dig,
						ContentDigest: contentDig,
						Schema:        CatalogAPSSchema,
						Capabilities:  []string{"skills", "mcp.stdio"},
					}},
				}},
			}
			_ = json.NewEncoder(w).Encode(cat)
		case strings.Contains(r.URL.Path, "acme-example-1.0.0.tar.gz"):
			_, _ = w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	home := t.TempDir()
	res, err := Install(context.Background(), InstallOptions{
		Scope:           ScopeGlobal,
		GlobalRoot:      filepath.Join(home, ".strike"),
		CatalogRegistry: srv.URL + "/strike-plugins",
		CatalogPackage:  "acme.example",
		CatalogVersion:  "1.0.0",
		HTTPClient:      srv.Client(),
		StrikeVersion:   "0.2.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ID != "acme.example" || res.Digest != contentDig {
		t.Fatalf("install: %+v digest want %s", res, contentDig)
	}
	if res.Source.Digest != dig {
		t.Fatalf("artifact digest: %s want %s", res.Source.Digest, dig)
	}
	loaded, diags := LoadOne(res.Root, ScopeGlobal, "0.2.0")
	if loaded == nil {
		t.Fatalf("loader failed: %v", diags)
	}
	if loaded.Manifest.Format != FormatAPS || loaded.Manifest.ID != "acme.example" {
		t.Fatalf("manifest: %+v", loaded.Manifest)
	}
	if len(loaded.Skills) != 1 || loaded.MCPCount != 1 {
		t.Fatalf("skills=%d mcp=%d diags=%v", len(loaded.Skills), loaded.MCPCount, diags)
	}
}

func TestCatalogInstall_DigestMismatchPreservesNothing(t *testing.T) {
	archive := mustTarGz(t, map[string]string{
		"plugin.json": manifest("acme.bad", `{"agents":[{"path":"agents/a.md"}]}`),
		"agents/a.md": validAgentMD("a"),
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/catalog.json") {
			cat := Catalog{
				SchemaVersion: 1,
				Packages: []CatalogPackage{{
					ID: "acme.bad",
					Versions: []CatalogVersion{{
						Version: "1.0.0",
						URL:     "http://" + r.Host + "/a.tar.gz",
						Digest:  "sha256:" + strings.Repeat("0", 64), // wrong
					}},
				}},
			}
			_ = json.NewEncoder(w).Encode(cat)
			return
		}
		_, _ = w.Write(archive)
	}))
	t.Cleanup(srv.Close)

	home := t.TempDir()
	_, err := Install(context.Background(), InstallOptions{
		Scope: ScopeGlobal, GlobalRoot: filepath.Join(home, ".strike"),
		CatalogRegistry: srv.URL, CatalogPackage: "acme.bad",
		HTTPClient: srv.Client(), StrikeVersion: "0.2.0",
	})
	if err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("want digest mismatch, got %v", err)
	}
	if entries, err := os.ReadDir(filepath.Join(home, ".strike", "plugins")); err == nil {
		for _, e := range entries {
			if !strings.HasPrefix(e.Name(), ".") {
				t.Fatalf("partial install: %s", e.Name())
			}
		}
	}
}

func TestCatalogInstall_FailedValidationPreservesPrior(t *testing.T) {
	home := t.TempDir()
	global := filepath.Join(home, ".strike")
	// Install good local first.
	src := testBundle(t, t.TempDir(), "acme.pack")
	res, err := Install(context.Background(), InstallOptions{
		Scope: ScopeGlobal, GlobalRoot: global, LocalPath: src, StrikeVersion: "0.2.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	priorDigest := res.Digest
	// Seed trust on lockfile to ensure failed replace keeps it.
	_ = WithLockfileLock(filepath.Join(global, "plugins.lock.json"), func(lf Lockfile) (Lockfile, bool, error) {
		e := lf.Plugins["acme.pack"]
		e.Trust = &TrustRecord{Digest: priorDigest, TrustedAt: "2026-01-01T00:00:00Z"}
		return setLockEntry(lf, "acme.pack", e), false, nil
	})

	// Bad catalog artifact: valid archive but invalid plugin (missing agent file).
	bad := mustTarGz(t, map[string]string{
		"plugin.json": `{
  "$schema": "https://agent-plugins.org/schemas/99.0.0/plugin.schema.json",
  "name": "acme.pack",
  "version": "2.0.0"
}`,
	})
	sum := sha256.Sum256(bad)
	dig := "sha256:" + hex.EncodeToString(sum[:])
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "catalog.json") {
			_ = json.NewEncoder(w).Encode(Catalog{
				SchemaVersion: 1,
				Packages: []CatalogPackage{{
					ID: "acme.pack",
					Versions: []CatalogVersion{{
						Version: "2.0.0",
						URL:     "http://" + r.Host + "/bad.tar.gz",
						Digest:  dig,
					}},
				}},
			})
			return
		}
		_, _ = w.Write(bad)
	}))
	t.Cleanup(srv.Close)

	_, err = Install(context.Background(), InstallOptions{
		Scope: ScopeGlobal, GlobalRoot: global,
		CatalogRegistry: srv.URL, CatalogPackage: "acme.pack", CatalogVersion: "2.0.0",
		HTTPClient: srv.Client(), StrikeVersion: "0.2.0", Force: true,
	})
	if err == nil {
		t.Fatal("expected validation failure")
	}
	// Prior install intact.
	if _, err := os.Stat(res.Root); err != nil {
		t.Fatal("prior version must be preserved:", err)
	}
	lf, _ := ReadLockfile(filepath.Join(global, "plugins.lock.json"))
	e := lf.Plugins["acme.pack"]
	if e.Digest != priorDigest || e.Version != "1.0.0" {
		t.Fatalf("lockfile changed on failed install: %+v", e)
	}
	if e.Trust == nil || e.Trust.Digest != priorDigest {
		t.Fatalf("trust must remain on failed update: %+v", e.Trust)
	}
}

func TestUpdate_InvalidatesTrustOnExecutableChange(t *testing.T) {
	home := t.TempDir()
	global := filepath.Join(home, ".strike")

	v1files := map[string]string{
		"plugin.json": `{
  "$schema": "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json",
  "name": "acme.tools",
  "version": "1.0.0"
}`,
		"skills/demo/SKILL.md": validSkillMD("demo"),
	}
	v2files := map[string]string{
		"plugin.json": `{
  "$schema": "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json",
  "name": "acme.tools",
  "version": "2.0.0"
}`,
		"skills/demo/SKILL.md": validSkillMD("demo"),
		"mcp.json": `{
  "$schema": "https://agent-plugins.org/schemas/1.0.0/mcp.schema.json",
  "mcpServers": {
    "lint": {
      "type": "stdio",
      "command": "./bin/lint",
      "args": ["--serve"],
      "env": {"TOK": "secret://env/TOK"}
    }
  }
}`,
		"bin/lint": "#!/bin/sh\n",
	}
	a1 := mustTarGz(t, v1files)
	a2 := mustTarGz(t, v2files)
	d1 := "sha256:" + hex.EncodeToString(sum256(a1))
	d2 := "sha256:" + hex.EncodeToString(sum256(a2))
	cd1, _ := contentDigestOfMap(v1files)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "catalog.json"):
			_ = json.NewEncoder(w).Encode(Catalog{
				SchemaVersion: 1,
				Registry:      "http://" + r.Host,
				Packages: []CatalogPackage{{
					ID: "acme.tools",
					Versions: []CatalogVersion{
						{Version: "1.0.0", URL: "http://" + r.Host + "/v1.tar.gz", Digest: d1, ContentDigest: cd1, Capabilities: []string{"skills"}},
						{Version: "2.0.0", URL: "http://" + r.Host + "/v2.tar.gz", Digest: d2, Capabilities: []string{"skills", "mcp.stdio"}},
					},
				}},
			})
		case strings.HasSuffix(r.URL.Path, "v1.tar.gz"):
			_, _ = w.Write(a1)
		case strings.HasSuffix(r.URL.Path, "v2.tar.gz"):
			_, _ = w.Write(a2)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	res, err := Install(context.Background(), InstallOptions{
		Scope: ScopeGlobal, GlobalRoot: global,
		CatalogRegistry: srv.URL, CatalogPackage: "acme.tools", CatalogVersion: "1.0.0",
		HTTPClient: srv.Client(), StrikeVersion: "0.2.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Grant trust as #728 would.
	_ = WithLockfileLock(filepath.Join(global, "plugins.lock.json"), func(lf Lockfile) (Lockfile, bool, error) {
		e := lf.Plugins["acme.tools"]
		e.Trust = &TrustRecord{Digest: res.Digest, Capabilities: []string{"mcp.stdio"}, TrustedAt: nowRFC3339()}
		return setLockEntry(lf, "acme.tools", e), false, nil
	})

	// Preview should show capability/exec changes once we have manifests; after update trust cleared.
	up, err := Update(context.Background(), UpdateOptions{
		ID: "acme.tools", Scope: ScopeGlobal, GlobalRoot: global,
		Registry: srv.URL, Confirm: true, HTTPClient: srv.Client(), StrikeVersion: "0.2.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if up.Install.Version != "2.0.0" {
		t.Fatalf("version: %s", up.Install.Version)
	}
	if !up.Review.ExecutableChanged && !up.Review.TrustInvalidated {
		t.Fatalf("expected executable change and trust invalidation: %+v", up.Review)
	}
	lf, _ := ReadLockfile(filepath.Join(global, "plugins.lock.json"))
	if lf.Plugins["acme.tools"].Trust != nil {
		t.Fatal("trust must be cleared after executable-changing update")
	}
	// Prior working version replaced only after success — new root exists.
	if _, err := os.Stat(up.Install.Root); err != nil {
		t.Fatal(err)
	}
}

func TestUpdate_RequiresConfirm(t *testing.T) {
	_, err := Update(context.Background(), UpdateOptions{ID: "acme.pack", Confirm: false})
	if err == nil || !strings.Contains(err.Error(), "confirmation") {
		t.Fatalf("got %v", err)
	}
}

func TestBuildUpdateReview_ExecutableDiffNoSecrets(t *testing.T) {
	old := InstalledPlugin{
		ID:      "acme.tools",
		Version: "1.0.0",
		Digest:  "sha256:" + strings.Repeat("a", 64),
		Trust:   &TrustRecord{Digest: "sha256:" + strings.Repeat("a", 64)},
		Manifest: &Manifest{
			ID: "acme.tools", Version: "1.0.0",
			Contributions: Contributions{
				MCP: []json.RawMessage{json.RawMessage(`{"name":"db","transport":"stdio","command":"bin/db","env":{"SECRET":"never-print-me"}}`)},
			},
		},
	}
	newMan := Manifest{
		ID: "acme.tools", Version: "2.0.0",
		Capabilities: []string{"mcp.stdio"},
		Contributions: Contributions{
			MCP: []json.RawMessage{json.RawMessage(`{"name":"db","transport":"stdio","command":"bin/db2","env":{"SECRET":"also-secret","OTHER":"x"}}`)},
		},
	}
	src := SourceIdentity{Type: SourceCatalog, Registry: "https://r", Package: "acme.tools", Version: "2.0.0", URL: "https://r/a", Digest: "sha256:" + strings.Repeat("b", 64)}
	rev := BuildUpdateReview(old, newMan, src, "sha256:"+strings.Repeat("c", 64), "")
	text := rev.Format()
	if strings.Contains(text, "never-print-me") || strings.Contains(text, "also-secret") {
		t.Fatalf("leaked secret in review:\n%s", text)
	}
	if !rev.ExecutableChanged || !rev.TrustInvalidated {
		t.Fatalf("review: %+v", rev)
	}
	if !strings.Contains(text, "envKeys=") {
		t.Fatalf("expected env keys in diff:\n%s", text)
	}
}

func TestDownload_BoundsAndScheme(t *testing.T) {
	if err := validateHTTPURL("file:///etc/passwd"); err == nil {
		t.Fatal("file scheme")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte("x"), 100))
	}))
	t.Cleanup(srv.Close)
	_, err := downloadBytes(context.Background(), srv.Client(), srv.URL, 10)
	if err == nil || !strings.Contains(err.Error(), "exceeds limit") {
		t.Fatalf("want size limit error, got %v", err)
	}
}

func TestCatalogMetadataCannotGrantTrust(t *testing.T) {
	// Even if catalog JSON tried to include trust-like fields, ParseCatalog rejects unknowns
	// and Install never sets Trust from metadata.
	raw := `{
  "schemaVersion": 1,
  "packages": [{
    "id": "acme.pack",
    "versions": [{
      "version": "1.0.0",
      "url": "https://example.com/a.tar.gz",
      "digest": "sha256:` + strings.Repeat("a", 64) + `",
      "trust": true
    }]
  }]
}`
	if _, err := ParseCatalog([]byte(raw)); err == nil {
		t.Fatal("catalog must reject unknown trust field on version")
	}
}

func mustTarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		name = filepath.ToSlash(name)
		hdr := &tar.Header{
			Name: name,
			Mode: 0o644,
			Size: int64(len(body)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(tw, body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sum256(b []byte) []byte {
	s := sha256.Sum256(b)
	return s[:]
}

// contentDigestOfMap builds a temp tree and returns ComputeDigest.
func contentDigestOfMap(files map[string]string) (string, error) {
	dir, err := os.MkdirTemp("", "strike-dig-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			return "", err
		}
	}
	// Flatten single root if needed for digest of plugin root shape used in tests.
	if _, _, err := ReadManifest(dir); err != nil {
		entries, _ := os.ReadDir(dir)
		if len(entries) == 1 && entries[0].IsDir() {
			return ComputeDigest(filepath.Join(dir, entries[0].Name()))
		}
		return "", fmt.Errorf("no manifest: %w", err)
	}
	return ComputeDigest(dir)
}
