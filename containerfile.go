package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// A source with `from_containerfile: true` also takes repos that ship no
// compose.yaml -- an image repo with a Containerfile, a .daemonless/config.yaml
// and nothing else (the AppJail-makejails org). The compose they would have is
// written from the Containerfile: one service, the image's ports and volumes,
// and x-daemonless from its OCI labels. Everything after that is the normal
// path, so these apps get the same manifest, variants and AppJail bundle.

// containerfileRepo copies repo (with its .git, which dbuild reads the registry
// from) to a temp dir named id -- dbuild names the image after its directory --
// and writes the compose there. The caller removes the returned parent.
func containerfileRepo(repo, id string) (dir, parent string, compose []byte, err error) {
	compose, web, err := composeFromContainerfile(repo, id)
	if err != nil {
		return "", "", nil, err
	}
	parent, err = os.MkdirTemp("", "fc-containerfile-")
	if err != nil {
		return "", "", nil, err
	}
	dir = filepath.Join(parent, id)
	if out, err := exec.Command("cp", "-R", repo, dir).CombinedOutput(); err != nil {
		os.RemoveAll(parent)
		return "", "", nil, fmt.Errorf("copy %s: %v: %s", repo, err, out)
	}
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), compose, 0o644); err != nil {
		os.RemoveAll(parent)
		return "", "", nil, err
	}
	if web != 0 {
		if err := markWebUI(filepath.Join(dir, ".daemonless", "config.yaml"), web); err != nil {
			os.RemoveAll(parent)
			return "", "", nil, err
		}
	}
	return dir, parent, compose, nil
}

