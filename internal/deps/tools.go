//go:build tools

// Package deps pins the dependencies of docs/design.md section 5 before any
// code uses them, so the orchestrator can resolve them into go.mod and go.sum.
// The tools build tag keeps this file out of every normal build; internal/arch
// exempts it, and only it, from the import rules.
package deps

import (
	_ "charm.land/bubbles/v2/help"
	_ "charm.land/bubbles/v2/key"
	_ "charm.land/bubbles/v2/table"
	_ "charm.land/bubbles/v2/textinput"
	_ "charm.land/bubbles/v2/viewport"
	_ "charm.land/bubbletea/v2"
	_ "charm.land/lipgloss/v2"
	_ "github.com/BurntSushi/toml"
	_ "github.com/aymanbagabas/go-udiff"
	_ "github.com/charmbracelet/x/exp/teatest/v2"
	_ "github.com/spf13/cobra"
	_ "go.yaml.in/yaml/v3"
)
