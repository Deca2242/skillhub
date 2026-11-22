//go:build network

package testsupport

// The network build tag runs the GitHub contract suite against the real gh,
// so its shim is not installed and its credential variables are kept. The
// orchestrator runs it only with the user's approval (docs/design.md
// section 11). gh still sees the fake HOME, so it authenticates only through
// GH_TOKEN or GH_CONFIG_DIR. The same tag runs the skills.sh contract test
// against the real curl, which needs no credential (S14).
func init() {
	realTools["gh"] = true
	realTools["curl"] = true
}
