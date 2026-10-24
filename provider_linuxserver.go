package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// linuxserverProvider builds the linuxserver.io catalog: the app list from
// their API (one record per image: description, category, version, release
// channels, architectures), and each app's install config from its
// readme-vars.yml (ports, volumes, env, devices). Their images are Linux
// ones, so the catalog says os: [linux] and fjord offers it only where they
// run.
type linuxserverProvider struct {
	apiURL   string
	rawURL   string // readme-vars base: <rawURL>/docker-<app>/<branch>/readme-vars.yml
	cacheDir string // fetched readme-vars and icons
	include  *regexp.Regexp
	exclude  *regexp.Regexp
	apps     []string
	tested   map[string]string // app -> "pass" or why not; nil = offer every app
	images   map[string]lsioImage
	client   *http.Client
}

// lsioImage is one record of api.linuxserver.io/api/v1/images.
type lsioImage struct {
	Name          string                       `json:"name"`
	ProjectURL    string                       `json:"project_url"`
	ProjectLogo   string                       `json:"project_logo"`
	Description   string                       `json:"description"`
	Version       string                       `json:"version"`
	Category      string                       `json:"category"`
	Deprecated    bool                         `json:"deprecated"`
	Tags          []struct{ Tag, Desc string } `json:"tags"`
	Architectures []struct{ Arch, Tag string } `json:"architectures"`
}

// readmeVars is the part of a linuxserver readme-vars.yml that says how the
// container runs.
type readmeVars struct {
	CommonEnv   lsBool         `yaml:"common_param_env_vars_enabled"`
	Volumes     lsList[lsVol]  `yaml:"param_volumes"`
	OptVolumes  lsList[lsVol]  `yaml:"opt_param_volumes"`
	Ports       lsList[lsPort] `yaml:"param_ports"`
	OptPorts    lsList[lsPort] `yaml:"opt_param_ports"`
	Env         lsList[lsEnv]  `yaml:"param_env_vars"`
	OptEnv      lsList[lsEnv]  `yaml:"opt_param_env_vars"`
	Devices     lsList[lsDev]  `yaml:"param_devices"`
	Net         string         `yaml:"param_net"`
	IncludeNet  lsBool         `yaml:"param_usage_include_net"`
	CapAdd      lsList[lsCap]  `yaml:"cap_add_param_vars"`
	CapAddOn    lsBool         `yaml:"cap_add_param"`
	Custom      lsList[lsCust] `yaml:"custom_params"`
	Privileged  lsBool         `yaml:"privileged"`
	ProjectName string         `yaml:"project_name"`
}

// lsList is a readme-vars list that may be written as an empty string
// (shotcut: cap_add_param_vars: ”).
type lsList[T any] []T

func (l *lsList[T]) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		*l = nil
		return nil
	}
	var v []T
	if err := n.Decode(&v); err != nil {
		return err
	}
	*l = v
	return nil
}

// lsBool is a readme-vars switch, which may be a word: duckdns has
// common_param_env_vars_enabled: 'optional' (the variables are offered).
type lsBool bool

func (b *lsBool) UnmarshalYAML(n *yaml.Node) error {
	switch strings.ToLower(strings.Trim(n.Value, `"'`)) {
	case "true", "yes", "optional":
		*b = true
	default:
		*b = false
	}
	return nil
}

type lsVol struct {
	Path string `yaml:"vol_path"`
	Desc string `yaml:"desc"`
}
type lsPort struct {
	External string `yaml:"external_port"`
	Internal string `yaml:"internal_port"`
	Desc     string `yaml:"port_desc"`
}
type lsEnv struct {
	Var   string `yaml:"env_var"`
	Value string `yaml:"env_value"`
	Desc  string `yaml:"desc"`
}
type lsDev struct {
	Path string `yaml:"device_path"`
	Host string `yaml:"device_host_path"`
}
type lsCap struct {
	Var string `yaml:"cap_add_var"`
}
type lsCust struct {
	Compose string `yaml:"name_compose"`
	Value   any    `yaml:"value"`
}

func (p *linuxserverProvider) http() *http.Client {
	if p.client == nil {
		p.client = &http.Client{Timeout: 60 * time.Second}
	}
	return p.client
}

func (p *linuxserverProvider) get(url string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "fjord-catalog")
	resp, err := p.http().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// Discover reads the API's image list: every image not deprecated, filtered
