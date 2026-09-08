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
	"time"
)

// Default first-slice app set: varied, single-service.
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
	Version     string       `json:"version"`
	Variants    []catVariant `json:"variants"`
}

type catalogFile struct {
	CatalogName  string     `json:"catalog_name"`
	FjordVersion string     `json:"fjord_version"`
	Maintainer   string     `json:"maintainer"`
	Icon         string     `json:"icon,omitempty"` // catalog branding, source-relative like app icons
	Generated    string     `json:"generated"`
	Apps         []catEntry `json:"apps"`
}

func main() {
	reposDir := flag.String("repos-dir", "..", "directory containing the image repos (each a compose.yaml + .daemonless/config.yaml)")
	outDir := flag.String("out", ".", "output catalog directory")
	appsCSV := flag.String("apps", "all", "comma-separated app ids to derive, or \"all\" to scan every repo")
	versionsPath := flag.String("versions", "", "optional versions JSON; when omitted, versions come from each repo's sbom.json")
	sourcesPath := flag.String("sources", "sources.yaml", "catalog config (branding + sources); absent -> built-in daemonless default")
	flag.Parse()

	cfg, err := loadConfig(*sourcesPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(1)
	}

	manifestDir := filepath.Join(*outDir, "manifests")
	iconDir := filepath.Join(*outDir, "icons")
	for _, d := range []string{manifestDir, iconDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			fmt.Fprintln(os.Stderr, "mkdir:", err)
			os.Exit(1)
		}
	}

	cat := catalogFile{
		CatalogName:  cfg.Catalog.Name,
		FjordVersion: "0.1",
		Maintainer:   cfg.Catalog.Maintainer,
		Icon:         cfg.Catalog.Icon,
		Generated:    time.Now().UTC().Format(time.RFC3339),
	}
	var skipped []string

	for _, src := range cfg.Sources {
		prov, err := providerFor(src, *reposDir, *versionsPath, *appsCSV)
		if err != nil {
			fmt.Fprintf(os.Stderr, "source %q: %v\n", src.Name, err)
			os.Exit(1)
		}
		refs, err := prov.Discover()
		if err != nil {
			fmt.Fprintf(os.Stderr, "source %q discover: %v\n", src.Name, err)
			os.Exit(1)
		}
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
			fmt.Printf("derived  %-14s (%d vars, %d variants)\n", ref.ID, da.Vars, len(da.Entry.Variants))
		}
	}

	sort.Slice(cat.Apps, func(i, j int) bool { return cat.Apps[i].Name < cat.Apps[j].Name })
	data, _ := json.MarshalIndent(cat, "", "  ")
	if err := os.WriteFile(filepath.Join(*outDir, "catalog.json"), append(data, '\n'), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "write catalog:", err)
		os.Exit(1)
	}

	fmt.Printf("\n%d derived, %d skipped\n", len(cat.Apps), len(skipped))
	for _, s := range skipped {
		fmt.Println("  skip", s)
	}
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}

// scanRepos returns every repo dir under root that has a compose.yaml.
func scanRepos(root string) []string {
	var out []string
	entries, _ := os.ReadDir(root)
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
