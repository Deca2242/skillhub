//go:build !acceptance

package cli_test

// injectFaults: without the tag acceptance, the acceptance matrix walks its
// steps without the faults (seconds).
const injectFaults = false
