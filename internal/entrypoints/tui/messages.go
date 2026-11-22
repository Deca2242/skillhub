package tui

// User-facing texts of the TUI, in neutral Spanish (S11).
const (
	appTitle = "skillhub"

	colSkillName   = "Nombre"
	colSkillOrigin = "Origen"
	colSkillHarns  = "Agentes"
	colSkillState  = "Estado"
	colSkillIssues = "Problemas"

	colUpdTarget = "Destino"
	colUpdID     = "Candidato"
	colUpdCommit = "Commit"
	colUpdState  = "Estado"
	colUpdLabels = "Etiquetas"

	colHistAt       = "Fecha"
	colHistAction   = "Acción"
	colHistTarget   = "Destino"
	colHistVersions = "Versiones"
	colHistSnapshot = "Snapshot"
	colHistDetail   = "Detalle"

	msgEmpty            = "ninguno"
	msgYes              = "sí"
	msgNo               = "no"
	msgNoSkills         = "No se encontraron skills instaladas."
	msgNoUpdates        = "No hay actualizaciones pendientes."
	msgNoHistory        = "No hay entradas en el historial."
	msgFilterPrompt     = "Filtro: "
	msgChecking         = "Revisando actualizaciones..."
	msgApplying         = "Aplicando actualización..."
	msgApplyingAll      = "Aplicando actualizaciones..."
	msgSkipping         = "Omitiendo actualización..."
	msgUndoing          = "Deshaciendo aplicación..."
	msgRecovering       = "Recuperando operación interrumpida..."
	msgLoadingDiff      = "Cargando diff..."
	msgLoadingData      = "Cargando datos..."
	msgLoadFailed       = "No se pudieron cargar los datos; el motivo aparece abajo."
	msgRejectedReplaced = "Aprobación rechazada: el candidato %s cambió durante la revisión. Se cargó el candidato actual."
	msgBlockedNoApply   = "El candidato %s está bloqueado (%s) y no se puede aplicar."
	msgExcludedLocal    = "modificada localmente (aplicar una por una)"
	msgLocallyModified  = "Aviso: esta skill fue modificada localmente. Aplicar reemplaza tus cambios con la versión del autor (se guarda una copia permanente)."

	msgDiffLabel          = "Vista: instalada → upstream"
	msgDiffUserLabel      = "Tus cambios (registrada → instalada)"
	msgDiffAuthorLabel    = "Cambios del autor (registrada → upstream)"
	msgDiffRegisteredGone = "La versión registrada ya no está en upstream:\nsolo se muestra el diff de instalada a upstream."
	msgDiffLocalNotice    = "Esta skill tiene cambios locales: aplicar la actualización\nlos reemplaza con la versión del autor."
	msgDiffBinary         = "(archivo binario)"
	msgDiffEmpty          = "(sin diferencias de texto)"

	msgErrStateLocked       = "Otro proceso de skillhub está trabajando; vuelve a intentarlo en un momento."
	msgErrInterrupted       = "Hay una operación interrumpida; recupérala antes de continuar."
	msgErrInstalledChanged  = "Aprobación rechazada: la carpeta instalada cambió."
	msgErrStagedChanged     = "Aprobación rechazada: la copia preparada cambió."
	msgErrCandidateReplaced = "Aprobación rechazada: el candidato cambió durante la revisión."
	msgErrRejected          = "Aprobación rechazada."
	msgErrNothingToUndo     = "No hay ninguna aplicación que deshacer."
	msgErrSnapshotNotFound  = "No existe ese snapshot."
	msgErrAmbiguous         = "«%s» corresponde a varias skills: %s"
	msgErrUnknownSetting    = "Ajuste desconocido."
	msgErrGeneric           = "Ocurrió un error inesperado al ejecutar la operación."

	msgIgnoreComma = "No se puede ignorar desde aquí un nombre con coma; añádelo en settings.toml."

	msgBgOn  = "activada"
	msgBgOff = "desactivada"

	msgCmdEnable  = "skillhub background enable"
	msgCmdDisable = "skillhub background disable"

	msgIntervalTooShort = "El intervalo mínimo es de 15 minutos."
	msgInvalidInterval  = "Intervalo inválido (ejemplo: 1h, 30m)."
	msgSettingsSaved    = "Ajustes guardados."
	msgIgnoredAdded     = "Se añadió %q a la lista de ignorados."

	msgOutcomeApplied  = "aplicada"
	msgOutcomeRejected = "rechazada (revisar de nuevo)"
	msgOutcomeFailed   = "fallida y revertida"
	msgOutcomeExcluded = "excluida"

	msgConfirmApplyPrompt    = "¿Aplicar esta actualización? (s/n)"
	msgConfirmApplyAllPrompt = "¿Aplicar las actualizaciones incluidas? (s/n)"
	msgConfirmUndoPrompt     = "La carpeta cambió después de aplicar. Se guardó una copia en %s. ¿Deshacer de todos modos? (s/n)"
	msgRecoverPrompt         = "Pulsa r o enter para recuperar la versión anterior, o q para salir."

	msgInterruptedTitle = "Operación interrumpida detectada"
	msgInterruptedNote  = "La TUI no repara operaciones automáticamente. Recupera la versión anterior antes de continuar."

	msgHelpTitle = "Atajos de teclado"
	msgHelpClose = "Pulsa ? o esc para cerrar la ayuda."

	msgTUINilApp = "skillhub: la aplicación no se pudo inicializar.\n"
)
