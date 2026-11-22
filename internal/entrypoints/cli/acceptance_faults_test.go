//go:build acceptance

package cli_test

// injectFaults: with the tag acceptance, the acceptance matrix crashes and
// fails every write of every command (minutes).
const injectFaults = true
