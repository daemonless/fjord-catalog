package main

// A Provider turns one source of apps into catalog entries + manifests.
// Discovery (how the app list is found) and derivation (how one app becomes an
// entry) are both source-specific; the shell just loops Discover -> Derive ->
// write. Today there is one provider (daemonless); the seam is where a
// linuxserver/appjail/... source plugs in without touching main.
type Provider interface {
	// Discover returns the apps this source offers.
	Discover() ([]AppRef, error)
	// Derive turns one app into its catalog entry + files. A returned error is
	// log-and-skip: the app is omitted and the run continues.
	Derive(AppRef) (*DerivedApp, error)
}

// AppRef identifies one app a provider offers. It is intentionally minimal --
// providers carry their own per-app context internally (repo path, API record).
type AppRef struct {
	ID string
}

// DerivedApp is a provider's result for one app: the catalog.json entry plus the
// files to emit -- the manifest YAML and an optional repo icon to copy.
type DerivedApp struct {
	Entry        catEntry
	ManifestYAML string
	IconSrc      string // source path of a repo logo to copy into icons/, "" if none
	Vars         int    // variable count, for the progress line
}
