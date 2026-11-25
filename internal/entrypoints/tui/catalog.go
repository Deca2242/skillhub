package tui

import (
	"context"
	"strings"
	"unicode/utf8"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"skillhub/internal/app"
	"skillhub/internal/entrypoints/text"
)

type catalogLoadedMsg struct {
	seq    int
	result app.CatalogResult
	err    error
}

func newCatalogInput() textinput.Model {
	input := textinput.New()
	input.Prompt = text.TUICatalogPrompt
	input.CharLimit = 100
	return input
}

func (m *Model) loadCatalogOnEntry() tea.Cmd {
	if m.screen != ScreenInstall || !m.loaded || m.catalogLoaded || m.catalogLoading || !m.app.HasCatalog() {
		return nil
	}
	return m.loadCatalog(false)
}

func (m *Model) catalogAfterDataLoad() tea.Cmd {
	if m.app.HasCatalog() && m.catalogLoaded && !m.catalogLoading {
		// Look up the current query again so the cached rows reflect the new inventory.
		return m.loadCatalog(false)
	}
	return m.loadCatalogOnEntry()
}

func (m *Model) loadCatalog(refresh bool) tea.Cmd {
	m.catalogSeq++
	m.catalogLoading = true
	m.catalogErr = nil
	a, seq, q := m.app, m.catalogSeq, m.catalogQuery
	return func() tea.Msg {
		opts := app.CatalogOptions{Refresh: refresh}
		ctx := context.Background()
		if q == "" {
			res, err := a.PopularCatalog(ctx, opts)
			return catalogLoadedMsg{seq: seq, result: res, err: err}
		}
		res, err := a.SearchCatalog(ctx, q, 0, opts)
		return catalogLoadedMsg{seq: seq, result: res, err: err}
	}
}

func (m *Model) handleCatalogLoaded(msg catalogLoadedMsg) {
	if msg.seq != m.catalogSeq {
		return
	}
	m.catalogLoading = false
	m.catalogLoaded = true
	m.catalogErr = msg.err
	m.catalogResult = msg.result
	m.catalogCursor = min(m.catalogCursor, max(0, len(msg.result.Entries)-1))
	m.catalogTop = min(m.catalogTop, m.catalogCursor)
}

func (m *Model) changeCatalogQuery(query string) tea.Cmd {
	if query != m.catalogQuery {
		m.catalogQuery = query
		m.catalogCursor, m.catalogTop = 0, 0
		m.catalogResult = app.CatalogResult{}
	}
	m.clearCatalogStatus()
	return m.loadCatalog(false)
}

func (m *Model) clearCatalogStatus() {
	// A catalog key must not clear the status of a running install or plan.
	if !m.busy {
		m.status = ""
	}
}

func (m *Model) handleCatalogSearchKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch keyOf(msg) {
	case "enter":
		q := strings.TrimSpace(m.catalogInput.Value())
		m.catalogInput.Blur()
		m.mode = modeNormal
		if q != "" && utf8.RuneCountInString(q) < 2 {
			m.status = text.TUICatalogError(&app.CatalogError{Kind: app.ErrCatalogQueryTooShort})
			return m, nil
		}
		return m, m.changeCatalogQuery(q)
	case "esc":
		m.catalogInput.SetValue(m.catalogQuery)
		m.catalogInput.Blur()
		m.mode = modeNormal
		return m, nil
	}
	var cmd tea.Cmd
	m.catalogInput, cmd = m.catalogInput.Update(msg)
	return m, cmd
}

func (m *Model) handleCatalogKey(k string) tea.Cmd {
	switch k {
	case "/":
		if m.busy {
			return nil
		}
		m.clearCatalogStatus()
		m.catalogInput.SetValue(m.catalogQuery)
		m.catalogInput.CursorEnd()
		m.mode = modeCatalogSearch
		m.catalogInput.Focus()
	case "esc":
		if m.catalogQuery != "" {
			return m.changeCatalogQuery("")
		}
	case "o":
		if !m.busy {
			m.clearCatalogStatus()
			m.openInstallSource()
		}
	case "u":
		m.clearCatalogStatus()
		return m.loadCatalog(true)
	case "enter":
		if m.busy || m.catalogLoading {
			return nil
		}
		entry, ok := m.selectedCatalogEntry()
		switch {
		case !ok:
		case entry.Installed:
			m.status = text.TUICatalogAlreadyInstalled(entry)
		case !entry.Installable:
			m.status = text.TUICatalogUnsupported
		default:
			m.busy = true
			m.status = text.TUIResolving
			return resolveCmd(m.app, entry.InstallSource, m.ask())
		}
	case "up", "k", "down", "j", "pgup", "pgdown", "home", "end":
		m.clearCatalogStatus()
		m.moveCatalogCursor(k)
	}
	return nil
}

