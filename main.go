// Command fjord-catalog derives x-fjord manifests + catalog.json from a
// directory of image repos -- each a compose.yaml carrying x-daemonless metadata
// plus an optional .daemonless/config.yaml. Point --repos-dir at them; nothing
// about a specific registry, path, or app set is assumed.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type catVariant struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Default bool   `json:"default,omitempty"`
	Image   string `json:"image"`
	Version string `json:"version,omitempty"`
}

type catEntry struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Category    string       `json:"category"`
	Class       string       `json:"class"`
	Icon        string       `json:"icon"`
	Description string       `json:"description,omitempty"`
	UpstreamURL string       `json:"upstream_url,omitempty"`
	WebURL      string       `json:"web_url,omitempty"`
	Image       string       `json:"image,omitempty"`
	ManifestURL string       `json:"manifest_url"`
	Version     string       `json:"version,omitempty"`
	Variants    []catVariant `json:"variants"`
}

type catalogFile struct {
	CatalogName  string     `json:"catalog_name"`
	FjordVersion string     `json:"fjord_version"`
	Maintainer   string     `json:"maintainer"`
	Icon         string     `json:"icon,omitempty"` // catalog branding, relative to this catalog's base URL like app icons
	Generated    string     `json:"generated"`
	Apps         []catEntry `json:"apps"`
}

// sourceIndex is <out>/sources.json: one entry per published catalog, so a
// consumer given the base URL can find every source without guessing names.
type sourceIndex struct {
	Name    string `json:"name"`
	Catalog string `json:"catalog"` // relative path to that source's catalog.json
	Apps    int    `json:"apps"`
}

func main() {
	reposDir := flag.String("repos-dir", "..", "directory containing the image repos (each a compose.yaml + .daemonless/config.yaml)")
	outDir := flag.String("out", "out", "output root; each source is written to <out>/<source>/ plus <out>/sources.json")
	appsCSV := flag.String("apps", "all", "comma-separated app ids to derive, or \"all\" to scan every repo")
	versionsPath := flag.String("versions", "", "optional versions JSON; when omitted, versions come from each repo's sbom.json")
	sourcesPath := flag.String("sources", "sources.yaml", "catalog config (branding + sources); absent -> built-in daemonless default")
	flag.Parse()

	cfg, err := loadConfig(*sourcesPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(1)
	}

	// One catalog per source, each under <out>/<source>/ with its own
	// catalog.json, manifests/ and icons/, so every source is independently
	// publishable and refreshable; <out>/sources.json lists them.
	var index []sourceIndex
	total, failed := 0, 0
	for _, src := range cfg.Sources {
		n, err := buildSource(src, cfg.Catalog, *outDir, *reposDir, *versionsPath, *appsCSV)
		if err != nil {
			fmt.Fprintf(os.Stderr, "source %q: %v\n", src.Name, err)
			failed++
			continue
		}
		index = append(index, sourceIndex{Name: src.Name, Catalog: src.Name + "/catalog.json", Apps: n})
		total += n
	}
	if failed > 0 || total == 0 {
		// Never publish an empty or partial catalog as if it were real: a
		// rate-limited clone or a typo'd --repos-dir must fail the run.
		fmt.Fprintf(os.Stderr, "\n%d apps across %d sources (%d sources failed) -- refusing to write an empty/partial index\n", total, len(index), failed)
		os.Exit(1)
	}
	data, _ := json.MarshalIndent(index, "", "  ")
	if err := os.WriteFile(filepath.Join(*outDir, "sources.json"), append(data, '\n'), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "write sources.json:", err)
		os.Exit(1)
	}
	fmt.Printf("\n%d apps across %d source(s) -> %s\n", total, len(index), *outDir)
}

