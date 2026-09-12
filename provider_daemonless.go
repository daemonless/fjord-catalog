package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// daemonlessProvider derives apps from a directory of daemonless image repos --
// each a compose.yaml (+ optional .daemonless/config.yaml) carrying x-daemonless
// metadata. Versions come from each repo's sbom.json, or from an override file.
type daemonlessProvider struct {
	reposDir     string
	apps         []string       // explicit app ids, or nil to scan every repo
	versionsPath string         // optional versions override; "" -> per-repo sbom.json
	include      *regexp.Regexp // nil = no filter
	exclude      *regexp.Regexp // nil = no filter
	versions     map[string]*appVersions
}

// Discover lists the app ids (explicit set or a scan of reposDir), applies the
// include/exclude filters, and loads their versions. Versions load here, once
// the id set is known, so Derive stays a pure per-app transform.
func (p *daemonlessProvider) Discover() ([]AppRef, error) {
	ids := p.apps
	if ids == nil {
		ids = scanRepos(p.reposDir)
	}
	var refs []AppRef
	var clean []string
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if p.include != nil && !p.include.MatchString(id) {
			continue
		}
		if p.exclude != nil && p.exclude.MatchString(id) {
			continue
		}
		clean = append(clean, id)
		refs = append(refs, AppRef{ID: id})
	}
	if p.versionsPath != "" {
		p.versions = loadVersions(p.versionsPath)
	} else {
		p.versions = loadSbomVersions(p.reposDir, clean)
	}
	return refs, nil
}

// Derive reads one repo's compose + config and renders its x-fjord manifest and
// catalog entry.
func (p *daemonlessProvider) Derive(ref AppRef) (*DerivedApp, error) {
	repo := filepath.Join(p.reposDir, ref.ID)
	composeBytes, err := os.ReadFile(filepath.Join(repo, "compose.yaml"))
	if err != nil {
		return nil, fmt.Errorf("no compose.yaml")
	}
	configBytes, _ := os.ReadFile(filepath.Join(repo, ".daemonless/config.yaml"))

	d, err := deriveManifest(composeBytes, configBytes, repo, ref.ID, p.versions[ref.ID])
	if err != nil {
		return nil, err
	}
	return &DerivedApp{
		Entry:        catalogEntryFor(d, ref.ID),
		ManifestYAML: d.manifestYAML,
		IconSrc:      d.logoSrc,
		Vars:         len(d.xf.Variables),
	}, nil
}

// catalogEntryFor builds the catalog.json entry from a derived app.
func catalogEntryFor(d *derived, id string) catEntry {
	xf := d.xf
	base := d.imageRepo
	vs := []catVariant{}
	for _, v := range xf.Variants {
		vs = append(vs, catVariant{ID: v.ID, Label: v.Label, Default: v.Default, Image: base + ":" + v.ID, Version: v.Version})
	}
	// A single-image app with no declared variants still installs as :latest.
	// A stack's images are pinned by its own ${VAR} tags, so inventing a
	// "<first service>:latest" variant would mislead the wizard.
	if len(vs) == 0 && xf.Info.Class != "stack" {
		vs = append(vs, catVariant{ID: "latest", Label: "Latest", Default: true, Image: base + ":latest"})
	}
	return catEntry{
		ID:            xf.Info.ID,
		Name:          xf.Info.Name,
		Category:      xf.Info.Category,
		Class:         xf.Info.Class,
		Icon:          xf.Info.Icon,
		Description:   xf.Info.Description,
		UpstreamURL:   xf.Info.UpstreamURL,
		WebURL:        xf.Info.WebURL,
		Image:         base,
		ManifestURL:   "manifests/" + id + ".yaml",
		Version:       xf.Info.Version,
		Architectures: d.arches,
		Variants:      vs,
	}
}