func (m *Model) selectedCatalogEntry() (app.CatalogEntry, bool) {
	if m.catalogCursor < 0 || m.catalogCursor >= len(m.catalogResult.Entries) {
		return app.CatalogEntry{}, false
	}
	return m.catalogResult.Entries[m.catalogCursor], true
}

func (m *Model) moveCatalogCursor(key string) {
	last := len(m.catalogResult.Entries) - 1
	page := max(1, m.catalogPage)
	cursor := m.catalogCursor
	switch key {
	case "up", "k":
		cursor--
	case "down", "j":
		cursor++
	case "pgup":
		cursor -= page
	case "pgdown":
		cursor += page
	case "home":
		cursor = 0
	case "end":
		cursor = last
	}
	m.catalogCursor = max(0, min(cursor, last))
	m.catalogTop = max(0, min(m.catalogTop, m.catalogCursor))
	m.catalogTop = max(m.catalogTop, m.catalogCursor-page+1)
}

func (m *Model) renderCatalog(boxW int) string {
	entries := m.catalogResult.Entries
	prefix := ansi.Truncate(text.TUICatalogHeading(m.catalogQuery, len(entries)), boxW, "…") + "\n"
	if m.mode == modeCatalogSearch {
		prefix = m.catalogInputView() + "\n"
	} else if m.catalogResult.Stale != nil {
		stale := text.CatalogStale(m.catalogResult.Stale, m.catalogResult.CachedAt)
		prefix += strings.TrimSuffix(stale, "\n") + "\n"
	}
	if len(entries) == 0 {
		return prefix + m.styles.box.Width(boxW).Render(m.catalogEmpty())
	}
	items := make([]listItem, 0, len(entries))
	for _, e := range entries {
		it := listItem{name: text.TUICatalogName(e), icon: "●", style: m.styles.icons[RowUpdate], short: text.Installs(e.Installs)}
		switch {
		case e.Installed:
			it.icon, it.style, it.short = "●", m.styles.icons[RowOK], text.TUICatalogInstalled
		case !e.Installable:
			it.icon, it.style, it.short = "○", m.styles.muted, text.TUICatalogNotInstallable
		case e.PossibleDuplicate:
			it.icon, it.style = "⚠", m.styles.icons[RowWarn]
		}
		items = append(items, it)
	}
	list := func(width, height int) []string {
		m.catalogPage = max(1, height-1)
		return m.listLines(text.TUICatalogResults, items, m.catalogCursor, &m.catalogTop, width, height)
	}
	return m.split(prefix, max(20, m.width) >= wideLayout, list, m.catalogPanel)
}

func (m *Model) catalogInputView() string {
	// Display an escaped copy, but keep the actual query and its cursor intact.
	input := m.catalogInput
	value := input.Value()
	if safe := text.SafeLine(value); safe != value {
		runes := []rune(value)
		before := string(runes[:min(input.Position(), len(runes))])
		input.CharLimit = 0
		input.SetValue(safe)
		input.SetCursor(utf8.RuneCountInString(text.SafeLine(before)))
	}
	return input.View()
}

func (m *Model) catalogEmpty() string {
	actions := []string{text.TUICatalogActionOrigin}
	message := text.TUICatalogEmpty
	switch {
	case m.catalogLoading:
		message = text.TUICatalogLoading
	case m.catalogErr != nil:
		message = text.TUICatalogError(m.catalogErr)
		actions = append(actions, text.TUICatalogActionReload)
	case m.catalogQuery != "":
		actions = append(actions, text.TUICatalogActionBack)
	}
	return message + "\n\n" + strings.Join(actions, "   ")
}

func (m *Model) catalogPanel(width int) []string {
	e, ok := m.selectedCatalogEntry()
	if !ok {
		return nil
	}
	lines := []string{m.styles.title.Render(ansi.Truncate(text.TUICatalogName(e), width, "…"))}
	wrap := wrapper(width)
	for _, line := range text.TUICatalogDetails(e) {
		// Prefer moving the final value to its own line over splitting an
		// origin or install source at a hyphen when the value fits intact.
		if end := strings.LastIndexByte(line, ' '); end > 0 && end < len(line)-1 && ansi.StringWidth(line) > width {
			if ansi.StringWidth(line[:end]) <= width && ansi.StringWidth(line[end+1:]) <= width {
				line = line[:end] + "\n" + line[end+1:]
			}
		}
		lines = append(lines, wrap(line)...)
	}
	actions := []string{text.TUICatalogActionSearch, text.TUICatalogActionOrigin, text.TUICatalogActionReload}
	if !e.Installed && e.Installable {
		actions = append(actions, text.TUICatalogActionInstall)
	}
	return append(lines, m.actionLines(actions, width)...)
}
