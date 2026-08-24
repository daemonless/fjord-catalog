package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// --- source shapes (subset we read) ---

type volDoc struct {
	Desc     string
	Optional bool
}

// UnmarshalYAML accepts either a bare string ("Movie library") or a mapping
// ({desc: ..., optional: true}).
func (v *volDoc) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		v.Desc = n.Value
		return nil
	}
	var m struct {
		Desc     string `yaml:"desc"`
		Optional bool   `yaml:"optional"`
	}
	if err := n.Decode(&m); err != nil {
		return err
	}
	v.Desc, v.Optional = m.Desc, m.Optional
	return nil
}

// xDaemonless is the typed metadata. docs are read separately from the node
// (parseDocs) because their shapes vary wildly across repos and a strict typed
// parse of docs would fail the whole app.
type xDaemonless struct {
	Title       string `yaml:"title"`
	Icon        string `yaml:"icon"`
	Category    string `yaml:"category"`
	Description string `yaml:"description"`
	UpstreamURL string `yaml:"upstream_url"`
	WebURL      string `yaml:"web_url"`
	Type        string `yaml:"type"` // "stack" = authored multi-service compose
}

// parseDocs reads x-daemonless.docs tolerantly from the node, returning
// env/volume/port label maps. Anything it can't read is simply omitted (labels
// are nice-to-have). Keys come from node .Value, so "67/udp" and 7878 both work.
func parseDocs(docsNode *yaml.Node) (env map[string]string, vols map[string]volDoc, ports map[string]string) {
	env, vols, ports = map[string]string{}, map[string]volDoc{}, map[string]string{}
	if docsNode == nil || docsNode.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(docsNode.Content); i += 2 {
		section := docsNode.Content[i].Value
		val := docsNode.Content[i+1]
		if val.Kind != yaml.MappingNode {
			continue
		}
		for j := 0; j+1 < len(val.Content); j += 2 {
			key, v := val.Content[j].Value, val.Content[j+1]
			switch section {
			case "env":
				if v.Kind == yaml.ScalarNode {
					env[key] = v.Value
				} else {
					var m struct {
						Desc string `yaml:"desc"`
					}
					_ = v.Decode(&m)
					env[key] = m.Desc
				}
			case "ports":
				if v.Kind == yaml.ScalarNode {
					ports[key] = v.Value
				}
			case "volumes":
				var vd volDoc
				_ = v.Decode(&vd)
				vols[key] = vd
			}
		}
	}
	return
}

type imageConfig struct {
	Cit struct {
		Mode string `yaml:"mode"`
		Port int    `yaml:"port"`
	} `yaml:"cit"`
	Build struct {
		Architectures []string `yaml:"architectures"`
		Variants      []struct {
			Tag     string `yaml:"tag"`
			Default bool   `yaml:"default"`
			TagDesc string `yaml:"tag_desc"`
		} `yaml:"variants"`
	} `yaml:"build"`
	Fjord struct {
		Exclude bool `yaml:"exclude"`
	} `yaml:"fjord"`
}

// --- x-fjord output shapes ---

type hostPerms struct {
	Uid  int    `yaml:"uid"`
	Gid  int    `yaml:"gid"`
	Mode string `yaml:"mode"`
}

type variable struct {
	Name            string     `yaml:"name"`
	Label           string     `yaml:"label,omitempty"`
	Type            string     `yaml:"type"`
	Default         string     `yaml:"default"`
	Optional        bool       `yaml:"optional,omitempty"`
	HostPermissions *hostPerms `yaml:"host_permissions,omitempty"`
}

type variant struct {
	ID      string `yaml:"id"`
	Label   string `yaml:"label"`
	Default bool   `yaml:"default,omitempty"`
	Version string `yaml:"version,omitempty"`
}

type info struct {
	ID           string   `yaml:"id"`
	Name         string   `yaml:"name"`
	Description  string   `yaml:"description,omitempty"`
	Category     string   `yaml:"category,omitempty"`
	Class        string   `yaml:"class"`
	Icon         string   `yaml:"icon,omitempty"`
	WebPort      string   `yaml:"web_port,omitempty"`
	UpstreamURL  string   `yaml:"upstream_url,omitempty"`
	WebURL       string   `yaml:"web_url,omitempty"`
	Version      string   `yaml:"version,omitempty"`
	OnlyForArchs []string `yaml:"only_for_archs,omitempty"`
}