// buildSource derives one source into <out>/<name>/ and returns its app count.
func buildSource(src Source, top CatalogMeta, outRoot, reposDir, versionsPath, appsCSV string) (int, error) {
	outDir := filepath.Join(outRoot, src.Name)
	manifestDir := filepath.Join(outDir, "manifests")
	iconDir := filepath.Join(outDir, "icons")
	for _, d := range []string{manifestDir, iconDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return 0, fmt.Errorf("mkdir: %w", err)
		}
	}
	meta := src.meta(top)
	cat := catalogFile{
		CatalogName:  meta.Name,
		FjordVersion: "0.1",
		Maintainer:   meta.Maintainer,
		Icon:         meta.Icon,
		Generated:    time.Now().UTC().Format(time.RFC3339),
	}
	prov, err := providerFor(src, reposDir, versionsPath, appsCSV)
	if err != nil {
		return 0, err
	}
	refs, err := prov.Discover()
	if err != nil {
		return 0, fmt.Errorf("discover: %w", err)
	}
	var skipped []string
	for _, ref := range refs {
		da, err := prov.Derive(ref)
		if err != nil {
			skipped = append(skipped, ref.ID+": "+err.Error())
			continue
		}
		if err := os.WriteFile(filepath.Join(manifestDir, ref.ID+".yaml"), []byte(da.ManifestYAML), 0o644); err != nil {
			skipped = append(skipped, ref.ID+": write "+err.Error())
			continue
		}
		if da.IconSrc != "" {
			if err := copyFile(da.IconSrc, filepath.Join(iconDir, ref.ID+filepath.Ext(da.IconSrc))); err != nil {
				fmt.Fprintf(os.Stderr, "  warn %s: copy logo: %v\n", ref.ID, err)
			}
		}
		cat.Apps = append(cat.Apps, da.Entry)
		fmt.Printf("%-12s derived  %-14s (%d vars, %d variants)\n", src.Name, ref.ID, da.Vars, len(da.Entry.Variants))
	}
	// Stable, case-insensitive: "code-server" sorts with the Cs, not after
	// every capitalized name. Then id, so shared display names keep a fixed order.
	sort.SliceStable(cat.Apps, func(i, j int) bool {
		a, b := strings.ToLower(cat.Apps[i].Name), strings.ToLower(cat.Apps[j].Name)
		if a != b {
			return a < b
		}
		return cat.Apps[i].ID < cat.Apps[j].ID
	})
	if len(cat.Apps) == 0 {
		return 0, fmt.Errorf("derived no apps (repos dir empty or unreadable?)")
	}
	data, _ := json.MarshalIndent(cat, "", "  ")
	if err := os.WriteFile(filepath.Join(outDir, "catalog.json"), append(data, '\n'), 0o644); err != nil {
		return 0, fmt.Errorf("write catalog: %w", err)
	}
	fmt.Printf("%-12s %d derived, %d skipped\n", src.Name, len(cat.Apps), len(skipped))
	for _, s := range skipped {
		fmt.Println("  skip", s)
	}
	return len(cat.Apps), nil
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}

// scanRepos returns every repo dir under root that has a compose.yaml. An
// unreadable root yields nothing, which buildSource turns into an error.
func scanRepos(root string) []string {
	var out []string
	entries, err := os.ReadDir(root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  warn: read %s: %v\n", root, err)
		return nil
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, e.Name(), "compose.yaml")); err == nil {
			out = append(out, e.Name())
		}
	}
	return out
}

// loadVersions parses a versions file into per-app, per-variant version data,
// handling both the simple ({pkg,pkg-latest,upstream}) and multi-version
// ({variants:{...}}) shapes. Missing/unreadable file -> empty (no versions).
func loadVersions(path string) map[string]*appVersions {
	out := map[string]*appVersions{}
	b, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	var f struct {
		Services map[string]json.RawMessage `json:"services"`
	}
	if json.Unmarshal(b, &f) != nil {
		return out
	}
	for id, raw := range f.Services {
		var probe map[string]json.RawMessage
		json.Unmarshal(raw, &probe)
		if _, isMulti := probe["variants"]; isMulti {
			var m struct {
				Variants map[string]map[string]string `json:"variants"`
			}
			json.Unmarshal(raw, &m)
			out[id] = &appVersions{multi: m.Variants}
		} else {
			var s map[string]string
			json.Unmarshal(raw, &s)
			out[id] = &appVersions{simple: s}
		}
	}
	return out
}
