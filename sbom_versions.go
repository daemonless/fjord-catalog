package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// sbomFile is the subset of an image repo's sbom.json we need: per-tag app
// versions. Tag values come in two shapes across repos -- an object
// ({"app_version":"6.3.0",...}) or a bare version string ("15.0") -- so they
// decode lazily.
type sbomFile struct {
	Image string                     `json:"image"`
	Tags  map[string]json.RawMessage `json:"tags"`
}

// tagVersion extracts the app version from either tag-value shape.
func tagVersion(raw json.RawMessage) string {
	var obj struct {
		AppVersion string `json:"app_version"`
	}
	if json.Unmarshal(raw, &obj) == nil && obj.AppVersion != "" {
		return obj.AppVersion
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return ""
}

var (
	archSufRe  = regexp.MustCompile(`-(amd64|x86_64|aarch64|arm64)$`)
	multiTagRe = regexp.MustCompile(`^(\d[\w.]*?)(?:-(pkg-latest|pkg))?$`)
)

// simple channel-tag keys -> versions-file keys (see appVersions.forVariant).
var simpleKey = map[string]string{"latest": "upstream", "pkg": "pkg", "pkg-latest": "pkg-latest"}

// loadSbomVersions builds per-app versions straight from each repo's
// sbom.json -- no external versions service needed. sbom tag keys carry arch
// suffixes ("latest-amd64") and multi-version prefixes ("14-pkg-latest-amd64"
// for postgres-style images); they normalize to the same simple
// ({upstream,pkg,pkg-latest}) and multi ({variants:{ver:{channel}}}) shapes
// loadVersions reads. amd64 (or unsuffixed) entries are canonical; other-arch
// duplicates are skipped. "unknown" placeholder versions are dropped.
func loadSbomVersions(reposDir string, apps []string) map[string]*appVersions {
	out := map[string]*appVersions{}
	for _, app := range apps {
		b, err := os.ReadFile(filepath.Join(reposDir, app, "sbom.json"))
		if err != nil {
			continue
		}
		var sb sbomFile
		if json.Unmarshal(b, &sb) != nil {
			continue
		}
		id := sb.Image
		if id == "" {
			id = app
		}
		simple := map[string]string{}
		multi := map[string]map[string]string{}
		for tag, raw := range sb.Tags {
			v := tagVersion(raw)
			if v == "" || v == "unknown" {
				continue
			}
			if strings.HasSuffix(tag, "-aarch64") || strings.HasSuffix(tag, "-arm64") {
				continue
			}
			tag = archSufRe.ReplaceAllString(tag, "")
			if key, ok := simpleKey[tag]; ok {
				simple[key] = v
				continue
			}
			if m := multiTagRe.FindStringSubmatch(tag); m != nil {
				ch := m[2]
				if ch == "" {
					ch = "pkg"
				}
				if multi[m[1]] == nil {
					multi[m[1]] = map[string]string{}
				}
				multi[m[1]][ch] = v
			}
		}
		var av *appVersions
		switch {
		case len(multi) > 0:
			av = &appVersions{multi: multi}
		case len(simple) > 0:
			av = &appVersions{simple: simple}
		default:
			continue
		}
		out[id] = av
		if id != app {
			out[app] = av // deriver looks up by repo dir name
		}
	}
	return out
}