type xFjord struct {
	Version   string     `yaml:"version"`
	Info      info       `yaml:"info"`
	Variables []variable `yaml:"variables"`
	Variants  []variant  `yaml:"variants,omitempty"`
}

// appVersions holds an app's per-variant versions from a versions file, in
// either the simple channel form ({pkg, pkg-latest, upstream}) or the
// multi-version form ({variants: {<ver>: {pkg, pkg-latest}}}).
type appVersions struct {
	simple map[string]string
	multi  map[string]map[string]string
}

// forVariant resolves a build variant tag to its version ("" if unknown). The
// channel/version scheme is the only place that knows the versions-file layout.
func (av *appVersions) forVariant(tag string) string {
	if av == nil {
		return ""
	}
	if av.simple != nil {
		if tag == "latest" {
			return av.simple["upstream"]
		}
		return av.simple[tag] // "pkg" / "pkg-latest" match directly
	}
	ver, channel := tag, "pkg"
	for _, suf := range []string{"-pkg-latest", "-pkg"} {
		if strings.HasSuffix(tag, suf) {
			ver, channel = strings.TrimSuffix(tag, suf), strings.TrimPrefix(suf, "-")
			break
		}
	}
	if m, ok := av.multi[ver]; ok {
		return m[channel]
	}
	return ""
}

var secretRe = regexp.MustCompile(`(?i)(password|secret|token|_key$|apikey)`)

// derived is what one app yields: the rendered manifest YAML plus the fields
// the catalog.json entry needs. logoSrc, if set, is a repo logo file the caller
// should copy into the catalog icons dir.
type derived struct {
	manifestYAML string
	xf           xFjord
	logoSrc      string
	imageRepo    string // the service image without its tag, e.g. ghcr.io/x/radarr
}

// resolveIcon prefers the app's own .daemonless/logo.svg|png (real logo, self
// hosted), falling back to an Iconify URL for the :material-*: / :simple-*:
// token. Returns the icon URL and, if a repo logo was found, its source path.
func resolveIcon(repoDir, id, token string) (url, logoSrc string) {
	for _, ext := range []string{"svg", "png"} {
		p := filepath.Join(repoDir, ".daemonless", "logo."+ext)
		if _, err := os.Stat(p); err == nil {
			return "/catalog/icons/" + id + "." + ext, p
		}
	}
	return iconifyURL(token), ""
}

// iconifyURL maps a mkdocs-material icon token to an Iconify SVG URL, tinted
// light for the dark UI. ":material-movie:" -> mdi:movie, ":simple-x:" ->
// simple-icons:x. Unknown prefixes yield "" (UI shows a letter placeholder).
func iconifyURL(token string) string {
	t := strings.Trim(token, ": ")
	set, name, ok := strings.Cut(t, "-")
	if !ok {
		return ""
	}
	prefix := map[string]string{"material": "mdi", "simple": "simple-icons"}[set]
	if prefix == "" {
		return ""
	}
	return fmt.Sprintf("https://api.iconify.design/%s:%s.svg?color=%%23cbd5e1", prefix, name)
}

