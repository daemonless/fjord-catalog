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
