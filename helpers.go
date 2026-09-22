package main

import (
	"bytes"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// envName uppercases a path component and collapses every non-alphanumeric rune
// to "_" so it forms a valid env-var fragment: "resolv.conf" -> "RESOLV_CONF",
// never the invalid "RESOLV.CONF" that breaks ${VAR} expansion.
func envName(s string) string {
	s = strings.ToUpper(strings.Trim(s, "/"))
	return strings.Map(func(r rune) rune {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '_'
	}, s)
}

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

// stripMarkdown removes inline emphasis markers so a README-flavored string
// reads cleanly as a plain label.
func stripMarkdown(s string) string {
	return strings.NewReplacer("*", "", "_", "", "`", "").Replace(s)
}

// varRefFull matches one whole ${...} reference, capturing the name and
// whatever follows it.
var varRefFull = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)([^}]*)\}`)

// composeDefault turns a compose value into the default the install form
// should show.
//
// A variabilized compose says `GARAGE_ZONE=${GARAGE_ZONE:-dc1}`, and taking
// that string as the default put the literal "${GARAGE_ZONE:-dc1}" in the
// field -- so the form asked the operator to read shell substitution syntax,
// and installing without touching it wrote that text into the .env.
//
// The forms compose supports, and what each means here:
//
//	${NAME}            no default; the field starts empty
//	${NAME:-x} ${NAME-x}   x is the default
//	${NAME:?msg}       required, no default
//	${NAME:+x}         only when NAME is set, so nothing to prefill
//
// A reference inside a larger string keeps the rest of it:
// "http://${ML_HOST:-localhost}:3003" -> "http://localhost:3003".
func composeDefault(val string) string {
	if !strings.Contains(val, "${") {
		return val
	}
	return varRefFull.ReplaceAllStringFunc(val, func(m string) string {
		g := varRefFull.FindStringSubmatch(m)
		rest := g[2]
		switch {
		case strings.HasPrefix(rest, ":-"):
			return rest[2:]
		case strings.HasPrefix(rest, ":?"), strings.HasPrefix(rest, ":+"):
			return ""
		case strings.HasPrefix(rest, "-"):
			return rest[1:]
		case rest == "":
			return ""
		default:
			// ":default" -- pyaml_env's form, which appjail-director uses.
			if strings.HasPrefix(rest, ":") {
				return rest[1:]
			}
			return ""
		}
	})
}