// deriveManifest turns an app's compose.yaml + config.yaml into an x-fjord
// manifest. It returns an error (for log-and-skip) on anything it won't handle:
// missing services, multi-service, excluded, etc.
func deriveManifest(composeBytes, configBytes []byte, repoDir, id string, av *appVersions) (*derived, error) {
	var cfg imageConfig
	_ = yaml.Unmarshal(configBytes, &cfg) // config is optional/best-effort
	if cfg.Fjord.Exclude {
		return nil, fmt.Errorf("fjord.exclude=true")
	}

	// Typed metadata pass.
	var meta struct {
		Name        string         `yaml:"name"`
		XDaemonless xDaemonless    `yaml:"x-daemonless"`
		Services    map[string]any `yaml:"services"`
	}
	if err := yaml.Unmarshal(composeBytes, &meta); err != nil {
		return nil, fmt.Errorf("parse compose: %w", err)
	}
	if len(meta.Services) == 0 {
		return nil, fmt.Errorf("no services")
	}
	// Authored multi-service stacks (x-daemonless type: stack) are already
	// hand-variabilized -- derive by collecting their ${VARS}, never rewriting.
	if meta.XDaemonless.Type == "stack" {
		return deriveStackManifest(composeBytes, meta.XDaemonless, repoDir, id)
	}
	if len(meta.Services) > 1 {
		return nil, fmt.Errorf("multi-service (%d) without x-daemonless type: stack", len(meta.Services))
	}

	// Node pass for the ${VAR} rewrite.
	var doc yaml.Node
	if err := yaml.Unmarshal(composeBytes, &doc); err != nil {
		return nil, fmt.Errorf("parse compose node: %w", err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("compose is not a mapping")
	}
	root := doc.Content[0]

	// Read docs labels tolerantly, then strip x-daemonless from the output.
	envDocs, volDocs, portDocs := parseDocs(mapGet(mapGet(root, "x-daemonless"), "docs"))
	root.Content = dropKey(root.Content, "x-daemonless")

	services := mapGet(root, "services")
	svcName, svcNode := firstChild(services)
	if svcNode == nil {
		return nil, fmt.Errorf("service node missing")
	}
	_ = svcName

	xd := meta.XDaemonless
	if xd.Title == "" {
		return nil, fmt.Errorf("no x-daemonless.title (not a catalog app)")
	}
	// Image repo comes from the compose itself -- no registry is assumed.
	imageRepo := ""
	if img := mapGet(svcNode, "image"); img != nil && img.Kind == yaml.ScalarNode {
		imageRepo = repoOf(img.Value)
	}
	var vars []variable

	// ports -> WEB_PORT / PORT_<c>
	if ports := mapGet(svcNode, "ports"); ports != nil && ports.Kind == yaml.SequenceNode {
		for i, item := range ports.Content {
			// Only short-form scalar ports ("15630:15630") are variabilized.
			// Long-form ({published, target}) entries have no scalar Value --
			// leave them literal rather than stamping !!str on a mapping node,
			// which produces invalid YAML.
			if item.Kind != yaml.ScalarNode {
				continue
			}
			host, cont := splitColon2(item.Value)
			if cont == "" {
				cont = host // "7878" form -> same
			}
			// Split the protocol off the container port: a variable name can't
			// contain '/' ("${PORT_53/udp}" is bash substitution syntax, not a
			// lookup), and the protocol belongs only on the container side of the
			// mapping (once). "3478/udp" -> num 3478, proto udp -> PORT_3478_UDP.
			num, proto := cont, ""
			if s := strings.IndexByte(cont, '/'); s >= 0 {
				num, proto = cont[:s], cont[s+1:]
			}
			name := "PORT_" + num
			if proto != "" && proto != "tcp" {
				name += "_" + strings.ToUpper(proto)
			}
			if num == fmt.Sprint(cfg.Cit.Port) || (cfg.Cit.Port == 0 && i == 0) {
				name = "WEB_PORT"
			}
			label := "Port"
			if d, ok := portDocs[num]; ok {
				label = d
			}
			def := host // host-side port number, without any protocol
			if s := strings.IndexByte(def, '/'); s >= 0 {
				def = def[:s]
			}
			vars = append(vars, variable{Name: name, Label: label, Type: "port", Default: def})
			setScalar(item, fmt.Sprintf("${%s}:%s", name, cont), true)
		}
	}

	// volumes -> CONFIG_DATA (zfs_dataset) / <NAME>_PATH (path)
	if vols := mapGet(svcNode, "volumes"); vols != nil && vols.Kind == yaml.SequenceNode {
		for _, item := range vols.Content {
			host, cont, opts := splitVolume(item.Value)
			if cont == "" {
				continue
			}
			v := variable{Default: host}
			if cont == "/config" {
				v.Name = "CONFIG_DATA"
				v.Type = "zfs_dataset"
				v.Default = "config"
				v.HostPermissions = &hostPerms{Uid: 1000, Gid: 1000, Mode: "755"}
			} else {
				v.Name = strings.ToUpper(strings.Trim(filepath.Base(cont), "/")) + "_PATH"
				v.Type = "path"
				v.Default = ""
			}
			if d, ok := volDocs[cont]; ok {
				v.Label = d.Desc
				v.Optional = d.Optional
			}
			vars = append(vars, v)
			target := "${" + v.Name + "}:" + cont
			if opts != "" {
				target += ":" + opts
			}
			setScalar(item, target, true)
		}
	}

	// environment -> KEY=${KEY}. Compose allows both the array form
	// ("- KEY=val") and the mapping form ("KEY: val"); variabilize either.
	if env := mapGet(svcNode, "environment"); env != nil {
		addEnvVar := func(key, val string) {
			typ := "string"
			if secretRe.MatchString(key) {
				typ = "secret"
			}
			vars = append(vars, variable{Name: key, Label: envDocs[key], Type: typ, Default: val})
		}
		switch env.Kind {
		case yaml.SequenceNode:
			for _, item := range env.Content {
				key, val := splitEq(item.Value)
				if key == "" {
					continue
				}
				addEnvVar(key, val)
				setScalar(item, key+"=${"+key+"}", false)
			}
		case yaml.MappingNode:
			for i := 0; i+1 < len(env.Content); i += 2 {
				keyNode, valNode := env.Content[i], env.Content[i+1]
				if valNode.Kind != yaml.ScalarNode || keyNode.Value == "" {
					continue
				}
				addEnvVar(keyNode.Value, valNode.Value)
				setScalar(valNode, "${"+keyNode.Value+"}", true)
			}
		}
	}

	// Assemble x-fjord. Each variant carries its own version; the app-level
	// version is just the default variant's, for the store card.
	variants := deriveVariants(cfg, av)
	appVer := ""
	for _, v := range variants {
		if v.Default {
			appVer = v.Version
			break
		}
	}
	if appVer == "" && len(variants) > 0 {
		appVer = variants[0].Version
	}

	iconURL, logoSrc := resolveIcon(repoDir, id, xd.Icon)
	xf := xFjord{
		Version: "0.1",
		Info: info{
			ID:          firstNonEmpty(meta.Name, id),
			Name:        firstNonEmpty(xd.Title, id),
			Description: xd.Description,
			Category:    xd.Category,
			UpstreamURL: xd.UpstreamURL,
			WebURL:      xd.WebURL,
			Class:       "service",
			Icon:        iconURL,
			Version:     appVer,
		},
		Variables: vars,
		Variants:  variants,
	}
	if hasVar(vars, "WEB_PORT") && cfg.Cit.Mode == "screenshot" {
		xf.Info.WebPort = "${WEB_PORT}"
	}
	if len(cfg.Build.Architectures) > 0 {
		xf.Info.OnlyForArchs = cfg.Build.Architectures
	}

	// Attach x-fjord to the compose root and marshal. The MANIFEST copy drops
	// version fields (catalog.json carries them): versions bump on every
	// release, and version-free manifests stay byte-stable across bumps --
	// smaller diffs, better caching.
	mxf := xf
	mxf.Info.Version = ""
	mxf.Variants = make([]variant, len(xf.Variants))
	for i, v := range xf.Variants {
		v.Version = ""
		mxf.Variants[i] = v
	}
	xfNode, err := toNode(mxf)
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

func deriveVariants(cfg imageConfig, av *appVersions) []variant {
	canned := map[string]string{
		"latest":     "Upstream binary",
		"pkg":        "FreeBSD quarterly packages",
		"pkg-latest": "FreeBSD latest packages",
	}
	var out []variant
	for _, v := range cfg.Build.Variants {
		label := v.Tag
		if v.TagDesc != "" {
			label = firstSentence(v.TagDesc)
		} else if c, ok := canned[v.Tag]; ok {
			label = c
		}
		out = append(out, variant{ID: v.Tag, Label: label, Default: v.Default, Version: av.forVariant(v.Tag)})
	}
	// No declared variants: the image implicitly publishes "latest". Synthesize
	// it so the app still gets a variant (and a version) in the catalog.
	if len(out) == 0 {
		out = append(out, variant{ID: "latest", Label: canned["latest"], Default: true, Version: av.forVariant("latest")})
	}
	// Config listed variants but marked none default: the first one is treated
	// as default everywhere else (app version, wizard pick) -- say so.
	hasDefault := false
	for _, v := range out {
		if v.Default {
			hasDefault = true
			break
		}
	}
	if !hasDefault {
		out[0].Default = true
	}
	return out
}
