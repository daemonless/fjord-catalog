package main

import "testing"

// A variabilized compose says ${NAME:-fallback}; taking that string as the
// install form's default showed the operator shell substitution syntax and
// wrote it verbatim into the .env when they pressed Install without editing.
func TestComposeDefault(t *testing.T) {
	for in, want := range map[string]string{
		// Garage, exactly as its compose declares them.
		"${RPC_SECRET}":                      "",
		"${RPC_PUBLIC_ADDR:-127.0.0.1:3901}": "127.0.0.1:3901",
		"${GARAGE_AUTO_LAYOUT:-true}":        "true",
		"${GARAGE_ZONE:-dc1}":                "dc1",
		"${GARAGE_CAPACITY:-10G}":            "10G",
		// The other forms compose allows.
		"${NAME-x}":   "x", // unset only
		"${NAME:?no}": "",  // required, nothing to prefill
		"${NAME:+x}":  "",  // only when already set
		"${NAME:d}":   "d", // pyaml_env's form, used by appjail-director
		// A reference inside a larger string keeps the rest of it.
		"http://${ML_HOST:-localhost}:3003": "http://localhost:3003",
		"${A:-a}/${B:-b}":                   "a/b",
		// A plain value is not a reference.
		"1000":     "1000",
		"UTC":      "UTC",
		"":         "",
		"$notavar": "$notavar",
	} {
		if got := composeDefault(in); got != want {
			t.Errorf("composeDefault(%q) = %q, want %q", in, got, want)
		}
	}
}
