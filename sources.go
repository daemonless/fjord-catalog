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
	Name     string `yaml:"name"`
	Provider string `yaml:"provider"` // local | monorepo | daemonless-github (org-clone: later)
	Path     string `yaml:"path"`     // local/monorepo: subdir under --repos-dir to scan ("." = root)
	Org      string `yaml:"org"`      // github providers: the org to scan (org-clone: later)
	Include  string `yaml:"include"`  // regexp; app id must match (empty = all)
	Exclude  string `yaml:"exclude"`  // regexp; app id must NOT match (empty = none)
}

func defaultConfig() Config {
	return Config{
		Catalog: CatalogMeta{
			Name:       "Daemonless Apps",
			Maintainer: "https://daemonless.io",
			Icon:       "/catalog/icon.svg",
		},
		Sources: []Source{{Name: "daemonless", Provider: "local", Path: "."}},
	}
}

// loadConfig reads sources.yaml. A missing file yields defaultConfig(); a present
// one has empty catalog fields / source list backfilled from the default.
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
	d := defaultConfig()
	if c.Catalog.Name == "" {
		c.Catalog.Name = d.Catalog.Name
	}
	if c.Catalog.Maintainer == "" {
		c.Catalog.Maintainer = d.Catalog.Maintainer
	}
	if c.Catalog.Icon == "" {
		c.Catalog.Icon = d.Catalog.Icon
	}
	if len(c.Sources) == 0 {
		c.Sources = d.Sources
	}
	return c, nil
}

// providerFor builds the Provider for one source. reposDir/versionsPath/appsCSV
// are runtime paths (flags) shared across sources. Unknown providers return an
// error the caller logs and skips.
func providerFor(src Source, reposDir, versionsPath, appsCSV string) (Provider, error) {
	switch src.Provider {
	case "", "local", "monorepo", "daemonless-github":
		// All scan a local directory of app subdirs today. daemonless-github's
		// org clone is done by CI (into reposDir) until org-clone discovery lands.
		scanDir := reposDir
		if src.Path != "" && src.Path != "." {
			scanDir = filepath.Join(reposDir, src.Path)
		}
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
		return &daemonlessProvider{
			reposDir:     scanDir,
			apps:         apps,
			versionsPath: versionsPath,
			include:      inc,
			exclude:      exc,
		}, nil
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
