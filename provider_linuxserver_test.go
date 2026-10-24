package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Six real apps, as linuxserver published them (testdata/linuxserver): the
// shapes the provider has to turn into manifests fjord installs.
func lsioFixture(t *testing.T, app string) (lsioImage, readmeVars) {
	t.Helper()
	var doc struct {
		Data struct {
			Repositories map[string][]lsioImage `json:"repositories"`
		} `json:"data"`
	}
	b, _ := os.ReadFile("testdata/linuxserver/images.json")
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	var img lsioImage
	for _, i := range doc.Data.Repositories["linuxserver"] {
		if i.Name == app {
			img = i
		}
	}
	var rv readmeVars
	raw, _ := os.ReadFile("testdata/linuxserver/" + app + ".yml")
	if err := yaml.Unmarshal(raw, &rv); err != nil {
		t.Fatal(err)
	}
	return img, rv
}

func deriveFixture(t *testing.T, app string) (*DerivedApp, lsManifest) {
	t.Helper()
	img, rv := lsioFixture(t, app)
	da, err := deriveLSIO(img, rv)
	if err != nil {
		t.Fatal(err)
	}
	var m lsManifest
	if err := yaml.Unmarshal([]byte(da.ManifestYAML), &m); err != nil {
		t.Fatalf("%s manifest does not parse: %v\n%s", app, err, da.ManifestYAML)
	}
	return da, m
}

func varNamed(vs []variable, n string) *variable {
	for i := range vs {
		if vs[i].Name == n {
			return &vs[i]
		}
	}
	return nil
}

func TestLinuxserverRadarr(t *testing.T) {
	da, m := deriveFixture(t, "radarr")
	s := m.Services["radarr"]
	if s.Image != "lscr.io/linuxserver/radarr:latest" || m.XFjord.Info.WebPort != "${WEB_PORT}" {
		t.Errorf("image %q web %q", s.Image, m.XFjord.Info.WebPort)
	}
	if strings.Join(s.Ports, ",") != "${WEB_PORT}:7878" || varNamed(m.XFjord.Variables, "WEB_PORT").Default != "7878" {
		t.Errorf("ports %v", s.Ports)
	}
	// /config is app data; the libraries are the folder kinds, optional.
	want := "${CONFIG_DATA}:/config,${MOVIES_PATH}:/movies,${DOWNLOADS_PATH}:/downloads"
	if strings.Join(s.Volumes, ",") != want {
		t.Errorf("volumes %v", s.Volumes)
	}
	if v := varNamed(m.XFjord.Variables, "MOVIES_PATH"); v == nil || !v.Optional || v.Type != "path" {
		t.Errorf("MOVIES_PATH %+v", v)
	}
	if v := varNamed(m.XFjord.Variables, "CONFIG_DATA"); v == nil || v.Type != "zfs_dataset" {
		t.Errorf("CONFIG_DATA %+v", v)
	}
	if strings.Join(da.Entry.OS, ",") != "linux" || len(da.Entry.Architectures) == 0 || da.Entry.Variants[0].ID != "latest" || !da.Entry.Variants[0].Default {
		t.Errorf("entry %+v", da.Entry)
	}
}

// Host networking, no ports: Plex.
func TestLinuxserverPlex(t *testing.T) {
	_, m := deriveFixture(t, "plex")
	s := m.Services["plex"]
	if s.NetworkMode != "host" || len(s.Ports) != 0 {
		t.Errorf("plex: network %q ports %v", s.NetworkMode, s.Ports)
	}
	// Its readme-vars list no port: the Open link still needs 32400.
	if w := m.XFjord.Info.WebPort; w != "32400" || m.XFjord.Info.WebHTTPS {
		t.Errorf("plex: web port %q https %v, want 32400 http", w, m.XFjord.Info.WebHTTPS)
	}
}

// Host networking publishes nothing, but the web port is the app's own:
// homeassistant's is an optional port "only if you are not using host mode",
// wireshark's the HTTPS one that needs no proxy.
func TestLinuxserverHostWebPort(t *testing.T) {
	for app, want := range map[string]struct {
		port  string
		https bool
	}{"homeassistant": {"8123", false}, "wireshark": {"3001", true}} {
		_, m := deriveFixture(t, app)
		s := m.Services[app]
		if s.NetworkMode != "host" || len(s.Ports) != 0 {
			t.Errorf("%s: network %q ports %v", app, s.NetworkMode, s.Ports)
		}
		if i := m.XFjord.Info; i.WebPort != want.port || i.WebHTTPS != want.https {
			t.Errorf("%s: web port %q https %v, want %q %v", app, i.WebPort, i.WebHTTPS, want.port, want.https)
		}
		if v := varNamed(m.XFjord.Variables, "WEB_PORT"); v != nil {
			t.Errorf("%s: a WEB_PORT variable with nothing published: %+v", app, v)
		}
	}
}

