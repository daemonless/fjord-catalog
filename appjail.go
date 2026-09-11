package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// appjailDbuildPath is the PYTHONPATH to the dbuild checkout used to render the
// AppJail bundle (dbuild owns the compose->director mapping). Empty disables
// bundle rendering, so the catalog still builds without dbuild present.
var appjailDbuildPath string

// appjailBundle is the dbuild-generated AppJail deploy bundle, carried inline in
// the manifest so fjord's appjail engine runs `appjail-director` without any
// compose translation of its own. director.yml references WEB_PORT and one var
// per volume via !ENV; fjord fills those into .env at install.
type appjailBundle struct {
	Director     string `yaml:"director"`
	Makejail     string `yaml:"makejail"`
	TemplateConf string `yaml:"template_conf,omitempty"`
	EnvDefaults  string `yaml:"env_defaults"`
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// renderAppjailBundle runs `dbuild appjail-bundle` in repoDir and returns the
// rendered bundle. Returns (nil, nil) when bundle rendering is disabled or the
// app isn't AppJail-enabled (dbuild emits no director file).
func renderAppjailBundle(repoDir string) (*appjailBundle, error) {
	if appjailDbuildPath == "" {
		return nil, nil
	}
	tmp, err := os.MkdirTemp("", "aj-bundle-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)

	cmd := exec.Command("python3", "-m", "dbuild", "appjail-bundle", "--out", tmp)
	cmd.Dir = repoDir
	cmd.Env = append(os.Environ(), "PYTHONPATH="+appjailDbuildPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("dbuild appjail-bundle in %s: %v: %s", repoDir, err, out)
	}

	read := func(name string) string {
		b, _ := os.ReadFile(filepath.Join(tmp, name))
		return string(b)
	}
	director := read("appjail-director.yml")
	if director == "" {
		return nil, nil // appjail: false -> dbuild emitted nothing
	}
	return &appjailBundle{
		Director:     director,
		Makejail:     read("Makejail"),
		TemplateConf: read("template.conf"),
		EnvDefaults:  read(".env"),
	}, nil
}
