package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// tested: names a file beside sources.yaml, wherever the build runs from.
func TestTestedRelativeToSources(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sources.yaml")
	os.WriteFile(p, []byte("sources:\n  - name: linuxserver\n    provider: linuxserver-api\n    tested: tested-linuxserver.json\n"), 0o644)
	c, err := loadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Sources[0].Tested; got != filepath.Join(dir, "tested-linuxserver.json") {
		t.Errorf("tested %q", got)
	}
}

func TestRunSourcesSingleSourceKeepsOther(t *testing.T) {
	dir := t.TempDir()
	repos := filepath.Join(dir, "repos", "radarr")
	if err := os.MkdirAll(repos, 0o755); err != nil {
		t.Fatal(err)
	}
	compose := "name: radarr\nx-daemonless:\n  title: Radarr\n  category: Media\nservices:\n  radarr:\n    image: ghcr.io/daemonless/radarr:latest\n"
	if err := os.WriteFile(filepath.Join(repos, "compose.yaml"), []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(dir, "out")
	lsDir := filepath.Join(out, "linuxserver")
	if err := os.MkdirAll(lsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	seeded := `{"catalog_name":"Daemonless Apps","fjord_version":"0.1","apps":[{"id":"sonarr","name":"Sonarr"},{"id":"plex","name":"Plex"}]}`
	if err := os.WriteFile(filepath.Join(lsDir, "catalog.json"), []byte(seeded), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := Config{
		Catalog: CatalogMeta{Name: "Daemonless Apps"},
		Sources: []Source{
			{Name: "daemonless", Provider: "local"},
			{Name: "linuxserver", Provider: "unknown-should-not-run"},
		},
	}

	if err := runSources(cfg, out, filepath.Join(dir, "repos"), "", "all", "missing"); err == nil {
		t.Fatal("expected error for unknown source name")
	}

	if err := runSources(cfg, out, filepath.Join(dir, "repos"), "", "all", "daemonless"); err != nil {
		t.Fatalf("runSources: %v", err)
	}

	var idx []sourceIndex
	b, err := os.ReadFile(filepath.Join(out, "sources.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &idx); err != nil {
		t.Fatal(err)
	}
	if len(idx) != 2 || idx[0].Name != "daemonless" || idx[0].Apps != 1 || idx[1].Name != "linuxserver" || idx[1].Apps != 2 {
		t.Fatalf("unexpected sources.json: %+v", idx)
	}
}
