package main

import (
	"bytes"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// --- yaml.Node helpers ---

func mapGet(m *yaml.Node, key string) *yaml.Node {
	if m == nil {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// dropKey removes key (and its value) from a mapping node's Content slice.
func dropKey(content []*yaml.Node, key string) []*yaml.Node {
	out := content[:0:0]
	for i := 0; i+1 < len(content); i += 2 {
		if content[i].Value == key {
			continue
		}
		out = append(out, content[i], content[i+1])
	}
	return out
}

// firstChild returns the first (key, value) of a mapping node.
func firstChild(m *yaml.Node) (string, *yaml.Node) {
	if m == nil || len(m.Content) < 2 {
		return "", nil
	}
	return m.Content[0].Value, m.Content[1]
}

func scalarNode(v string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}
}

// setScalar rewrites a scalar node's value, optionally forcing double quotes
// (needed for "${VAR}:1234" so YAML doesn't parse the colon oddly).
func setScalar(n *yaml.Node, v string, quote bool) {
	n.Tag = "!!str"
	n.Value = v
	if quote {
		n.Style = yaml.DoubleQuotedStyle
	} else {
		n.Style = 0
	}
}

// toNode marshals v and returns it as a yaml mapping node.
func toNode(v any) (*yaml.Node, error) {
	b, err := yaml.Marshal(v)
	if err != nil {
		return nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	return doc.Content[0], nil
}

func marshalNode(root *yaml.Node) (string, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return "", err
	}
	enc.Close()
	return buf.String(), nil
}

// --- string helpers ---

// splitColon2 splits "host:cont" -> (host, cont). "7878" -> ("7878", "").
func splitColon2(s string) (string, string) {
	s = strings.Trim(s, `"`)
	if i := strings.LastIndex(s, ":"); i >= 0 {
		return s[:i], s[i+1:]
	}
	return s, ""
}

// splitVolume splits "HOST:CONTAINER[:opts]" preserving the container path.
func splitVolume(s string) (host, cont, opts string) {
	s = strings.Trim(s, `"`)
	parts := strings.Split(s, ":")
	switch len(parts) {
	case 2:
		return parts[0], parts[1], ""
	case 3:
		return parts[0], parts[1], parts[2]
	}
	return s, "", ""
}

func splitEq(s string) (string, string) {
	if i := strings.Index(s, "="); i >= 0 {
		return s[:i], s[i+1:]
	}
	return s, ""
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func hasVar(vs []variable, name string) bool {
	for _, v := range vs {
		if v.Name == name {
			return true
		}
	}
	return false
}

func atoiSafe(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// repoOf strips the tag/digest from an image reference, leaving the repo.
// "ghcr.io/x/radarr:latest" -> "ghcr.io/x/radarr".
func repoOf(image string) string {
	ref := strings.TrimSpace(image)
	if at := strings.LastIndex(ref, "@"); at >= 0 {
		ref = ref[:at]
	}
	slash := strings.LastIndex(ref, "/")
	if colon := strings.LastIndex(ref, ":"); colon > slash {
		ref = ref[:colon]
	}
	return ref
}

func firstSentence(s string) string {
	s = strings.TrimSpace(s)
	// Strip leading markdown bold marker like "**Develop branch** — ..."
	if i := strings.Index(s, "—"); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	if i := strings.Index(s, ". "); i >= 0 {
		s = s[:i]
	}
	s = strings.Trim(s, "*")
	return s
}
