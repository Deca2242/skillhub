package text

// Hidden-field labels shown in the Skills panel when an active search query
// matches a field not visible in the list row (INV2, A29).
const (
	SearchFieldDescription = "descripción"
	SearchFieldAlias       = "alias"
	SearchFieldRepo        = "repositorio"
	SearchFieldRemote      = "remoto"
)

// TUISearchMatch formats the plain-language explanation of which non-visible
// fields matched the active search query (INV2, A29).
func TUISearchMatch(fields []string) string {
	if len(fields) == 0 {
		return ""
	}
	return "Coincide en: " + JoinNames(fields)
}