// by include/exclude and, when tested results are given, only those that
// passed the install test.
func (p *linuxserverProvider) Discover() ([]AppRef, error) {
	b, err := p.get(p.apiURL)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Data struct {
			Repositories map[string][]lsioImage `json:"repositories"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("linuxserver API: %w", err)
	}
	p.images = map[string]lsioImage{}
	var refs []AppRef
	for _, img := range doc.Data.Repositories["linuxserver"] {
		if img.Deprecated || img.Name == "" {
			continue
		}
		if p.include != nil && !p.include.MatchString(img.Name) || p.exclude != nil && p.exclude.MatchString(img.Name) {
			continue
		}
		if len(p.apps) > 0 && !contains(p.apps, img.Name) {
			continue
		}
		if p.tested != nil && p.tested[img.Name] != "pass" {
			continue
		}
		p.images[img.Name] = img
		refs = append(refs, AppRef{ID: img.Name})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].ID < refs[j].ID })
	return refs, nil
}

// Derive fetches one app's readme-vars.yml (cached) and its logo, and builds
// the manifest.
func (p *linuxserverProvider) Derive(ref AppRef) (*DerivedApp, error) {
	img := p.images[ref.ID]
	raw, err := p.readmeVars(ref.ID)
	if err != nil {
		return nil, err
	}
	var rv readmeVars
	if err := yaml.Unmarshal(raw, &rv); err != nil {
		return nil, fmt.Errorf("readme-vars.yml: %w", err)
	}
	da, err := deriveLSIO(img, rv)
	if err != nil {
		return nil, err
	}
	if img.ProjectLogo != "" {
		if src, err := p.icon(ref.ID, img.ProjectLogo); err == nil {
			da.IconSrc = src
			ext := filepath.Ext(src)
			da.Entry.Icon = "icons/" + ref.ID + ext
			da.ManifestYAML = strings.Replace(da.ManifestYAML, "icon: /catalog/icons/"+ref.ID+".png", "icon: /catalog/icons/"+ref.ID+ext, 1)
		}
	}
	return da, nil
}

func (p *linuxserverProvider) readmeVars(app string) ([]byte, error) {
	path := filepath.Join(p.cacheDir, "readme-vars", app+".yml")
	if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
		return b, nil
	}
	var last error
	for _, branch := range []string{"master", "main"} {
		b, err := p.get(fmt.Sprintf("%s/docker-%s/%s/readme-vars.yml", p.rawURL, app, branch))
		if err == nil {
			os.MkdirAll(filepath.Dir(path), 0o755)
			os.WriteFile(path, b, 0o644)
			return b, nil
		}
		last = err
	}
	return nil, last
}

func (p *linuxserverProvider) icon(app, url string) (string, error) {
	ext := strings.ToLower(filepath.Ext(strings.SplitN(url, "?", 2)[0]))
	if ext != ".svg" && ext != ".png" && ext != ".jpg" && ext != ".webp" {
		ext = ".png"
	}
	path := filepath.Join(p.cacheDir, "icons", app+ext)
	if fi, err := os.Stat(path); err == nil && fi.Size() > 0 {
		return path, nil
	}
	b, err := p.get(url)
	if err != nil {
		return "", err
	}
	os.MkdirAll(filepath.Dir(path), 0o755)
	return path, os.WriteFile(path, b, 0o644)
}

// lsioLibraryKinds maps a container path to the folder kind fjord's presets fill in: a
// volume at one of these container paths is named <KIND>_PATH, the same name
// the daemonless images use, so an install pre-fills the right library.
var lsioLibraryKinds = map[string]string{
	"movies": "MOVIES", "tv": "TV", "music": "MUSIC", "audiobooks": "AUDIOBOOKS",
	"books": "EBOOKS", "ebooks": "EBOOKS", "downloads": "DOWNLOADS", "photos": "PHOTOS", "pictures": "PHOTOS",
}

// "WebUI" and "webinterface" have no word break inside: \bweb\b misses them.
var webPortRe = regexp.MustCompile(`(?i)(webui|web ?interface|\b(web|ui|gui|http|dashboard|interface)\b)`)

// proxiedRe: a port that only works behind a proxy ("desktop gui HTTP, must
// be proxied", "only to be used by reverse proxies" beside an HTTPS one):
// opened directly, the app refuses it.
var proxiedRe = regexp.MustCompile(`(?i)\bproxied\b|reverse[ -]prox(y|ies)`)

// offByDefaultRe: a port nothing answers on until it is switched on in the
// app (calibre's content server, "needs to be enabled in gui settings
// first"): not where the Open link goes.
var offByDefaultRe = regexp.MustCompile(`(?i)needs to be enabled`)

// webPortIndex picks the web UI among ports, the first required ones
// counting: a web-sounding TCP port, plain HTTP before HTTPS (kasm's are all
// "(https)"; "HTTPS WineGUI." is web without saying so), never one that must
// be proxied or is off until enabled, else a lone required port that does
// not say it is something else. -1 when none is.
func webPortIndex(ports []lsPort, required int) int {
	// The web UI is a TCP port: wireguard's one port is UDP 51820.
	tcp := func(pt lsPort) bool {
		_, proto, _ := strings.Cut(pt.Internal, "/")
		return proto == "" || proto == "tcp"
	}
	for _, wantTLS := range []bool{false, true} {
		for i, pt := range ports {
			if i < required && tcp(pt) && (wantTLS || webPortRe.MatchString(pt.Desc)) && !proxiedRe.MatchString(pt.Desc) && !offByDefaultRe.MatchString(pt.Desc) && isHTTPS(pt.Desc) == wantTLS {
				return i
			}
		}
	}
	if required == 1 && tcp(ports[0]) && loneWebPort(ports[0].Desc) {
		return 0
	}
	return -1
}

// hostWebPorts: host-network apps whose readme-vars list no port at all; the
// web port is only in their README's prose.
var hostWebPorts = map[string]string{"plex": "32400"}

// A lone port is the web UI unless it says otherwise: mariadb's 3306, ssh's
// 2222, ngircd's 6667 and Wyoming's 10200 are not, nor is a bare API.
var notWebRe = regexp.MustCompile(`(?i)(ssh|irc|wyoming|mysql|mariadb|postgres|redis|mqtt|smtp|imap|\bdns\b|\bftp\b|\bapi\b)`)

// readme-vars examples standing in for the user's own: "<hostname or ip>",
// "your_db_user", "user:password@", "kimai.example.com", "${OTHER}". Offered
// as a default they install an app pointed at nothing.
// "(https)" or "HTTPS port", not a link: foldingathome's web gui "redirects to
// https://app.foldingathome.org".
var httpsRe = regexp.MustCompile(`(?i)\bhttps\b([^:]|$)`)

func isHTTPS(desc string) bool { return httpsRe.MatchString(desc) }

func loneWebPort(desc string) bool {
	return !notWebRe.MatchString(desc) || strings.Contains(strings.ToLower(desc), "http")
}

var placeholderRe = regexp.MustCompile(`(?i)(<[^>]+>|\byour[_-]|user:password@|\bexample\.(com|org|net)\b|\$\{)`)

type lsService struct {
	Image         string   `yaml:"image"`
	ContainerName string   `yaml:"container_name"`
	Restart       string   `yaml:"restart"`
	NetworkMode   string   `yaml:"network_mode,omitempty"`
	Privileged    bool     `yaml:"privileged,omitempty"`
	CapAdd        []string `yaml:"cap_add,omitempty"`
	ShmSize       string   `yaml:"shm_size,omitempty"`
	Sysctls       []string `yaml:"sysctls,omitempty"`
	Environment   []string `yaml:"environment,omitempty"`
	Volumes       []string `yaml:"volumes,omitempty"`
	Ports         []string `yaml:"ports,omitempty"`
	Devices       []string `yaml:"devices,omitempty"`
}

type lsManifest struct {
	Name     string               `yaml:"name"`
	Services map[string]lsService `yaml:"services"`
	XFjord   xFjord               `yaml:"x-fjord"`
}

// deriveLSIO turns one linuxserver image into a manifest and catalog entry.
func deriveLSIO(img lsioImage, rv readmeVars) (*DerivedApp, error) {
	id := img.Name
	if id == "" {
		return nil, fmt.Errorf("no name")
	}
	svc := lsService{Image: "lscr.io/linuxserver/" + id + ":latest", ContainerName: id, Restart: "unless-stopped", Privileged: bool(rv.Privileged)}
	var vars []variable
	add := func(v variable) {
		if !hasVar(vars, v.Name) {
			vars = append(vars, v)
		}
	}
	if rv.CommonEnv {
		svc.Environment = append(svc.Environment, "PUID=${PUID}", "PGID=${PGID}", "TZ=${TZ}")
	}
	for _, e := range rv.Env {
		if e.Var == "" {
			continue
		}
		svc.Environment = append(svc.Environment, e.Var+"=${"+e.Var+"}")
		typ := "string"
		if isSecret(e.Var) {
			typ = "secret"
		}
		dflt := e.Value
		if placeholderRe.MatchString(dflt) {
			dflt = ""
		}
		add(variable{Name: e.Var, Label: firstSentence(stripMarkdown(e.Desc)), Type: typ, Default: dflt})
	}
	// Optional variables are left out. Their values are examples, not
	// defaults (flexget's web password "info", lazylibrarian's calibre mods,
	// hishtory's Postgres URL), and set-but-empty is not unset for every app;
	// the editor can add one.
	for i, v := range append(append([]lsVol{}, rv.Volumes...), rv.OptVolumes...) {
		optional := i >= len(rv.Volumes)
		// "/audiobooks:ro": the options ride on the mount, not in its name.
		cpath, opts, _ := strings.Cut(v.Path, ":")
		cpath = strings.TrimRight(cpath, "/")
		if cpath == "" {
			continue
		}
		if opts != "" {
			opts = ":" + opts
		}
		base := strings.ToLower(filepath.Base(cpath))
		label := firstSentence(stripMarkdown(v.Desc))
		switch kind, isLib := lsioLibraryKinds[base]; {
		case cpath == "/config":
			add(variable{Name: "CONFIG_DATA", Label: firstNonEmpty(label, "Configuration"), Type: "zfs_dataset", Default: "config",
				HostPermissions: &hostPerms{Uid: 1000, Gid: 1000, Mode: "755"}})
			svc.Volumes = append(svc.Volumes, "${CONFIG_DATA}:/config"+opts)
		case isLib:
			name := kind + "_PATH"
			add(variable{Name: name, Label: label, Type: "path", Optional: optional})
			svc.Volumes = append(svc.Volumes, "${"+name+"}:"+cpath+opts)
		case !optional:
			// The app's own data: kept under App data, made for it.
			name := envName(base) + "_DATA"
			add(variable{Name: name, Label: label, Type: "zfs_dataset", Default: base,
				HostPermissions: &hostPerms{Uid: 1000, Gid: 1000, Mode: "755"}})
			svc.Volumes = append(svc.Volumes, "${"+name+"}:"+cpath+opts)
		default:
			name := envName(base) + "_PATH"
			add(variable{Name: name, Label: label, Type: "path", Optional: true})
			svc.Volumes = append(svc.Volumes, "${"+name+"}:"+cpath+opts)
		}
	}
	webVar, webLiteral := "", ""
	https := false
	ports := append(append([]lsPort{}, rv.Ports...), rv.OptPorts...)
	if rv.IncludeNet && rv.Net == "host" {
		svc.NetworkMode = "host"
		// Nothing is published, but the app still answers on its own port:
		// the Open link needs it (homeassistant's 8123, an optional port
		// "only if you are not using host mode").
		if web := webPortIndex(ports, len(ports)); web >= 0 {
			webLiteral, _, _ = strings.Cut(ports[web].Internal, "/")
			https = isHTTPS(ports[web].Desc)
		} else if p, ok := hostWebPorts[id]; ok {
			webLiteral = p
		}
	} else {
		web := webPortIndex(ports, len(rv.Ports))
		for i, pt := range ports {
			internal, proto, _ := strings.Cut(pt.Internal, "/")
			if internal == "" {
				continue
			}
			ext, _, _ := strings.Cut(pt.External, "/")
			name := "PORT_" + envName(internal)
			if proto == "udp" {
				name += "_UDP"
			}
			if i == web {
				name, webVar = "WEB_PORT", "WEB_PORT"
				https = isHTTPS(pt.Desc)
			}
			add(variable{Name: name, Label: firstSentence(stripMarkdown(pt.Desc)), Type: "port", Default: firstNonEmpty(ext, internal), Optional: i >= len(rv.Ports)})
			spec := "${" + name + "}:" + internal
			if proto != "" {
				spec += "/" + proto
			}
			svc.Ports = append(svc.Ports, spec)
		}
	}
	lg, ok := lsioLogins[id]
	if !ok && desktopApp(ports) {
		lg = selkiesLogin
	}
	xlogin := lg.apply(&svc, add)
	for _, d := range rv.Devices {
		if d.Path != "" {
			svc.Devices = append(svc.Devices, firstNonEmpty(d.Host, d.Path)+":"+d.Path)
		}
	}
	if rv.CapAddOn {
		for _, c := range rv.CapAdd {
			if c.Var != "" {
				svc.CapAdd = append(svc.CapAdd, c.Var)
			}
		}
	}
	for _, c := range rv.Custom {
		switch c.Compose {
		case "shm_size":
			svc.ShmSize = fmt.Sprint(c.Value)
		case "sysctls":
			if l, ok := c.Value.([]any); ok {
				for _, x := range l {
					svc.Sysctls = append(svc.Sysctls, fmt.Sprint(x))
				}
			}
		}
	}
	if rv.CommonEnv {
		add(variable{Name: "PUID", Label: "User ID for the application process", Type: "string", Default: "1000"})
		add(variable{Name: "PGID", Label: "Group ID for the application process", Type: "string", Default: "1000"})
		add(variable{Name: "TZ", Label: "Timezone for the container", Type: "string", Default: "UTC"})
	}

	xf := xFjord{Version: "0.1", Variables: vars, Login: xlogin}
	xf.Info = info{
		ID: id, Name: prettyName(id), Description: lsioBlurb(img.Description), Category: firstCategory(img.Category),
		Class: "service", Icon: "/catalog/icons/" + id + ".png", UpstreamURL: img.ProjectURL, Version: img.Version,
	}
	if webVar != "" {
		xf.Info.WebPort, xf.Info.WebHTTPS = "${"+webVar+"}", https
	} else if webLiteral != "" {
		xf.Info.WebPort, xf.Info.WebHTTPS = webLiteral, https
	}
	for i, t := range img.Tags {
		xf.Variants = append(xf.Variants, variant{ID: t.Tag, Label: firstNonEmpty(firstSentence(stripMarkdown(t.Desc)), t.Tag), Default: i == 0 && t.Tag == "latest" || t.Tag == "latest"})
	}
	b, err := yaml.Marshal(lsManifest{Name: id, Services: map[string]lsService{id: svc}, XFjord: xf})
	if err != nil {
		return nil, err
	}
	var variants []catVariant
	for _, v := range xf.Variants {
		variants = append(variants, catVariant{ID: v.ID, Label: v.Label, Default: v.Default, Image: "lscr.io/linuxserver/" + id + ":" + v.ID})
	}
	entry := catEntry{
		ID: id, Name: xf.Info.Name, Category: xf.Info.Category, Class: "service", Icon: "icons/" + id + ".png",
		Description: xf.Info.Description, UpstreamURL: img.ProjectURL, Image: "lscr.io/linuxserver/" + id,
		ManifestURL: "manifests/" + id + ".yaml", Version: img.Version, Architectures: lsioArches(img), Variants: variants,
		OS: []string{"linux"},
	}
	return &DerivedApp{Entry: entry, ManifestYAML: string(b), Vars: len(vars)}, nil
}

// prettyName: "adguardhome-sync" -> "Adguardhome Sync".
func prettyName(id string) string {
	words := strings.FieldsFunc(id, func(r rune) bool { return r == '-' || r == '_' })
	for i, w := range words {
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return strings.Join(words, " ")
}

var mdLink = regexp.MustCompile(`\[([^\]]*)\](\([^)]*\))?`)

// lsioBlurb drops the API's "radarr - " prefix and keeps the first sentence,
// markdown links as their text ("[AudioSilo](url) is" -> "AudioSilo is").
func lsioBlurb(desc string) string {
	if _, rest, ok := strings.Cut(desc, " - "); ok {
		desc = rest
	}
	desc = mdLink.ReplaceAllString(desc, "$1")
	s := firstSentence(stripMarkdown(desc))
	if s != "" && !strings.HasSuffix(s, ".") {
		s += "."
	}
	return s
}

func firstCategory(c string) string {
	first, _, _ := strings.Cut(c, ",")
	return strings.TrimSpace(first)
}

// lsioArches maps the API's arch names to OCI ones.
func lsioArches(img lsioImage) []string {
	var out []string
	for _, a := range img.Architectures {
		switch a.Arch {
		case "x86_64", "amd64":
			out = append(out, "amd64")
		case "arm64", "aarch64":
			out = append(out, "arm64")
		}
	}
	return out
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// lsioLogin is how one app signs in. A password variable becomes a required
// secret, so fjord makes it up at install instead of the app starting with
// its default (duplicati's "changeme") or no login at all (code-server); a
// user variable gets userDefault. user/password are the app's own default
// login, for apps that take no variable: shown, with "change it". env is
// set as it stands: what switches the login on (openssh-server's
// PASSWORD_ACCESS=true).
type lsioLogin struct {
	userVar, userDefault, passVar string
	user, password, note          string
	env                           []string
}

// selkiesLogin: a desktop app has no login unless PASSWORD is set; its
// nginx then asks for CUSTOM_USER and PASSWORD ("If unset there is no
// auth", linuxserver's Selkies configuration reference).
var selkiesLogin = lsioLogin{userVar: "CUSTOM_USER", userDefault: "admin", passVar: "PASSWORD"}

// lsioLogins, each from the app's readme-vars: its login variables
// (opt_param_env_vars), or the default its setup text names.
var lsioLogins = map[string]lsioLogin{
	"code-server": {passVar: "PASSWORD"},
	"openssh-server": {userVar: "USER_NAME", userDefault: "admin", passVar: "USER_PASSWORD",
		env: []string{"PASSWORD_ACCESS=true"}, note: "Sign in with ssh, on the stack's port 2222: ssh -p 2222 user@host."},
	"duplicati":    {passVar: "DUPLICATI__WEBSERVICE_PASSWORD"},
	"flexget":      {passVar: "FG_WEBUI_PASSWORD"},
	"nzbget":       {userVar: "NZBGET_USER", userDefault: "nzbget", passVar: "NZBGET_PASS"},
	"transmission": {userVar: "USER", userDefault: "admin", passVar: "PASS"},
	"vscodium-web": {passVar: "CONNECTION_TOKEN", note: "Add ?tkn= and this token to the page's address."},

	"airsonic-advanced": {user: "admin", password: "admin"},
	"babybuddy":         {user: "admin", password: "admin"},
	"calibre-web":       {user: "admin", password: "admin123"},
	"deluge":            {password: "deluge"},
	"diskover":          {user: "diskover", password: "darkdata"},
	"grocy":             {user: "admin", password: "admin"},
	"pyload-ng":         {user: "pyload", password: "pyload"},
	"raneto":            {user: "admin", password: "password"},
	"znc":               {user: "admin", password: "admin"},

	"audiosilo": {note: "AudioSilo prints the admin password to its log once, on first start: see Logs."},
}

// desktopApp: a Selkies desktop, known by its port that only works behind
// a proxy beside an HTTPS one.
func desktopApp(ports []lsPort) bool {
	return slices.ContainsFunc(ports, func(p lsPort) bool { return proxiedRe.MatchString(p.Desc) }) &&
		slices.ContainsFunc(ports, func(p lsPort) bool { return isHTTPS(p.Desc) })
}

// apply adds the login's variables to the service and returns its
// x-fjord.login, nil when there is none.
func (lg lsioLogin) apply(svc *lsService, add func(variable)) *login {
	if lg.userVar == "" && lg.passVar == "" && lg.user == "" && lg.password == "" && lg.note == "" {
		return nil
	}
	env := func(name string) {
		if !slices.ContainsFunc(svc.Environment, func(e string) bool { return strings.HasPrefix(e, name+"=") }) {
			svc.Environment = append(svc.Environment, name+"=${"+name+"}")
		}
	}
	for _, e := range lg.env {
		name, _, _ := strings.Cut(e, "=")
		if !slices.ContainsFunc(svc.Environment, func(x string) bool { return strings.HasPrefix(x, name+"=") }) {
			svc.Environment = append(svc.Environment, e)
		}
	}
	out := &login{User: lg.user, Password: lg.password, Note: lg.note}
	if lg.userVar != "" {
		env(lg.userVar)
		add(variable{Name: lg.userVar, Label: "User name to sign in with", Type: "string", Default: lg.userDefault})
		out.User = "${" + lg.userVar + "}"
	}
	if lg.passVar != "" {
		env(lg.passVar)
		add(variable{Name: lg.passVar, Label: "Password to sign in with", Type: "secret"})
		out.Password = "${" + lg.passVar + "}"
	}
	return out
}
