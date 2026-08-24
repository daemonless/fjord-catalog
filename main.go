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
	Generated    string     `json:"generated"`
	Apps         []catEntry `json:"apps"`
}

func main() {
	reposDir := flag.String("repos-dir", "..", "directory containing the image repos (each a compose.yaml + .daemonless/config.yaml)")
	outDir := flag.String("out", ".", "output catalog directory")
	appsCSV := flag.String("apps", "all", "comma-separated app ids to derive, or \"all\" to scan every repo")
	versionsPath := flag.String("versions", "", "optional versions JSON; when omitted, versions come from each repo's sbom.json")
	flag.Parse()

	var apps []string
	if *appsCSV == "all" {
		apps = scanRepos(*reposDir)
	} else {
		apps = strings.Split(*appsCSV, ",")
	}
	var versions map[string]*appVersions
	if *versionsPath != "" {
		versions = loadVersions(*versionsPath)
	} else {
		versions = loadSbomVersions(*reposDir, apps)
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
		CatalogName:  "Daemonless Apps",
		FjordVersion: "0.1",
		Maintainer:   "https://daemonless.io",
		Generated:    time.Now().UTC().Format(time.RFC3339),
	}
	var skipped []string

	for _, id := range apps {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		repo := filepath.Join(*reposDir, id)
		composeBytes, err := os.ReadFile(filepath.Join(repo, "compose.yaml"))
		if err != nil {
			skipped = append(skipped, id+": no compose.yaml")
			continue
		}
		configBytes, _ := os.ReadFile(filepath.Join(repo, ".daemonless/config.yaml"))

		d, err := deriveManifest(composeBytes, configBytes, repo, id, versions[id])
		if err != nil {
			skipped = append(skipped, id+": "+err.Error())
			continue
		}

		if err := os.WriteFile(filepath.Join(manifestDir, id+".yaml"), []byte(d.manifestYAML), 0o644); err != nil {
			skipped = append(skipped, id+": write "+err.Error())
			continue
		}
		if d.logoSrc != "" {
			if err := copyFile(d.logoSrc, filepath.Join(iconDir, id+filepath.Ext(d.logoSrc))); err != nil {
				fmt.Fprintf(os.Stderr, "  warn %s: copy logo: %v\n", id, err)
			}
		}
		cat.Apps = append(cat.Apps, catalogEntryFor(d, id))
		fmt.Printf("derived  %-14s (%d vars, %d variants)\n", id, len(d.xf.Variables), len(d.xf.Variants))
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

func catalogEntryFor(d *derived, id string) catEntry {
	xf := d.xf
	base := d.imageRepo
	var vs []catVariant
	for _, v := range xf.Variants {
		vs = append(vs, catVariant{ID: v.ID, Label: v.Label, Default: v.Default, Image: base + ":" + v.ID, Version: v.Version})
	}
	if len(vs) == 0 {
		vs = append(vs, catVariant{ID: "latest", Label: "Latest", Default: true, Image: base + ":latest"})
	}
	return catEntry{
		ID:          xf.Info.ID,
		Name:        xf.Info.Name,
		Category:    xf.Info.Category,
		Class:       xf.Info.Class,
		Icon:        xf.Info.Icon,
		Description: xf.Info.Description,
		UpstreamURL: xf.Info.UpstreamURL,
		WebURL:      xf.Info.WebURL,
		Image:       base,
		ManifestURL: "/catalog/manifests/" + id + ".yaml",
		Version:     xf.Info.Version,
		Variants:    vs,
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