// markWebUI records in the copy's config that port serves a web UI, as a
// daemonless image's own test config does (cit), which is what gives the
// app its Open button. A config that already names a cit is left alone.
func markWebUI(path string, port int) error {
	doc := map[string]any{}
	if b, err := os.ReadFile(path); err == nil {
		if err := yaml.Unmarshal(b, &doc); err != nil {
			return fmt.Errorf("%s: %v", path, err)
		}
	}
	if _, ok := doc["cit"]; ok {
		return nil
	}
	doc["cit"] = map[string]any{"mode": "health", "port": port}
	b, err := yaml.Marshal(doc)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// composeFromContainerfile writes the one-service compose a repo's
// Containerfile describes, and the port its web UI is on (0: none known).
func composeFromContainerfile(repo, id string) ([]byte, int, error) {
	var cfg imageConfig
	if b, err := os.ReadFile(filepath.Join(repo, ".daemonless/config.yaml")); err == nil {
		_ = yaml.Unmarshal(b, &cfg)
	}
	// The default variant's own Containerfile: nextcloud builds only
	// Containerfile.apache and Containerfile.fpm.
	tag, file := "latest", "Containerfile"
	for _, v := range cfg.Build.Variants {
		if v.Default {
			tag = v.Tag
			if v.Containerfile != "" {
				file = v.Containerfile
			}
		}
	}
	raw, err := os.ReadFile(filepath.Join(repo, file))
	if err != nil {
		return nil, 0, fmt.Errorf("no compose.yaml or %s", file)
	}
	cf := parseContainerfile(string(raw))
	title := cf.labels["org.opencontainers.image.title"]
	if title == "" {
		return nil, 0, fmt.Errorf("%s: no org.opencontainers.image.title label (not a catalog app)", file)
	}
	registry, err := remoteRegistry(repo)
	if err != nil {
		return nil, 0, err
	}
	var ports []string
	ordered := webFirst(cf.expose, cfg.Cit.Port)
	web := 0
	if len(ordered) > 0 {
		web = webPort(ordered[0])
	}
	for _, p := range ordered {
		num := strings.TrimSuffix(p, "/tcp")
		host := num
		if i := strings.IndexByte(host, '/'); i >= 0 {
			host = host[:i]
		}
		ports = append(ports, host+":"+num)
	}
	// Written as a daemonless compose would: the app's own data under
	// /containers (a folder fjord makes), a library folder (/music) as a
	// path the person picks, so it gets the library's <KIND>_PATH name.
	var vols []string
	for _, v := range cf.volumes {
		base := strings.ToLower(filepath.Base(v))
		if libraryKinds[base] {
			vols = append(vols, "/path/to/"+base+":"+v)
			continue
		}
		vols = append(vols, "/containers/"+id+"/"+base+":"+v)
	}

	// No EXPOSE still installs and runs, with no port to set or open.
	svc := map[string]any{"image": registry + "/" + id + ":" + tag}
	if len(ports) > 0 {
		svc["ports"] = ports
	}
	if len(vols) > 0 {
		svc["volumes"] = vols
	}
	xd := map[string]any{"title": title, "appjail": map[string]any{}}
	if d := cf.labels["org.opencontainers.image.description"]; d != "" {
		xd["description"] = d
	}
	if u := cf.labels["org.opencontainers.image.url"]; u != "" {
		xd["upstream_url"] = u
	}
	compose, err := yaml.Marshal(map[string]any{
		"services":     map[string]any{id: svc},
		"x-daemonless": xd,
	})
	return compose, web, err
}

// hasContainerfile: a Containerfile, or a variant's Containerfile.<name>.
func hasContainerfile(dir string) bool {
	m, _ := filepath.Glob(filepath.Join(dir, "Containerfile*"))
	return len(m) > 0
}

// libraryKinds are the folder kinds fjord's folder sets pre-fill.
var libraryKinds = map[string]bool{"movies": true, "tv": true, "music": true, "audiobooks": true, "ebooks": true, "downloads": true, "photos": true}

// notWeb are well-known ports that speak something other than HTTP: a
// database, mail, DNS, a broker. Never taken for the web UI.
var notWeb = map[int]bool{
	21: true, 22: true, 25: true, 53: true, 110: true, 143: true, 465: true, 587: true, 993: true, 995: true,
	1025: true, 2379: true, 2380: true, 3306: true, 4369: true, 5432: true, 5671: true, 5672: true,
	6379: true, 9000: true, 11211: true, 27017: true,
}

// webPort is the TCP port number of an EXPOSE entry when it can be a web
// UI: 80, 443 or above 1023, and not a known non-HTTP port. 0 otherwise.
func webPort(p string) int {
	num, proto, _ := strings.Cut(p, "/")
	n, err := strconv.Atoi(num)
	if err != nil || (proto != "" && proto != "tcp") || notWeb[n] {
		return 0
	}
	if n == 80 || n == 443 || n > 1023 {
		return n
	}
	return 0
}

// webFirst puts the port people open first, where the deriver looks for it:
// the config's cit.port, else the first that can be a web UI (webPort) --
// gitea exposes ssh before 3000, AdGuard Home DNS before 80, mailpit SMTP
// before 8025. No such port: the order stands.
func webFirst(ports []string, citPort int) []string {
	pick := -1
	for i, p := range ports {
		num, _, _ := strings.Cut(p, "/")
		if citPort != 0 {
			if num == fmt.Sprint(citPort) {
				pick = i
				break
			}
			continue
		}
		if webPort(p) != 0 {
			pick = i
			break
		}
	}
	if pick <= 0 {
		return ports
	}
	out := append([]string{ports[pick]}, ports[:pick]...)
	return append(out, ports[pick+1:]...)
}

// remoteRegistry is where the repo's images are published, by dbuild's own
// rule: ghcr.io/<owner of the origin remote>, lowercased.
func remoteRegistry(repo string) (string, error) {
	out, err := exec.Command("git", "-C", repo, "-c", "safe.directory=*", "remote", "get-url", "origin").Output()
	if err != nil {
		return "", fmt.Errorf("no origin remote to name the registry: %v", err)
	}
	m := regexp.MustCompile(`github\.com[:/]([^/]+)/`).FindStringSubmatch(strings.TrimSpace(string(out)))
	if m == nil {
		return "", fmt.Errorf("origin %q is not a GitHub repo", strings.TrimSpace(string(out)))
	}
	return "ghcr.io/" + strings.ToLower(m[1]), nil
}

type containerfile struct {
	labels  map[string]string
	expose  []string
	volumes []string
}

// parseContainerfile reads the instructions a compose needs: LABEL, EXPOSE
// and VOLUME. Continuation lines are joined; everything else is ignored.
// EXPOSE and VOLUME are expanded against the ARG and ENV defaults set above
// them (`ENV PORT=3000` / `EXPOSE $PORT`); a port still unresolved is dropped.
func parseContainerfile(src string) containerfile {
	cf := containerfile{labels: map[string]string{}}
	var lines []string
	cur := ""
	for _, l := range strings.Split(src, "\n") {
		t := strings.TrimSpace(l)
		if cur == "" && (t == "" || strings.HasPrefix(t, "#")) {
			continue
		}
		if strings.HasSuffix(t, "\\") {
			cur += strings.TrimSuffix(t, "\\") + " "
			continue
		}
		lines = append(lines, cur+t)
		cur = ""
	}
	vars := map[string]string{}
	expand := func(v string) string { return os.Expand(v, func(k string) string { return vars[k] }) }
	for _, l := range lines {
		word, rest, _ := strings.Cut(l, " ")
		rest = strings.TrimSpace(rest)
		switch strings.ToUpper(word) {
		case "ARG", "ENV":
			toks := shellWords(rest)
			if len(toks) == 2 && !strings.Contains(toks[0], "=") && strings.ToUpper(word) == "ENV" {
				vars[toks[0]] = expand(toks[1]) // legacy `ENV KEY value`
				continue
			}
			for _, t := range toks {
				if k, v, ok := strings.Cut(t, "="); ok {
					vars[k] = expand(v)
				}
			}
		case "LABEL":
			toks := shellWords(rest)
			for _, t := range toks {
				if k, v, ok := strings.Cut(t, "="); ok {
					cf.labels[k] = v
				}
			}
		case "EXPOSE":
			for _, p := range strings.Fields(expand(rest)) {
				if num, _, _ := strings.Cut(p, "/"); num != "" && strings.Trim(num, "0123456789") == "" {
					cf.expose = append(cf.expose, p)
				}
			}
		case "VOLUME":
			var arr []string
			if json.Unmarshal([]byte(rest), &arr) != nil {
				arr = strings.Fields(rest)
			}
			for _, v := range arr {
				if v = expand(v); strings.HasPrefix(v, "/") {
					cf.volumes = append(cf.volumes, v)
				}
			}
		}
	}
	return cf
}

// shellWords splits on spaces outside double quotes and drops the quotes:
// `a="b c" d=e` -> [a=b c, d=e].
func shellWords(s string) []string {
	var out []string
	var b strings.Builder
	inQ, esc, quoted := false, false, false
	for _, r := range s {
		switch {
		case esc:
			b.WriteRune(r)
			esc = false
		case r == '\\':
			esc = true
		case r == '"':
			inQ, quoted = !inQ, true
		case r == ' ' && !inQ:
			if b.Len() > 0 || quoted {
				out = append(out, b.String())
			}
			b.Reset()
			quoted = false
		default:
			b.WriteRune(r)
		}
	}
	if b.Len() > 0 || quoted {
		out = append(out, b.String())
	}
	return out
}
