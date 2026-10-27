package main

import "testing"

// A daemonless login makes its password a required secret fjord makes up
// (paperless's admin is optional in its compose: empty, no admin), and a
// setting the compose lacks leaves the app without one.
func TestApplyLogin(t *testing.T) {
	xf := xFjord{Variables: []variable{
		{Name: "PAPERLESS_ADMIN_USER", Type: "string", Default: "admin", Optional: true},
		{Name: "PAPERLESS_ADMIN_PASSWORD", Type: "secret", Default: "changeme", Optional: true},
	}}
	if err := applyLogin(&xf, dlOverrides["paperless-ngx"]); err != nil {
		t.Fatal(err)
	}
	u, p := xf.Variables[0], xf.Variables[1]
	if u.Optional || p.Optional || p.Default != "" || p.Type != "secret" {
		t.Errorf("variables %+v %+v", u, p)
	}
	if xf.Login == nil || xf.Login.User != "${PAPERLESS_ADMIN_USER}" || xf.Login.Password != "${PAPERLESS_ADMIN_PASSWORD}" {
		t.Errorf("login %+v", xf.Login)
	}
	none := xFjord{}
	if err := applyLogin(&none, dlOverrides["librenms"]); err == nil || none.Login != nil {
		t.Errorf("no ADMIN_USER in the compose: err %v, login %+v", err, none.Login)
	}
	if err := applyLogin(&none, lsOverride{}); err != nil || none.Login != nil {
		t.Errorf("no override: err %v login %+v", err, none.Login)
	}
}

// An override names a web UI cit could not test, or takes one away; an
// endpoint cit already found is kept.
func TestApplyWeb(t *testing.T) {
	xf := xFjord{}
	applyWeb(&xf, dlOverrides["transmission-wireguard"])
	if xf.Info.WebPort != "${WEB_PORT}" {
		t.Errorf("transmission-wireguard web_port %q", xf.Info.WebPort)
	}
	ml := xFjord{Info: info{WebPort: "3003"}}
	applyWeb(&ml, dlOverrides["immich-ml"])
	if ml.Info.WebPort != "" {
		t.Errorf("immich-ml no_web kept %q", ml.Info.WebPort)
	}
	found := xFjord{Info: info{WebPort: "8080"}}
	applyWeb(&found, lsOverride{WebPort: "9999"})
	if found.Info.WebPort != "8080" {
		t.Errorf("cit's port replaced: %q", found.Info.WebPort)
	}
}
