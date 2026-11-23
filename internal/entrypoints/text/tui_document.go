package text

import (
	"fmt"
	"strings"

	"skillhub/internal/app"
	"skillhub/internal/shared/ports"
)

// Texts for the read-only local SKILL.md document view (INV7, S15 #2, S11).
// Every third-party value passes through SafeLine or Safe (A28).
const (
	TUIKeysSkillDetail = "v ver SKILL.md • enter/esc volver"
	TUIKeysDocument    = "↑/↓ desplazar • PgUp/PgDn página • esc volver"

	TUIDocumentMissing       = "No se encontró el archivo SKILL.md en la carpeta instalada."
	TUIDocumentNoDescription = "Sin descripción en el frontmatter."
	TUIDocumentNoCompanions  = "Archivos acompañantes: ninguno"
)

// TUIDocumentTitle formats the header of the local document view.
func TUIDocumentTitle(skillName string) string {
	return fmt.Sprintf("Uso local: %s", SafeLine(skillName))
}

// TUIDocumentCompanionsHeader formats the companion files section heading.
func TUIDocumentCompanionsHeader(count int) string {
	return fmt.Sprintf("Archivos acompañantes (%d):", count)
}

// TUIDocumentTruncated formats the truncation notice when SKILL.md exceeds the cap.
func TUIDocumentTruncated(shownBytes, originalBytes int) string {
	return fmt.Sprintf("Contenido truncado: se muestran %d de %d bytes.", shownBytes, originalBytes)
}

// TUIDocumentScrollHint formats the scroll position hint at the bottom of the view.
func TUIDocumentScrollHint(from, to, total int) string {
	return fmt.Sprintf("Líneas %d–%d de %d • ↑/↓ desplazar • esc volver", from, to, total)
}

// TUIDocumentFormat formats a SkillDocument in neutral Spanish with every
// third-party field sanitized against terminal control and bidi sequences.
func TUIDocumentFormat(doc app.SkillDocument) string {
	var b strings.Builder

	titleName := doc.SkillName
	if titleName == "" {
		titleName = doc.Frontmatter.Name
	}
	fmt.Fprintf(&b, "%s\n", TUIDocumentTitle(titleName))

	if doc.Dir != "" {
		fmt.Fprintf(&b, "Ruta: %s\n", SafeLine(doc.Dir))
	} else {
		b.WriteString("Ruta: sin carpeta instalada\n")
	}

	b.WriteString("\nFrontmatter:\n")
	fmName := "—"
	if doc.Frontmatter.Name != "" {
		fmName = SafeLine(doc.Frontmatter.Name)
	}
	fmt.Fprintf(&b, "  Nombre: %s\n", fmName)

	if doc.Frontmatter.Description != "" {
		fmt.Fprintf(&b, "  Descripción: %s\n", SafeLine(doc.Frontmatter.Description))
	} else {
		fmt.Fprintf(&b, "  Descripción: %s\n", TUIDocumentNoDescription)
	}

	if doc.Frontmatter.Author != "" {
		fmt.Fprintf(&b, "  Autor: %s\n", SafeLine(doc.Frontmatter.Author))
	}

	b.WriteString("\n")
	if len(doc.Companions) == 0 {
		b.WriteString(TUIDocumentNoCompanions + "\n")
	} else {
		fmt.Fprintf(&b, "%s\n", TUIDocumentCompanionsHeader(len(doc.Companions)))
		for _, c := range doc.Companions {
			suffix := ""
			switch c.Kind {
			case ports.EntryDir:
				suffix = "/"
			case ports.EntryBrokenLink:
				suffix = " (enlace roto)"
			}
			fmt.Fprintf(&b, "  - %s%s\n", SafeLine(c.Name), suffix)
		}
	}

	b.WriteString("\nSKILL.md:\n")
	if doc.Missing {
		b.WriteString(TUIDocumentMissing + "\n")
	} else {
		content := Safe(doc.Content)
		b.WriteString(content)
		if !strings.HasSuffix(content, "\n") {
			b.WriteString("\n")
		}
		if doc.Truncated {
			fmt.Fprintf(&b, "\n%s\n", TUIDocumentTruncated(doc.ShownBytes, doc.OriginalBytes))
		}
	}

	return b.String()
}
