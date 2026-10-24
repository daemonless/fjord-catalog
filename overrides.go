package main

import (
	"embed"
	"fmt"
	"path"
	"strings"

	"gopkg.in/yaml.v3"
)

// overrides/<source>/<app>.yaml: what a provider cannot read from an app's
// own metadata (linuxserver's readme-vars, a daemonless compose), one file per app, each naming where the fact comes from
// (source, and why: the line it rests on). Reviewed like code, read like data.
//
//go:embed overrides/linuxserver/*.yaml overrides/daemonless/*.yaml
var overrideFiles embed.FS

// lsOverride is one app's corrections.
type lsOverride struct {
	Source string `yaml:"source"`
	Why    string `yaml:"why"`
	// WebPort: the Open link's port where readme-vars list none (plex, on
	// host networking).
	WebPort string `yaml:"web_port"`
	// NoWeb: answers HTTP, but nothing a person opens (an API another app
	// calls): no Open link, whatever cit tests.
	NoWeb bool `yaml:"no_web"`
	// Login: its own login variables, made up at install (password_var), or
	// the default it ships with (user, password), or only a note.
	Login *struct {
		UserVar     string   `yaml:"user_var"`
		UserDefault string   `yaml:"user_default"`
		PasswordVar string   `yaml:"password_var"`
		User        string   `yaml:"user"`
		Password    string   `yaml:"password"`
		Note        string   `yaml:"note"`
		Env         []string `yaml:"env"`
	} `yaml:"login"`
	// GivenSecrets are secrets only their owner has, from an outside service
	// (duckdns's TOKEN): asked for at install, never made up.
	GivenSecrets []string `yaml:"given_secrets"`
}

var (
	lsOverrides = mustLoadOverrides("overrides/linuxserver")
	dlOverrides = mustLoadOverrides("overrides/daemonless")
)

func mustLoadOverrides(dir string) map[string]lsOverride {
	out, err := loadOverrides(dir)
	if err != nil {
		panic(err) // a broken override file is a build error; the tests read them all
	}
	return out
}

func loadOverrides(dir string) (map[string]lsOverride, error) {
	entries, err := overrideFiles.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := map[string]lsOverride{}
	for _, e := range entries {
		raw, err := overrideFiles.ReadFile(path.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		var o lsOverride
		dec := yaml.NewDecoder(strings.NewReader(string(raw)))
		dec.KnownFields(true) // a misspelt key would be ignored silently
		if err := dec.Decode(&o); err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		if o.Source == "" || o.Why == "" {
			return nil, fmt.Errorf("%s: no source or why: every override says where it comes from", e.Name())
		}
		out[strings.TrimSuffix(e.Name(), ".yaml")] = o
	}
	return out, nil
}

// lsioLogin is the override's login, false when it has none.
func (o lsOverride) lsioLogin() (lsioLogin, bool) {
	if o.Login == nil {
		return lsioLogin{}, false
	}
	l := o.Login
	return lsioLogin{userVar: l.UserVar, userDefault: l.UserDefault, passVar: l.PasswordVar,
		user: l.User, password: l.Password, note: l.Note, env: l.Env}, true
}

// applyLogin gives a daemonless app the login its override names: the
// card's user and password, and its password setting made a required
// secret so fjord makes it up -- paperless's admin is optional in its
// compose, and left empty there is no admin to sign in as. A setting the
// compose does not have leaves the app without a login, and says so.
func applyLogin(xf *xFjord, o lsOverride) error {
	lg, ok := o.lsioLogin()
	if !ok {
		return nil
	}
	find := func(name string) *variable {
		for i := range xf.Variables {
			if xf.Variables[i].Name == name {
				return &xf.Variables[i]
			}
		}
		return nil
	}
	l := &login{User: lg.user, Password: lg.password, Note: lg.note}
	if lg.userVar != "" {
		v := find(lg.userVar)
		if v == nil {
			return fmt.Errorf("login: the compose has no %s", lg.userVar)
		}
		v.Optional = false
		if v.Default == "" {
			v.Default = lg.userDefault
		}
		l.User = "${" + lg.userVar + "}"
	}
	if lg.passVar != "" {
		v := find(lg.passVar)
		if v == nil {
			return fmt.Errorf("login: the compose has no %s", lg.passVar)
		}
		v.Type, v.Optional, v.Default, v.Generate = "secret", false, "", nil
		l.Password = "${" + lg.passVar + "}"
	}
	xf.Login = l
	return nil
}
