package cli

// User-facing texts of the CLI, in neutral Spanish (S11). Command names, flag
// names and JSON keys stay in English; the Spanish names of states, issues
// and labels come from internal/entrypoints/text.
const (
	msgErrorPrefix = "skillhub: "

	// Help.
	usageTemplate = `Uso:{{if .Runnable}}
  {{.UseLine}}{{end}}{{if .HasAvailableSubCommands}}
  {{.CommandPath}} <comando>{{end}}{{if .HasExample}}

Ejemplos:
{{.Example}}{{end}}{{if .HasAvailableSubCommands}}

Comandos:{{range .Commands}}{{if (or .IsAvailableCommand (eq .Name "help"))}}
  {{rpad .Name .NamePadding }} {{.Short}}{{end}}{{end}}{{end}}{{if .HasAvailableLocalFlags}}

Opciones:
{{.LocalFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}{{if .HasAvailableInheritedFlags}}

Opciones globales:
{{.InheritedFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}{{if .HasAvailableSubCommands}}

Usa «{{.CommandPath}} <comando> --help» para ver la ayuda de un comando.{{end}}
`
	helpFlagUsage = "muestra esta ayuda"
	helpShort     = "Muestra la ayuda de un comando"

	rootShort = "Administra las skills de agentes que instalaste"
	rootLong  = `skillhub administra las skills de agentes que instalaste: las lista en un solo
inventario, detecta las actualizaciones upstream y las aplica solo cuando tú lo
apruebas. Sin argumentos, en una terminal, abre la interfaz de terminal.`

	tuiShort     = "Abre la interfaz de terminal"
	scanShort    = "Lista las skills instaladas"
	checkShort   = "Revisa si hay actualizaciones; nunca aplica nada"
	checkLong    = "Detecta los cambios upstream, prepara las actualizaciones y las etiqueta. Nunca aplica ni recupera nada. Una skill cuya carpeta ya no existe en el repositorio de origen queda al día, con ese problema, y no cuenta como fallo. Una skill instalada desde un sitio web (/.well-known/agent-skills/) todavía no se revisa: se indica como «origen web» y tampoco cuenta como fallo."
	updatesShort = "Lista, muestra, aplica u omite actualizaciones"
	listShort    = "Lista las actualizaciones pendientes y las bloqueadas"
	showShort    = "Muestra una actualización: sus etiquetas, sus archivos y su diff"
	showLong     = "Muestra una actualización: sus etiquetas, sus archivos y su diff.\n\n" +
		"Si editaste la skill, muestra dos diffs: tus cambios (de la versión registrada a la instalada) y " +
		"los del autor (de la versión registrada a upstream). Si la versión registrada ya no está en " +
		"upstream, muestra solo el diff de la instalada a upstream y lo indica. Aplicar reemplaza tus " +
		"cambios por la versión del autor.\n\n" +
		"Con --json, cada diff va en su propio campo: installed_to_upstream, registered_to_installed y " +
		"registered_to_upstream; el que no corresponde es null."
	applyShort      = "Aplica actualizaciones tras tu confirmación"
	applyLong       = "Muestra lo que va a aplicar y pide confirmación; la confirmación vale solo para los candidatos mostrados. --yes aprueba los candidatos pendientes en el momento de ejecutarse, sin mostrártelos antes. Sin terminal y sin --yes, se niega."
	skipShort       = "Omite la versión pendiente hasta que el autor publique otra"
	historyShort    = "Muestra lo que se aplicó, deshizo, omitió o recuperó"
	undoShort       = "Deshace una actualización aplicada"
	recoverShort    = "Restaura la versión anterior tras una operación interrumpida"
	recoverLong     = "Resuelve una operación interrumpida: la revierte si no llegó a completarse o termina de cerrarla si ya se había completado. Sin ninguna operación pendiente, borra los restos incompletos: snapshots sin terminar y la copia de una instalación que se cortó."
	settingsShort   = "Muestra o cambia los ajustes"
	settingsShowS   = "Muestra los ajustes"
	settingsSetS    = "Cambia un ajuste: check_interval, notifications o ignore"
	backgroundShort = "Estado, activación o desactivación de la revisión en segundo plano"
	bgStatusShort   = "Muestra si la revisión en segundo plano está activa"
	bgEnableShort   = "Activa la revisión en segundo plano (timer de usuario de systemd)"
	bgDisableShort  = "Desactiva la revisión en segundo plano y borra sus unidades"

	flagJSON     = "salida en JSON"
	flagIfDue    = "revisa solo si ya pasó check_interval desde la última revisión exitosa"
	flagNotify   = "envía una notificación de escritorio cuando hay algo nuevo"
	flagExitCode = "termina con código 1 si hay actualizaciones pendientes"
	flagYes      = "aprueba sin preguntar los candidatos pendientes al ejecutarse"
	flagDryRun   = "muestra el plan sin cambiar nada"
	flagAll      = "aplica todas las pendientes, salvo las modificadas localmente y las bloqueadas"
	flagTo       = "snapshot que se restaura, en lugar del de la última aplicación"

	// Usage errors.
	msgUnknownCommand = "comando desconocido «%s»; usa «skillhub --help»"
	msgBadFlag        = "%s; usa «%s --help»"
	msgArgsExact      = "«%s» necesita %d argumento(s) y recibió %d"
	msgArgsMin        = "«%s» necesita al menos %d argumento(s)"
	msgApplyWhat      = "indica una o varias skills, o --all"
	msgApplyBoth      = "indica skills o --all, no ambos"

	// Errors.
	msgError             = "error: %v"
	msgCompose           = "no se pudo preparar skillhub: %v"
	msgInterruptedRefuse = "hay una operación interrumpida; ejecuta «skillhub recover» antes de continuar"
	msgStateLocked       = "otro proceso de skillhub está trabajando; vuelve a intentarlo en un momento"
	msgAmbiguous         = "«%s» corresponde a varias skills; indica una por su id:"
	msgNotFound          = "no hay ninguna skill «%s»"
	msgNoCandidate       = "«%s» no tiene ninguna actualización pendiente"
	msgBlocked           = "la actualización de %s está bloqueada: %s"
	msgNeedsYes          = "sin una terminal, confirma con --yes; no se aplicó nada"
	msgNotConfirmed      = "no se confirmó; no se cambió nada"
	msgSomeNotApplied    = "alguna actualización no se aplicó"
	msgCheckFailed       = "la revisión falló en algún destino"
	msgUnknownSetting    = "«%s» no es un ajuste; los ajustes son check_interval, notifications e ignore"
	msgInvalidSetting    = "valor no válido para %s: «%s»"
	msgIntervalTooShort  = "el intervalo mínimo es 15m"
	msgUndoNeedsTerminal = "confirma el deshacer en una terminal; no se restauró nada"
	msgNothingToUndo     = "no hay ninguna aplicación de %s que deshacer"
	msgSnapshotNotFound  = "no existe ese snapshot de %s"
	msgNoExecutable      = "no se pudo determinar la ruta de skillhub"

	// Warnings.
	msgInterruptedWarning = "aviso: hay una operación interrumpida (%s de %s, iniciada el %s). Ejecuta «skillhub recover» para restaurar la versión anterior.\n"

	// Common.
	msgDryRun    = "Simulación: no se cambió nada.\n"
	msgConfirmYN = " [s/N] "
	msgNone      = "—"
	msgYes       = "sí"
	msgNo        = "no"
	msgStore     = "almacén"

	// scan.
	msgNoSkills   = "No se encontró ninguna skill.\n"
	hdrSkills     = "NOMBRE\tORIGEN\tHARNESS\tESTADO\tPROBLEMAS\n"
	fmtSkillRow   = "%s\t%s\t%s\t%s\t%s\n"
	fmtSkillAlias = "%s (también %s)"

	// check.
	msgNotDue      = "La revisión todavía no toca: la última revisión exitosa fue el %s y el intervalo es %s.\n"
	fmtCheckTarget = "  %s\t%s\n"
	msgPending     = "Actualizaciones pendientes: %d\n"
	msgOutcomeUp   = "al día"
	// fmtOutcomeRemoved is "al día" followed by the removed_upstream issue.
	fmtOutcomeRemoved = "%s (%s)"
	msgOutcomeAv      = "actualización disponible"
	msgOutcomeBl      = "bloqueada"
	msgOutcomeSk      = "omitida"
	msgOutcomeFail    = "revisión fallida: %v"
	msgOutcomeNo      = "no se revisa"

	// Notifications.
	notifyTitle       = "skillhub"
	notifyNew         = "Hay actualizaciones nuevas: %s. Revísalas con «skillhub updates list»."
	notifyFailed      = "La revisión de actualizaciones falló. Ejecuta «skillhub check» para ver el motivo."
	notifyInterrupted = "Hay una actualización interrumpida. Ejecuta «skillhub recover» para restaurar la versión anterior."

	// updates list and show.
	msgNoUpdates          = "No hay actualizaciones pendientes.\n"
	hdrUpdates            = "SKILL\tCANDIDATO\tVERSIÓN\tESTADO\tETIQUETAS\n"
	fmtUpdateRow          = "%s\t%s\t%s\t%s\t%s\n"
	fmtShowHeader         = "Skill:      %s\nCandidato:  %s\nDe:         árbol %s\nA:          commit %s, árbol %s\nEstado:     %s\nEtiquetas:  %s\n"
	fmtShowBlocked        = "Motivo:     %s\n"
	msgShowLocal          = "Esta skill tiene cambios tuyos: aplicar la reemplaza por la versión del autor. Tus cambios se conservan en un snapshot.\n"
	msgShowFiles          = "\nArchivos:\n"
	fmtShowSection        = "\n== %s ==\nArchivos:\n"
	msgShowUser           = "Tus cambios (registrada → instalada)"
	msgShowAuthor         = "Cambios del autor (registrada → upstream)"
	msgShowInstalled      = "instalada → upstream"
	msgShowRegisteredGone = "La versión registrada ya no está en upstream: solo se muestra el diff de instalada a upstream.\n"
	fmtShowFile           = "  %s\t%s\n"
	msgShowBinary         = "(archivo binario: sin diff de texto)\n"

	// updates apply.
	msgPlanHeader     = "Se va a aplicar:\n"
	fmtPlanRow        = "  %s\tcandidato %s\tversión %s (árbol %s)\tetiquetas: %s\n"
	msgExcludedHeader = "Quedan fuera:\n"
	fmtExcludedLocal  = "  %s\tmodificada localmente; se aplica de una en una\n"
	fmtExcludedBlock  = "  %s\tbloqueada: %s\n"
	msgNothingToApply = "No hay actualizaciones para aplicar.\n"
	msgConfirmApply   = "¿Aplicar estas actualizaciones?"
	fmtApplied        = "aplicada: %s (snapshot %s)\n"
	fmtRejected       = "rechazada: %s: %v; vuelve a revisarla\n"
	fmtFailed         = "fallida: %s: %v\n"
	fmtExcluded       = "fuera: %s: modificada localmente; se aplica de una en una\n"

	// updates skip.
	fmtSkipPlan = "Se omitiría la versión %s de %s.\n"
	fmtSkipped  = "Versión %s de %s omitida: no se mostrará hasta que el autor publique otra.\n"

	// history.
	msgNoHistory = "El historial está vacío.\n"
	hdrHistory   = "FECHA\tACCIÓN\tSKILL\tDE\tA\tSNAPSHOT\tDETALLE\n"
	fmtHistory   = "%s\t%s\t%s\t%s\t%s\t%s\t%s\n"

	// undo.
	fmtUndoPlan     = "Se restauraría %s desde %s.\n"
	msgUndoLatest   = "el snapshot de su última aplicación"
	fmtUndoSnapshot = "el snapshot %s"
	fmtUndoChanged  = "La carpeta de %s cambió después de aplicar; se guardó una copia de su estado actual en el snapshot %s.\n"
	msgConfirmUndo  = "¿Deshacer de todos modos?"
	fmtUndone       = "Deshecha la actualización de %s: se restauró el snapshot %s.\n"
	fmtDetail       = "Nota: %s\n"
	fmtRecoverPlan  = "Se restauraría %s desde el snapshot %s (%s iniciada el %s).\n"
	// msgRecoverWouldClear is what recover --dry-run adds when the install
	// staging holds copies no operation owns.
	msgRecoverWouldClear = "Se eliminaría la copia de una instalación sin terminar.\n"

	// settings.
	fmtSettings = "Intervalo de revisión (check_interval): %s\nNotificaciones (notifications): %s\nIgnoradas (ignore): %s\n"
	msgSaved    = "Ajustes guardados.\n"

	// background.
	fmtBgStatus     = "Revisión en segundo plano: %s\nUnidades instaladas: %s\nTimer activo: %s\n"
	msgBgOn         = "activada"
	msgBgOff        = "desactivada"
	msgBgHowTo      = "Para activarla, ejecuta «skillhub background enable».\n"
	fmtBgEnablePlan = "Se escribirían skillhub-check.service y skillhub-check.timer en %s, apuntando a %s, y se activaría el timer.\n"
	msgBgEnabled    = "Revisión en segundo plano activada: el timer se dispara cada 15 minutos y revisa según check_interval.\n"
	msgBgDisablePl  = "Se desactivaría el timer y se borrarían sus unidades.\n"
	msgBgDisabled   = "Revisión en segundo plano desactivada.\n"
)

// flagErrorPrefixes are pflag's error prefixes and their Spanish text.
var flagErrorPrefixes = [][2]string{
	{"unknown flag: ", "opción desconocida: "},
	{"unknown shorthand flag: ", "opción corta desconocida: "},
	{"flag needs an argument: ", "la opción necesita un valor: "},
	{"invalid argument ", "valor no válido "},
	{"bad flag syntax: ", "opción mal escrita: "},
}
