//go:build realgit

package testsupport

// The realgit build tag runs the git contract suites against the real git
// binary, so its shim is not installed. HOME and the XDG variables are still
// fake, and GIT_CONFIG_NOSYSTEM keeps /etc/gitconfig out.
func init() { realTools["git"] = true }
