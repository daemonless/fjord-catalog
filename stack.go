package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// varRefRe matches ${VAR} and ${VAR:-default} references in an authored compose.
var varRefRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(?::-([^}]*))?\}`)

// imageTagVarRe matches an image ref whose TAG is a variable:
// "ghcr.io/x/app:${APP_TAG}" or "...:${APP_TAG:-latest}".
var imageTagVarRe = regexp.MustCompile(`^([^$\s]+):\$\{([A-Za-z_][A-Za-z0-9_]*)(?::-[^}]*)?\}$`)

// deriveStackManifest handles authored multi-service composes (x-daemonless
// type: stack). Unlike single-image apps, these are already hand-variabilized
// with ${VARS} and an example.env -- so the compose passes through UNTOUCHED
// (minus x-daemonless) and the variables are collected, not invented:
//   - default: inline ${VAR:-def} wins, else the (uncommented) example.env value
//   - label:   x-daemonless docs.env, when present
//   - type:    secret by name; path when the var is a volume's host side; else string
//
// No variants/train picker: a stack spans multiple image repos, so a single
// version tag is meaningless (and retagging its db/redis would be destructive).
func deriveStackManifest(composeBytes []byte, xd xDaemonless, cfg imageConfig, repoDir, id string) (*derived, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(composeBytes, &doc); err != nil {
		return nil, fmt.Errorf("parse compose node: %w", err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("compose is not a mapping")
	}
	root := doc.Content[0]
	envDocs, _, _ := parseDocs(mapGet(mapGet(root, "x-daemonless"), "docs"))
	root.Content = dropKey(root.Content, "x-daemonless")
	// A service behind a compose profile is a part the stack offers, not
	// part of the default install (immich's public proxy); fjord cannot
	// pick it yet, so it stays out of the manifest, variables included.
	dropProfiledServices(root)

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return nil, err
	}
	enc.Close()
	composeText := buf.String()

	exampleEnv := parseExampleEnv(filepath.Join(repoDir, "example.env"))

	// Map tag-position variables to their image repo: `image: repo:${VAR...}`
	// makes VAR an image_tag var, so the wizard can offer that repo's published
	// tags and follow the train picker.
	tagVarRepo := map[string]string{}
	if services := mapGet(root, "services"); services != nil {
		for i := 1; i < len(services.Content); i += 2 {
			svc := services.Content[i]
			if svc.Kind != yaml.MappingNode {
				continue
			}
			img := mapGet(svc, "image")
			if img == nil || img.Kind != yaml.ScalarNode {
				continue
			}
			if m := imageTagVarRe.FindStringSubmatch(img.Value); m != nil {
				tagVarRepo[m[2]] = m[1]
			}
		}
	}

	// Collect ${VAR} refs in order of first appearance.
	seen := map[string]bool{}
	var vars []variable
	for _, m := range varRefRe.FindAllStringSubmatch(composeText, -1) {
		name, inlineDef := m[1], m[2]
		if seen[name] {
			continue
		}
		seen[name] = true
		// An inline ${VAR:-fallback} means the compose works without a value --
		// the variable is optional by construction.
		optional := strings.Contains(m[0], ":-") || envDocs[name].Optional
		def := inlineDef
		if strings.Contains(def, "${") {
			// Nested fallback like ${A:-${B:-x}} -- leave the default empty so
			// the compose's own chain resolves at runtime (fjord's .env writer
			// skips empty values).
			def = ""
		}
		if def == "" {
			def = exampleEnv[name]
		}
		typ, imgRepo := "string", ""
		switch {
		case tagVarRepo[name] != "":
			typ, imgRepo = "image_tag", tagVarRepo[name]
		case secretRe.MatchString(name):
			typ = "secret"
		case strings.Contains(composeText, "${"+name+"}:/"):
			typ = "path" // host side of a bind mount
		}
		if envDocs[name].PublicURL {
			typ = "public_url"
		}
		vars = append(vars, variable{Name: name, Label: envDocs[name].Desc, Type: typ, Default: def, Optional: optional, Level: envDocs[name].Level, Image: imgRepo})
	}

	// First service image repo, for the store card / future use.
	imageRepo := ""
	if services := mapGet(root, "services"); services != nil {
		if _, svc := firstChild(services); svc != nil {
			if img := mapGet(svc, "image"); img != nil && img.Kind == yaml.ScalarNode {
				imageRepo = repoOf(varRefRe.ReplaceAllString(img.Value, ""))
			}
		}
	}

	iconURL, logoSrc := resolveIcon(repoDir, id, xd.Icon)
	xf := xFjord{
		Version: "0.1",
		// A multi-service stack is exactly the case this exists for: it has a
		// service people open and several that are its plumbing, and only the
		// app knows which is which.
		Networking: xd.Networking,
		Hostnames:  xd.Hostnames,
		Info: info{
			ID:          id,
			Name:        firstNonEmpty(xd.Title, id),
			Description: xd.Description,
			Category:    xd.Category,
			UpstreamURL: xd.UpstreamURL,
			WebURL:      xd.WebURL,
			Class:       "stack",
			Icon:        iconURL,
		},
		Variables: vars,
	}
	// Same web-endpoint rule as single-image apps: cit config is the truth
	// (host-net stacks publish nothing, so this hint is their ONLY web link).
	setWebEndpoint(&xf, cfg, repoDir)

	// AppJail bundle for multi-service stacks: an app that authored a full
	// appjail.director (immich) gets it emitted verbatim by dbuild. Fail-soft.
	if bundle, err := renderAppjailBundle(repoDir); err != nil {
		fmt.Fprintf(os.Stderr, "warning: %s: appjail bundle skipped: %v\n", id, err)
	} else if bundle != nil {
		xf.Appjail = bundle
	}
	if cs, err := renderChoices(repoDir); err != nil {
		fmt.Fprintf(os.Stderr, "warning: %s: choices skipped: %v\n", id, err)
	} else if len(cs) > 0 {
		xf.Choices = cs
	}

	xfNode, err := toNode(xf)
	if err != nil {
		return nil, err
	}
	root.Content = append(root.Content, scalarNode("x-fjord"), xfNode)
	out, err := marshalNode(root)
	if err != nil {
		return nil, err
	}
	return &derived{manifestYAML: out, xf: xf, arches: archesOf(cfg), logoSrc: logoSrc, imageRepo: imageRepo}, nil
}

// parseExampleEnv reads uncommented KEY=VALUE lines from an example.env.
// Missing file -> empty map.
func parseExampleEnv(path string) map[string]string {
	out := map[string]string{}
	b, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if eq := strings.IndexByte(line, '='); eq > 0 {
			out[strings.TrimSpace(line[:eq])] = strings.TrimSpace(line[eq+1:])
		}
	}
	return out
}

// dropProfiledServices removes every service that carries `profiles:` from
// the compose node, and the mentions of it in the others' depends_on lists.
func dropProfiledServices(root *yaml.Node) {
	services := mapGet(root, "services")
	if services == nil || services.Kind != yaml.MappingNode {
		return
	}
	gone := map[string]bool{}
	kept := make([]*yaml.Node, 0, len(services.Content))
	for i := 0; i+1 < len(services.Content); i += 2 {
		name, svc := services.Content[i], services.Content[i+1]
		if p := mapGet(svc, "profiles"); p != nil && (p.Kind == yaml.SequenceNode && len(p.Content) > 0) {
			gone[name.Value] = true
			continue
		}
		kept = append(kept, name, svc)
	}
	if len(gone) == 0 {
		return
	}
	services.Content = kept
	for i := 1; i < len(services.Content); i += 2 {
		dep := mapGet(services.Content[i], "depends_on")
		if dep == nil || dep.Kind != yaml.SequenceNode {
			continue
		}
		left := dep.Content[:0]
		for _, n := range dep.Content {
			if !gone[n.Value] {
				left = append(left, n)
			}
		}
		dep.Content = left
	}
}
