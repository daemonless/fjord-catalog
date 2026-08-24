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
func deriveStackManifest(composeBytes []byte, xd xDaemonless, repoDir, id string) (*derived, error) {
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

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return nil, err
	}
	enc.Close()
	composeText := buf.String()

	exampleEnv := parseExampleEnv(filepath.Join(repoDir, "example.env"))

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
		optional := strings.Contains(m[0], ":-")
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
		typ := "string"
		switch {
		case secretRe.MatchString(name):
			typ = "secret"
		case strings.Contains(composeText, "${"+name+"}:/"):
			typ = "path" // host side of a bind mount
		}
		vars = append(vars, variable{Name: name, Label: envDocs[name], Type: typ, Default: def, Optional: optional})
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

	xfNode, err := toNode(xf)
	if err != nil {
		return nil, err
	}
	root.Content = append(root.Content, scalarNode("x-fjord"), xfNode)
	out, err := marshalNode(root)
	if err != nil {
		return nil, err
	}
	return &derived{manifestYAML: out, xf: xf, logoSrc: logoSrc, imageRepo: imageRepo}, nil
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