// A capability, a sysctl, devices, shm_size, required env with secrets.
func TestLinuxserverExtras(t *testing.T) {
	_, wg := deriveFixture(t, "wireguard")
	if wg.XFjord.Info.WebPort != "" {
		t.Errorf("wireguard's UDP port taken for a web UI: %q", wg.XFjord.Info.WebPort)
	}
	if s := wg.Services["wireguard"]; strings.Join(s.CapAdd, ",") != "NET_ADMIN" || len(s.Sysctls) == 0 {
		t.Errorf("wireguard: cap_add %v sysctls %v", s.CapAdd, s.Sysctls)
	}
	_, emby := deriveFixture(t, "emby")
	if s := emby.Services["emby"]; len(s.Devices) != 0 {
		t.Errorf("emby's GPU is optional, not a device it requires: %v", s.Devices)
	}
	_, altus := deriveFixture(t, "altus")
	// 3000 is "desktop gui HTTP, must be proxied": opened directly the app
	// refuses it. The Open link is 3001, HTTPS.
	if i := altus.XFjord.Info; i.WebPort != "${WEB_PORT}" || !i.WebHTTPS {
		t.Errorf("altus: web port %q https %v", i.WebPort, i.WebHTTPS)
	}
	if !strings.Contains(strings.Join(altus.Services["altus"].Ports, " "), "${WEB_PORT}:3001") {
		t.Errorf("altus: WEB_PORT is not 3001: %v", altus.Services["altus"].Ports)
	}
	if s := altus.Services["altus"]; s.ShmSize == "" {
		t.Error("altus: no shm_size")
	}
	_, bs := deriveFixture(t, "bookstack")
	if v := varNamed(bs.XFjord.Variables, "DB_PASSWORD"); v == nil || v.Type != "secret" {
		t.Errorf("bookstack DB_PASSWORD %+v", v)
	}
	if v := varNamed(bs.XFjord.Variables, "DB_HOST"); v == nil || v.Type != "string" || v.Optional {
		t.Errorf("bookstack DB_HOST %+v", v)
	}
}

// A volume path with options ("/audiobooks:ro") keeps them on the mount, and
// a markdown link in the description is its text.
func TestLinuxserverMountOptions(t *testing.T) {
	da, m := deriveFixture(t, "audiosilo")
	if got := strings.Join(m.Services["audiosilo"].Volumes, ","); !strings.Contains(got, "${AUDIOBOOKS_PATH}:/audiobooks:ro") && !strings.Contains(got, "${AUDIOBOOKS_DATA}:/audiobooks:ro") {
		t.Errorf("volumes %s", got)
	}
	if strings.ContainsAny(da.Entry.Description, "[]") {
		t.Errorf("description %q", da.Entry.Description)
	}
}

// Port descriptions as linuxserver wrote them (the install test's misses).
func TestWebPortDescriptions(t *testing.T) {
	for desc, want := range map[string]bool{
		"WebUI": true, "Application WebUI": true, "The port for the LazyLibrarian webinterface": true,
		"The port for the Medusa webui": true, "Port for Limnoria's web interface.": true,
		"Web gui port": true, "MariaDB listens on this port.": false, "ssh port": false,
	} {
		if got := webPortRe.MatchString(desc); got != want {
			t.Errorf("webPortRe(%q) = %v, want %v", desc, got, want)
		}
	}
	for desc, want := range map[string]bool{
		"MariaDB listens on this port.": false, "ngircd port": false, "ssh port": false,
		"Wyoming connection port.": false, "API port": false,
		"Port ZNC listens on.": true, "HTTPS port": true, "HTTPS Sessions and API communication port.": true,
		"will map the container's port 8000 to port 8000 on the host": true,
	} {
		if got := loneWebPort(desc); got != want {
			t.Errorf("loneWebPort(%q) = %v, want %v", desc, got, want)
		}
	}
}

func TestPlaceholderDefaults(t *testing.T) {
	for v, want := range map[string]bool{
		"<hostname or ip>": true, "<secret password>": true, "kimai.example.com": true,
		"mysql://your_db_user:your_db_pass@your_db_host:3306/your_db_name": true,
		"postgresql://user:password@planka-db:5432/planka":                 true,
		"postgresql://${HISHTORY_DB_USER}:x@db/h":                          true,
		"3306": false, "hedgedoc": false, "localhost": false, "/config/hishtory.db": false, "UTC": false,
	} {
		if got := placeholderRe.MatchString(v); got != want {
			t.Errorf("placeholderRe(%q) = %v, want %v", v, got, want)
		}
	}
}

func TestHTTPSDescriptions(t *testing.T) {
	for desc, want := range map[string]bool{
		"Kasm Workspaces interface. (https)": true, "HTTPS port": true, "https gui": true,
		"Folding@home web gui (redirects to [https://app.foldingathome.org](https://app.foldingathome.org)).": false,
		"WebUI": false,
	} {
		if got := isHTTPS(desc); got != want {
			t.Errorf("isHTTPS(%q) = %v, want %v", desc, got, want)
		}
	}
}

// Desktop apps beside altus's "must be proxied": boinc's HTTP port is "only
// to be used by reverse proxies", calibre's third is off until enabled.
func TestWebPortSkipsProxyOnlyAndOffPorts(t *testing.T) {
	calibre := []lsPort{
		{Internal: "8080", Desc: "Calibre desktop gui (only for reverse proxy access)."},
		{Internal: "8181", Desc: "Calibre desktop gui HTTPS."},
		{Internal: "8081", Desc: "Calibre webserver gui (needs to be enabled in gui settings first)."},
	}
	if i := webPortIndex(calibre, len(calibre)); i != 1 {
		t.Errorf("calibre: port %d, want 1 (8181)", i)
	}
	boinc := []lsPort{
		{Internal: "8080", Desc: "Boinc desktop gui HTTP (only to be used by reverse proxies and not direct access)."},
		{Internal: "8181", Desc: "Boinc desktop gui HTTPS."},
	}
	if i := webPortIndex(boinc, len(boinc)); i != 1 {
		t.Errorf("boinc: port %d, want 1 (8181)", i)
	}
}
