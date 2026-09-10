package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the builder's single knob (sources.yaml): catalog branding plus the
// sources to derive. An absent file falls back to defaultConfig() (the
// daemonless catalog), so the tool still works with zero config.
type Config struct {
	Catalog CatalogMeta `yaml:"catalog"`
	Sources []Source    `yaml:"sources"`
}

// CatalogMeta is the per-catalog branding emitted into catalog.json -- what was
// previously hardcoded in main().
type CatalogMeta struct {
	Name       string `yaml:"name"`
	Maintainer string `yaml:"maintainer"`
	Icon       string `yaml:"icon"`
}

// Source describes one place apps come from. `provider` selects how apps are
// discovered; `include`/`exclude` are regexps over app ids (empty = no filter).
type Source struct {
	Name     string      `yaml:"name"`
	Provider string      `yaml:"provider"` // local | daemonless-github
	Path     string      `yaml:"path"`     // local: subdir under --repos-dir to scan ("." = root)
	Org      string      `yaml:"org"`      // daemonless-github: the org to list + clone
	Include  string      `yaml:"include"`  // regexp; app id must match (empty = all)
	Exclude  string      `yaml:"exclude"`  // regexp; app id must NOT match (empty = none)
	Catalog  CatalogMeta `yaml:"catalog"`  // per-source branding; empty fields fall back to the top-level catalog:
}

// meta is the branding this source publishes: its own, backfilled from the
// top-level catalog block.
func (s Source) meta(top CatalogMeta) CatalogMeta {
	m := s.Catalog
	if m.Name == "" {
		m.Name = top.Name
	}
	if m.Maintainer == "" {
		m.Maintainer = top.Maintainer
	}
	if m.Icon == "" {
		m.Icon = top.Icon
	}
	if m.Name == "" {
		m.Name = s.Name
	}
	return m
}

// defaultConfig is what an absent sources.yaml means: derive the local repo
// dir as one unbranded source. Nothing daemonless-specific lives here -- a
// catalog's branding is its own data.
func defaultConfig() Config {
	return Config{
		Catalog: CatalogMeta{Name: "Apps"},
		Sources: []Source{{Name: "local", Provider: "local", Path: "."}},
	}
}

// loadConfig reads sources.yaml. A missing file yields defaultConfig(); a
// present one must name at least one source.
func loadConfig(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return defaultConfig(), nil
		}
		return Config{}, err
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if len(c.Sources) == 0 {
		return Config{}, fmt.Errorf("%s: no sources defined", path)
	}
	seen := map[string]bool{}
	for _, s := range c.Sources {
		if s.Name == "" || strings.ContainsAny(s.Name, "/\\ ") {
			return Config{}, fmt.Errorf("%s: every source needs a name usable as a directory (got %q)", path, s.Name)
		}
		if seen[s.Name] {
			return Config{}, fmt.Errorf("%s: duplicate source name %q", path, s.Name)
		}
		seen[s.Name] = true
	}
	return c, nil
}

// providerFor builds the Provider for one source. reposDir/versionsPath/appsCSV
// are runtime paths (flags) shared across sources. Unknown providers return an
// error the caller logs and skips.
func providerFor(src Source, reposDir, versionsPath, appsCSV string) (Provider, error) {
	inc, err := compileFilter(src.Include)
	if err != nil {
		return nil, fmt.Errorf("include: %w", err)
	}
	exc, err := compileFilter(src.Exclude)
	if err != nil {
		return nil, fmt.Errorf("exclude: %w", err)
	}
	var apps []string
	if appsCSV != "all" {
		apps = strings.Split(appsCSV, ",")
	}
	switch src.Provider {
	case "", "local":
		// A directory of app repos already on disk (a monorepo checkout, or
		// clones made by something else).
		scanDir := reposDir
		if src.Path != "" && src.Path != "." {
			scanDir = filepath.Join(reposDir, src.Path)
		}
		return &daemonlessProvider{reposDir: scanDir, apps: apps, versionsPath: versionsPath, include: inc, exclude: exc}, nil
	case "daemonless-github":
		if src.Org == "" {
			return nil, fmt.Errorf("provider daemonless-github needs org:")
		}
		// Clones land in <repos-dir>/<org>/ so two org sources can't collide.
		inner := &daemonlessProvider{reposDir: filepath.Join(reposDir, src.Org), versionsPath: versionsPath}
		return &githubProvider{org: src.Org, include: inc, exclude: exc, apps: apps, inner: inner}, nil
	default:
		return nil, fmt.Errorf("unknown provider %q", src.Provider)
	}
}

// compileFilter compiles a regexp, treating "" as nil (no filter).
func compileFilter(pat string) (*regexp.Regexp, error) {
	if pat == "" {
		return nil, nil
	}
	return regexp.Compile(pat)
}
