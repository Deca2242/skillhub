# skillhub

skillhub te muestra qué skills tienen tus agentes de IA y te deja revisar cada cambio antes de
aplicarlo, sin recorrer a mano la carpeta de cada agente.

Una *skill* es una carpeta con instrucciones para un agente (un archivo `SKILL.md`) y, a veces, scripts u
otros archivos que el agente usa para una tarea. Si instalaste skills para Claude Code, Codex, OpenCode,
Pi u otro de los más de 70 agentes que skillhub reconoce (Gemini CLI, GitHub Copilot y Cursor, entre
otros), skillhub las reúne en un solo inventario y te dice qué agentes ven cada una.

Es una app de terminal, con una interfaz interactiva y una línea de comandos. Con ella puedes:

- ver tus skills y filtrarlas por agente;
- encontrar enlaces rotos y copias duplicadas, y repararlos;
- revisar las actualizaciones publicadas en GitHub y aplicar solo las que apruebes;
- instalar, activar, desactivar, enlazar y eliminar skills, con el plan a la vista y la opción de
  deshacer.

skillhub nunca cambia tus skills por su cuenta: ni la revisión de actualizaciones ni la revisión en
segundo plano aplican nada. Qué pide confirmación y qué se puede deshacer está en
[Aprobación, deshacer y recuperar](#aprobación-deshacer-y-recuperar).

*Así se ve la pestaña Skills (ejemplo recortado, con datos de prueba):*

```text
skillhub   1 Skills (17)  2 Actualizar  3 Instalar  4 Salud (26)   h historial • c ajustes
17 skills · 12 activas · 5 desactivadas · 0 con actualización · 11 con problemas
╭──────────────────────────────────────╮╭──────────────────────────────────────────────────────────╮
│ Skills (17)                          ││ accessibility                                            │
│ ⚠ accessibility          enlace roto ││ La desactivaste a mano moviéndola a OFF/, pero Codex,    │
│ ● agent-browser          sin revisar ││ OpenCode y Pi la siguen viendo porque buscan en          │
│ ⚠ brainstorming               manual ││ subcarpetas.                                             │
│ ⚠ computer-use            modificada ││ Recomendado: [o] Desactivarla de verdad                  │
│ ⚠ domain-driven-design   enlace roto ││                                                          │
│ ● grill-me               sin revisar ││ Disponible en: Codex, OpenCode y Pi                      │
│ ● grill-with-docs        sin revisar ││ Origen: owner/accessibility                              │
│ ⚠ humanizer                   manual ││ Tiene un enlace que no lleva a ninguna parte.            │
╰──────────────────────────────────────╯╰──────────────────────────────────────────────────────────╯
o desactivarla de verdad • enter ver todo • x ignorar • / buscar • f filtrar • g ver por agente • ?
ayuda • q salir
```

## Instalar y probar

Necesitas Go 1.27. Clona o descarga este repositorio y, desde su carpeta raíz, compila y lista tus
skills:

```bash
go build -o bin/skillhub ./cmd/skillhub
./bin/skillhub scan
```

`scan` lista tus skills sin instalar ni cambiar nada. Para empezar no hace falta configurar nada más: ni
`gh`, ni el catálogo, ni la revisión en segundo plano.

Opcional: copia el binario a una carpeta de tu `PATH`, por ejemplo con
`install -m 0755 bin/skillhub ~/.local/bin/skillhub`. En el resto de este README, `skillhub` es ese
binario; si no lo copiaste, escribe `./bin/skillhub`.

### Tres recorridos que no cambian nada

1. **Ver qué skills tienes.** Ejecuta `skillhub` en una terminal: se abre la interfaz interactiva, y su
   pestaña Skills las lista todas, con su estado y los agentes que las ven. `skillhub scan` da la misma
   información en una tabla (`--json` para scripts).
2. **Filtrar por agente.** En la pestaña Skills, `g` elige un agente y deja solo las skills que ese
   agente ve; `f` filtra por estado.
3. **Entender un problema.** La pestaña Salud (`4`) explica cada hallazgo (enlaces rotos, duplicados,
   copias que difieren, skills que un agente sigue viendo aunque estén desactivadas) y qué reparación
   propone. Nada se repara sin tu confirmación. Desde la línea de comandos: `skillhub doctor`.

En la interfaz, `?` muestra las teclas de la pestaña en la que estás y `q` sale. Cada comando tiene su
ayuda con `--help`.

## Qué puedes hacer

| Quieres… | En la interfaz interactiva | En la línea de comandos |
|---|---|---|
| Ver tus skills y qué agentes las ven | pestaña **1 Skills**: `g` filtra por agente, `f` por estado y `/` busca | `skillhub scan` y `skillhub skills show <skill>` |
| Saber si hay actualizaciones y aplicarlas | pestaña **2 Actualizar**: `enter` muestra los cambios y `a` aplica; `r` revisa ahora | `skillhub check` y `skillhub updates` |
| Instalar, activar, desactivar, enlazar o eliminar una skill | pestaña **1 Skills** (`d` desactiva, `a` activa, `e` elimina, `l` elige agentes) y pestaña **3 Instalar** | `skillhub skills` |
| Encontrar y reparar enlaces rotos y duplicados | pestaña **4 Salud**: `enter` muestra cómo repararlo | `skillhub doctor` y `skillhub doctor fix` |
| Buscar skills en skills.sh, el catálogo público | no está en la interfaz | `skillhub skills search` y `skillhub skills popular` |
| Ver lo que se hizo y deshacerlo | `h` abre el Historial; `z` deshace lo último | `skillhub history` y `skillhub undo` |
| Recibir un aviso cuando haya actualizaciones | `c` abre los Ajustes, que dicen si está activa | `skillhub background enable` (opcional) |

### Ejemplo: consultar el detalle de una skill

```bash
skillhub skills show agent-browser         # estado, agentes que la ven y por qué, origen y actividad
skillhub skills show agent-browser --json  # lo mismo en JSON, sin escapar
```

### Ejemplo: revisar una actualización antes de aplicarla

```bash
skillhub check                  # busca actualizaciones en GitHub; no aplica nada
skillhub updates list           # las pendientes y las bloqueadas
skillhub updates show <skill>   # sus etiquetas de riesgo, sus archivos y su diff
```

Hasta aquí ninguna skill cambió: `check` prepara cada actualización en una carpeta propia de skillhub y
guarda lo que encontró en su estado. El diff son las líneas que cambian. Las etiquetas de riesgo avisan,
por ejemplo, de que una actualización «toca scripts», pide «permisos nuevos» o trae «texto
sospechoso»; informan, pero no deciden. Si la actualización te convence,
`skillhub updates apply <skill>` muestra lo que va a aplicar y pide confirmación, y
`skillhub undo <skill>` la deshace. `check` necesita `gh` con una sesión iniciada.

## Compatibilidad y límites

**Estado.** Están terminadas la versión 1 (ver y actualizar) y la 2 (gestionar las skills de todos los
agentes desde un solo lugar). skillhub se desarrolla y se prueba en Linux.

**Dónde busca.** En el almacén, `~/.agents/skills` (la carpeta compartida donde instala `npx skills`), y
en el directorio global de skills de cada agente que conoce. Su tabla de agentes viene de la
documentación de `npx skills`; si en tu máquina un agente guarda sus skills en otro lugar, corrígelo como
se explica [más abajo](#si-un-agente-guarda-sus-skills-en-otro-lugar).

**Qué deja fuera.** Las skills que trae o gestiona otra herramienta (los plugins de Claude Code, las
carpetas propias de algunos agentes, como Cursor y Codex, y las copias que gestiona gentle-ai), las de
nivel de proyecto (salvo para eliminar copias independientes del proyecto actual, como se detalla más abajo) y lo que pongas en la lista
`ignore` de los [ajustes](#ajustes).

**De dónde viene cada skill.** skillhub reconoce tres orígenes, y solo los dos primeros reciben
actualizaciones:

| Origen | Cómo lo reconoce | Actualizaciones |
|---|---|---|
| Instalada con `npx skills` o con skillhub | Una entrada en el lock de `npx skills`: el archivo donde `npx skills` registra lo que instaló, `~/.agents/.skill-lock.json` (o `$XDG_STATE_HOME/skills/.skill-lock.json`) | Se revisan en GitHub. Las instaladas desde un sitio web todavía no se revisan |
| Clonada con git | Una carpeta con `.git` y un remoto de GitHub | Se revisan en GitHub y se aplican solo si el clon está limpio y puede avanzar sin fusionar (fast-forward); si no, la actualización queda bloqueada y el clon no se toca. Un clon que no sigue una rama remota no se revisa |
| Sin origen | Cualquier otra carpeta con un `SKILL.md` | No hay con qué compararla |

**Herramientas que usa.** Ver tus skills solo usa `git`, y solo para las carpetas que son clones de git.
Las demás funciones usan estas herramientas:

| Herramienta | Para qué |
|---|---|
| `gh`, con una sesión iniciada (`gh auth login`) | Revisar actualizaciones e instalar o reinstalar desde GitHub; toda consulta a GitHub pasa por `gh` |
| `git` | Reconocer, revisar y actualizar las skills que clonaste con git |
| `curl` | Buscar en skills.sh e instalar desde un sitio web |
| `systemctl --user` (systemd) | La revisión en segundo plano |
| `notify-send` | Las notificaciones de escritorio |

### Si un agente guarda sus skills en otro lugar

La tabla de agentes trae el directorio global de skills de cada uno. Si en tu máquina un agente usa
otro, corrígelo en `~/.config/skillhub/agents.local.toml` (o bajo `XDG_CONFIG_HOME`). Cada tabla
`[[agent]]` lleva `id` y `global_dir`, y opcionalmente `reads_universal` (`true` si el agente también lee
el almacén, `~/.agents/skills`). Un `id` que ya está en la tabla corrige ese agente y uno nuevo lo
añade. Un archivo inválido se rechaza nombrando la clave.

```toml
# Codex guarda sus skills en ~/.codex/skills y también lee ~/.agents/skills.
[[agent]]
id = "codex"
global_dir = "~/.codex/skills"
reads_universal = true

# Pi vive en ~/.pi/agent.
[[agent]]
id = "pi"
global_dir = "~/.pi/agent/skills"
reads_universal = false

# Kilo vive en ~/.kilocode.
[[agent]]
id = "kilo"
global_dir = "~/.kilocode/skills"
reads_universal = false
```

El ejemplo completo y comentado, con los campos opcionales de la versión 2, está en
[`internal/data/agents.local.example.toml`](internal/data/agents.local.example.toml).

## Aprobación, deshacer y recuperar

Ninguna revisión, temporizador ni notificación cambia tus skills: solo cambian cuando tú ejecutas una
acción. Lo que varía según el comando es cómo la apruebas.

| Cómo se aprueba | Comandos | Qué ves antes |
|---|---|---|
| Confirmas en la terminal | `updates apply`; `skills install`, `remove`, `disable`, `enable`, `link`, `unlink` y `adopt-off`; `doctor fix`; `undo v2:<acción>` | `updates apply` muestra cada actualización, la versión a la que lleva y sus etiquetas de riesgo. Los demás muestran su plan: los pasos, los agentes afectados y un identificador |
| Apruebas con opciones, sin pregunta | `updates apply --yes`; `--yes --plan <id>` en los comandos de gestión | `updates apply --yes` aprueba las actualizaciones pendientes en ese momento, sin mostrártelas. `--yes --plan <id>` aprueba solo el plan que viste antes (por ejemplo, con `--dry-run`), y se niega si cambió |
| Actúa al ejecutarlo | `updates skip`, `undo <skill>` de una actualización, `recover`, `settings set`, `background enable` y `background disable` | Nada, salvo que uses `--dry-run`, que todos admiten. `undo` pide una confirmación aparte si la carpeta cambió después de aplicar |

`--dry-run` muestra lo que haría el comando sin cambiar nada. Sin una terminal, los comandos que piden
confirmación se niegan, salvo que los apruebes con opciones. Los demás comandos (`scan`, `check`,
`updates list`, `updates show`, `history`, `doctor`, `settings show`, `background status`,
`skills search` y `skills popular`) no cambian tus skills; algunos guardan datos propios de skillhub,
como el resultado de una revisión o una caché.

En la interfaz interactiva, aplicar, deshacer y cada operación de gestión muestran un resumen y esperan
tu confirmación (`s` o `enter`; `n` o `esc` cancela). Omitir una versión (`o`), ignorar una skill (`x`)
y cambiar un ajuste actúan al instante y no tocan los archivos de ninguna skill.

### Deshacer

Antes de aplicar una actualización o una operación de gestión, skillhub guarda una copia de respaldo de
lo que va a cambiar; en la ayuda y en la salida se llama *snapshot*. Con ella se deshacen:

- **Una actualización aplicada:** `skillhub undo <skill>`; `--to <snapshot>` elige otra copia en lugar
  de la de la última aplicación. Si la carpeta cambió después de aplicar, `undo` guarda antes una copia
  de su estado actual y pide una confirmación aparte.
- **Una operación de gestión** (instalar, eliminar, activar, desactivar, enlazar, quitar un enlace o
  reparar): al terminar, cada una dice cómo deshacerla, por ejemplo
  `skillhub undo v2:disable --to <snapshot>`. Ese deshacer muestra su plan, se aprueba igual que la
  operación y se niega si algo cambió después (y en una skill de proyecto, exige volver a abrir skillhub
  desde ese mismo proyecto). Un deshacer no se puede deshacer.

`skillhub history` lista lo que se aplicó, deshizo, omitió o recuperó. En la interfaz, `z` deshace lo
último que hiciste en la sesión y, en el Historial (`h`), `u` deshace la operación elegida.

### Si una operación se interrumpe

Si skillhub se corta a mitad de una escritura, deja un registro de la operación y no la repara solo:

- aplicar, omitir, deshacer y las operaciones de gestión se niegan, y lo explican, hasta que ejecutes
  `skillhub recover`; si lo que se cortó fue una actualización o un deshacer, además todo comando lo
  avisa;
- al abrirse, la interfaz interactiva lo dice y no muestra nada más hasta resolverlo: `r` o `enter`
  ejecuta el mismo `recover`, y `q` sale.

`recover` resuelve la operación: la revierte si no llegó a completarse o termina de cerrarla si ya se
había completado. Sin ninguna operación pendiente, borra los restos incompletos: copias de respaldo sin
terminar y la copia de una instalación que se cortó. `skillhub recover --dry-run` dice qué haría.

### No uses `npx skills` al mismo tiempo

`npx skills add`, `update` y `remove` reescriben el lock de `npx skills` sin ninguna protección, y
skillhub no puede impedirlo. No los ejecutes mientras skillhub cambia tus skills (al aplicar, deshacer,
recuperar o gestionar una). Al aplicar o deshacer una actualización, skillhub vuelve a leer el lock antes
de escribir y revierte si cambió; pero si `npx skills` escribe después a partir de una lectura anterior,
la skill aparecerá como modificada localmente.

## Referencia

Los detalles de cada parte, para cuando los necesites.

### Comandos

```text
skillhub                                    abre la interfaz interactiva, en una terminal
skillhub tui                                abre la interfaz interactiva
skillhub scan [--json]                      lista las skills instaladas
skillhub check [--if-due] [--notify] [--exit-code] [--json]
                                            revisa si hay actualizaciones; nunca aplica nada
skillhub updates list [--json]              actualizaciones pendientes y bloqueadas
skillhub updates show <skill> [--json]      etiquetas, archivos y diff de una actualización
skillhub updates apply <skill>... | --all [--yes] [--dry-run]
                                            aplica actualizaciones tras tu confirmación
skillhub updates skip <skill> [--dry-run]   omite esa versión hasta que el autor publique otra
skillhub history [--json]                   lo que se aplicó, deshizo, omitió o recuperó
skillhub undo <skill> [--to <snapshot>] [--dry-run] [--yes --plan <id>]
                                            deshace una actualización o una operación de gestión
skillhub recover [--dry-run]                resuelve una operación interrumpida
skillhub settings show                      muestra los ajustes
skillhub settings set <key> <value> [--dry-run]
                                            cambia check_interval, notifications o ignore
skillhub background status                  dice si la revisión en segundo plano está activa
skillhub background enable [--dry-run]      activa la revisión en segundo plano
skillhub background disable [--dry-run]     la desactiva y borra sus unidades de systemd

skillhub skills show <skill> [--json]                  estado, agentes que la ven, origen y actividad
skillhub skills install <origen> [--to <harness>...]   instala desde GitHub o un sitio web tras revisarla
skillhub skills remove <skill>                         elimina una skill independiente (global o del proyecto actual)
skillhub skills disable <skill>                        la desactiva en todos sus destinos gestionados
skillhub skills enable <skill>                         la activa con los enlaces que tenía
skillhub skills link <skill> --to <harness>            la enlaza en un agente
skillhub skills unlink <skill> --from <harness>        quita su enlace en un agente
skillhub skills adopt-off                              adopta como desactivadas las skills de ~/.agents/skills/OFF/
skillhub doctor                                        lista los hallazgos; solo lee
skillhub doctor fix <hallazgo> [--repair <tipo>] [--canonical <ruta>] [--to <harness>]

skillhub skills search <consulta> [--limit n] [--json] [--refresh]   busca en skills.sh
skillhub skills popular [--limit n] [--json] [--refresh]             las más instaladas, por temas
```

Los comandos de gestión (el segundo grupo, salvo `doctor`) admiten además `--dry-run` y
`--yes --plan <id>`. `<harness>` es el `id` de un agente, como `claude-code` u `opencode`:
`skillhub scan` los muestra. En `skills install` y `doctor fix`, `--to` se puede repetir o separar con
comas.

### Cómo se indica una skill

`<skill>` es el nombre de la skill o su `id`: `lock:<nombre>`, `parked:<clave>`, `git:<ruta>` o
`dir:<ruta>`, con la ruta relativa al home. Si un nombre corresponde a más de una skill, el comando se
niega y lista los `id`: vuelve a ejecutarlo con el `id` que quieras. En `skills remove` puedes indicar la
clave del almacén o el identificador exacto (`dir:<ruta>`, `lock:<nombre>`, `parked:<clave>`,
`project:.agents/skills/<nombre>` o `project:.claude/skills/<nombre>`); ante cualquier ambigüedad se niega
en lugar de eliminar varias copias por nombre.

### Salida JSON de `skills show`

`skillhub skills show <skill> --json` devuelve un objeto JSON con los valores crudos (sin escapar):

- `id`, `name`, `aliases` (opcional), `description` (opcional): identidad y metadatos de la skill.
- `skillhub_state`: estado en skillhub (`"active"`, `"disabled"`, `"disabled_by_hand"` o `"ignored"`).
- `state` (opcional) e `issues` (opcional): estado de actualización e incidencias del inventario.
- `origin`: origen (`kind`, y `lock` o `git_clone` según corresponda).
- `sightings`: lista de agentes que la ven (`harness`, `harness_name`, `kind` y `entry`).
- `locations`: ubicaciones detectadas en disco (`path`, `agent_id`, `kind`, `content_tree_sha`).
- `activity`: historial de skillhub para esta skill (`at`, `action`, `target`, `v2`, y opcionalmente `operation` y `undo`).
- `disabled_since` y `return_to` (opcionales): fecha y agentes de retorno cuando está desactivada con skillhub.

### Códigos de salida

`0` éxito y `2` error o rechazo. `check --exit-code` termina con `1` cuando hay actualizaciones
pendientes; sin esa opción, tenerlas sigue siendo un éxito. `updates apply` termina con `2` si alguna
actualización fue rechazada o falló.

### Actualizaciones en detalle

- **La confirmación vale solo para lo que se mostró.** `updates apply` muestra el `id` de cada
  candidato, la versión a la que lleva y sus etiquetas de riesgo, y pide confirmación. Si una revisión
  reemplazó un candidato mientras tanto, se rechaza y hay que revisarlo de nuevo. `--yes` aprueba los
  candidatos pendientes en el momento de ejecutarse, sin mostrártelos antes. Sin una terminal y sin
  `--yes`, `apply` se niega.
- **Una skill que editaste a mano** se revisa con dos diffs: tus cambios (de la versión registrada a la
  instalada) y los del autor (de la versión registrada a la del repositorio de origen). `updates show`
  los muestra en dos secciones y, con `--json`, en los campos `registered_to_installed` y
  `registered_to_upstream`; en la interfaz interactiva, `v` alterna entre las dos vistas del diff. Si la
  versión registrada ya no está en el repositorio de origen, se muestra solo el diff de la instalada a la
  de origen (`installed_to_upstream` en JSON) y se indica. Aplicar reemplaza tus cambios por la versión
  del autor; tus cambios se conservan en una copia de respaldo.
- **`updates apply --all`** deja fuera las skills que editaste a mano y las bloqueadas, e indica cuáles;
  las que editaste se aplican nombrándolas. Las demás se aplican una tras otra, y un fallo no detiene ni
  revierte a las otras.
- **Operación de gestión sin terminar.** Mientras exista el registro de una operación de gestión
  (`operation-v2.json`, en el directorio de estado), `check`, `updates apply`, `updates skip` y `undo`
  (también con `--dry-run`) se niegan sin escribir nada y lo explican: «hay una operación de gestión de
  skills sin terminar; ejecuta «skillhub recover»». `recover --dry-run` lo dice, y `recover` la
  resuelve: la revierte si no llegó a completarse o termina de cerrarla si ya se había completado. Si
  además quedó interrumpida una actualización, un segundo `recover` la resuelve. La revisión en segundo
  plano tampoco notifica ni guarda nada mientras tanto.

### Gestión de skills en detalle

Una skill administrada vive una sola vez en el almacén, `~/.agents/skills/<clave>`, y cada agente de
destino que no lee el almacén directamente la ve por un enlace simbólico.

- **El plan y su identificador.** Cada comando muestra primero su plan: los pasos, los enlaces que se
  quitan o se crean, los agentes que siguen viendo la skill, los enlaces en carpetas que skillhub no toca
  y que quedarán rotos, las otras copias con el mismo nombre, las actualizaciones pendientes que pasarán
  a reemplazadas, los agentes abiertos ahora y cuáles conviene recargar después. Termina con el
  identificador del plan, que cubre la acción, los destinos, la copia que se conserva, el origen y el
  estado del lock.
- **La aprobación.** Sin opciones, el comando pide confirmación en la terminal, y la confirmación vale
  solo para el plan que mostró. Sin una terminal se niega. `--dry-run` muestra el plan y no escribe nada;
  si hay una operación interrumpida, se niega igual que la ejecución real y pide `skillhub recover`.
  `--yes` exige `--plan <id>`, el identificador de un plan que viste: por ejemplo,
  `skillhub skills disable mi-skill --dry-run` y después
  `skillhub skills disable mi-skill --yes --plan <id>`. Sin `--plan`, `--yes` se niega y explica cómo
  obtenerlo.
- **Plan cambiado.** Al aprobar, skillhub vuelve a calcular el plan. Si ya no es el que aprobaste (por
  ejemplo, porque apareció un enlace nuevo o cambió el lock), se niega, muestra el identificador nuevo y
  no ejecuta nada: nunca ejecuta en silencio un plan regenerado.
- **Instalar.** Desde GitHub: `owner/repo`, `owner/repo@skill`, `owner/repo/ruta` o la URL
  `github.com/o/r/tree/ref/ruta`. También desde un sitio web que publica sus skills en
  `/.well-known/agent-skills/`, como hace `npx skills add <url>`: `https://sitio@skill`, por ejemplo
  `skillhub skills install https://uizze.sh@ui-taste` (sin `@skill`, el sitio debe publicar una sola).
  Antes del plan muestra la revisión completa de la copia preparada: archivos, etiquetas de riesgo y diff
  contra una carpeta vacía. Toda instalación lleva la etiqueta «permisos nuevos», porque la skill entera
  es nueva en esta máquina. Sin `--to`, los destinos son los agentes detectados que se proponen; es una
  propuesta que apruebas con el plan, nunca un permiso automático. El identificador de una instalación
  termina en `@<milisegundos>`: es la hora que registra su entrada del lock, y con ella `--yes --plan`
  vuelve a calcular exactamente el mismo plan. Mientras haya una operación pendiente, instalar se niega
  antes de escribir nada y pide `skillhub recover`. Los errores de GitHub (un repositorio o un ref que no
  existen, el límite de uso, gh sin sesión o sin red) se explican en español.
- **La copia preparada** para la revisión vive en `~/.agents/.skillhub/staging/` solo mientras se usa:
  si el plan no se aplica (`--dry-run`, una negativa, un plan cambiado o una confirmación que no llega),
  se borra, y también al deshacer la instalación, así que el home queda como estaba. Si skillhub se cortó
  mientras preparaba o borraba esa copia, y no hay ninguna operación pendiente, lo que quede en
  `staging/` no es de nadie: `skillhub recover` lo borra y dice que eliminó restos incompletos, y la
  siguiente instalación también lo borra. Mientras haya una operación pendiente, `staging/` no se toca.
- **Instalar desde un sitio web.** El origen se muestra como «sitio web uizze.sh (no es GitHub)».
  skillhub busca el índice donde lo busca `npx skills` 1.9.0: `/.well-known/agent-skills/index.json` y
  después `/.well-known/skills/index.json`; si la URL tiene una ruta, solo bajo esa ruta, nunca en la
  raíz del sitio. Acepta el índice antiguo (`files[]`) y el de la versión 0.2.0 (`url` y `digest`, que
  se verifica); una skill publicada como archivo comprimido no se instala. Se lee solo por `https`, con
  `curl`, sin seguir redirecciones, y solo archivos del mismo sitio y bajo la dirección indicada, con
  rutas simples (sin `..`, rutas absolutas, caracteres de control ni barras invertidas). Límites: 500
  archivos, 8 MiB por archivo y 32 MiB en total. La revisión, las etiquetas, el diff, el plan, la
  aprobación ligada al plan, el deshacer y `recover` son los de GitHub. La entrada del lock es la que
  escribe `npx skills` 1.9.0 (`sourceType: "well-known"`, `sourceBaseUrl` y `wellKnownDigest`, sin
  `ref`). Las actualizaciones de una skill de un sitio web todavía no se revisan: `skillhub check` la
  muestra como «origen web: la revisión de actualizaciones llegará después», y no cuenta como fallo.
- **Carpetas propias vacías.** Ninguna operación deja vacías `~/.agents/.skillhub/disabled` ni
  `~/.agents/.skillhub`: al activar la última skill desactivada, por ejemplo, se quitan. Nunca se quita
  una carpeta que no sea de skillhub ni una que tenga algo, y si convertiste una de esas carpetas en un
  enlace simbólico o en un archivo, se deja tal cual.
- **Recargar.** La lista «Después, recarga las skills en:» incluye todos los agentes que leen el
  directorio que cambia (OpenCode, por ejemplo, también lee el directorio de skills de Claude Code) y
  nunca menos de los que muestra el plan. `adopt-off` cuenta entre sus destinos los agentes cuyos
  enlaces quita, como Claude Code, por los enlaces de su propia carpeta `OFF/`.
- **Desactivar** quita los enlaces de la skill, guarda aparte su carpeta, en
  `~/.agents/.skillhub/disabled`, y también su entrada del lock, para que `npx skills update` no la
  reinstale. Un agente que tiene una copia propia la sigue viendo, y el plan lo avisa.
- **Activar con una vecina desactivada.** Las entradas del lock vuelven a su posición en el orden
  inverso al que salieron. Si una vecina sigue desactivada, `enable` se niega y nombra la skill que hay
  que activar primero.
- **Eliminar** deja todo en una copia de respaldo reversible con `undo`. Acepta una skill global del
  almacén (activa o desactivada), una carpeta global independiente en el directorio de skills de un
  agente configurado o desactivada a mano en `~/.agents/skills/OFF/`, o una carpeta real directa en
  `.agents/skills` o `.claude/skills` del proyecto desde el que abriste skillhub, seleccionándola por su
  identificador exacto (`dir:<ruta>`, `lock:<nombre>`, `parked:<clave>`, `project:.agents/skills/<nombre>`
  o `project:.claude/skills/<nombre>`). La confirmación muestra las rutas exactas, quita solo los enlaces
  propios seguros y la carpeta elegida, y no altera entradas del lock, homónimos globales ni copias de
  otros ámbitos. Rechaza rutas arbitrarias, raíces o copias enlazadas simbólicamente, otros proyectos
  (superiores o vecinos), copias de proveedores o perfiles, y clones de git (o carpetas con `.git`). Para
  deshacer o recuperar una operación de proyecto, debes volver a abrir skillhub en ese mismo proyecto. Si
  la eliminación se completó pero falló marcar como reemplazadas sus actualizaciones pendientes, se informa
  como un éxito con un aviso. Eliminar una skill desactivada sin carpeta (una entrada guardada aparte con
  la reparación `park_entry`) descarta solo su registro; el plan lo indica con «Sin carpeta».
- **Doctor** solo lee. `doctor fix` aplica la única reparación que admite el hallazgo; si admite varias,
  las lista y pide que elijas una con `--repair`, y si no admite ninguna, explica por qué (por ejemplo,
  las copias de gentle-ai solo se informan). `<hallazgo>` es el identificador que muestra `skillhub doctor`. Para unir copias que
  difieren (la reparación `adopt`), `--canonical <ruta>` elige la copia que se conserva.
- **Reinstalar** una entrada del lock sin carpeta: `skillhub doctor fix <hallazgo> --repair reinstall`.
  Muestra la misma revisión que `skills install` del origen que registra la entrada en GitHub y después
  un plan que quita esa entrada y la vuelve a añadir con el árbol revisado, junto con la carpeta y los
  enlaces. `--to` elige los destinos, como al instalar, y solo se admite con esta reparación; del mismo
  modo, `--canonical` solo se admite con `adopt`. Se aprueba igual que una instalación (el identificador
  termina en `@<milisegundos>`) y se deshace con `skillhub undo v2:reinstall --to <snapshot>`. Se niega
  si el lock ya no tiene la entrada, si la entrada fija un `ref` (skillhub nunca quita un `ref`) o si
  registra otro origen; una entrada que no viene de GitHub no se puede reinstalar.
- **Dotfiles enlazados.** Si un directorio de configuración, como `~/.claude`, es un enlace simbólico
  (por ejemplo, gestionado con stow), los enlaces que crean `link`, `install` y las reparaciones son
  relativos a su ruta real, como los escribe `npx skills`, y por eso resuelven.

### Buscar en skills.sh

- `skills search` y `skills popular` no instalan nada. Para instalar un resultado usa
  `skillhub skills install <origen>`, con la revisión, el plan y el deshacer de siempre:
  `owner/repo@skill` para GitHub, o `https://sitio@skill` para un resultado de un sitio web que publica
  `/.well-known/agent-skills/` (por ejemplo `https://uizze.sh@ui-taste`), que se marca como «sitio web
  (no es GitHub)». Un resultado que la instalación no admite se marca como no instalable.
- «Populares» no es el ranking oficial de skills.sh, que exige una credencial: se arma con búsquedas por
  tema (react, testing, python, docs, design, git, typescript, security, data y agent), ordenadas por
  instalaciones.
- `--limit` va de 1 a 100; por defecto, 20 en `search` y 30 en `popular`.
- Los resultados se guardan una hora en la caché; `--refresh` vuelve a consultar. Si skills.sh no
  responde y hay resultados guardados, se usan con un aviso.
- Requiere `curl`. `SKILLS_API_URL` cambia la dirección del catálogo (solo `https`).

### La interfaz interactiva

- **Cuatro pestañas:** 1 Skills, 2 Actualizar, 3 Instalar y 4 Salud; `tab` las recorre. Entre
  paréntesis, cuántas skills hay, cuántas actualizaciones están pendientes y cuántos problemas se pueden
  reparar. `h` abre el Historial y `c` los Ajustes desde cualquier pestaña; `esc` vuelve. `r` revisa si
  hay actualizaciones y `q` sale.
- **Teclas:** todas son letras minúsculas. Si Bloq Mayús está activado, la mayúscula hace lo mismo, pero
  nunca se muestra; lo que escribes en un filtro o en un campo conserva sus mayúsculas. Ninguna letra
  tiene dos significados en la misma pestaña. Las listas se mueven con las flechas, PgUp, PgDn, `home` y
  `end`. La línea de abajo muestra siempre las teclas de la pestaña y de la fila actual; `?` abre la
  ayuda de la pestaña.
- **Skills:** arriba, un resumen («17 skills · 12 activas · 5 desactivadas · 2 con actualización · 3 con
  problemas», más «· 2 ignoradas» cuando ignoras alguna). A la izquierda, la lista con un ícono por fila
  (`●` activa, `↑` con una actualización, `○` desactivada, `⚠` con problemas, `-` ignorada); a la
  derecha, el panel de la skill: primero su situación y el paso recomendado, después su descripción, los
  agentes que la ven de verdad, su origen y las acciones que admite. El panel solo ofrece acciones que la
  app puede planificar para esa fila; las skills gestionadas por un proveedor o plugin (como un plugin de
  Claude Code o `gentle-ai`) indican quién las gestiona y a qué superficie o documentación acudir, sin
  ofrecer acciones que skillhub no pueda verificar. `/` busca,
  `f` filtra por estado (todas, activas, desactivadas, con
  problemas, con actualización, ignoradas) y `g` muestra solo lo que ve un agente («Qué ve Claude
  Code»). Por debajo de 100 columnas, el panel va debajo de la lista.
- **Teclas de Skills:** `d` desactivar, `a` activar, `e` eliminar (disponible para skills del almacén,
  desactivadas, en `OFF/`, carpetas globales independientes de un agente que no sean clones de git,
  carpetas reales directas en `.agents/skills` o `.claude/skills` del proyecto actual y filas del filtro
  «ignoradas», manteniendo la lista `ignore` en `settings.toml`), `l` elegir en qué agentes está
  (casillas: espacio marca, `enter` confirma; cada enlace que cambia es un plan con su propia
  confirmación), `o` desactivarla de verdad (una skill apagada a mano en `OFF/` que algunos agentes
  siguen viendo: adopta todas las de `OFF/` en un solo plan y las nombra), `p` arreglar su enlace roto
  con la reparación de Salud, `x` ignorar (la oculta de skillhub; tus agentes la siguen usando). Con el
  filtro «ignoradas», `x` deja de ignorar la skill elegida y vuelve a aparecer en Skills. Ignorar o dejar
  de ignorar solo cambia `settings.toml`; nunca toca los archivos de la skill.
- **Confirmaciones:** empiezan con una pregunta («¿Desactivar agent-browser?») y unas pocas líneas en
  palabras simples: qué va a pasar, en qué agentes, qué no se pierde, cómo se deshace y qué hay que
  recargar. Debajo, `[s] Sí  [n] No  [v] Ver detalles`: `v` muestra el detalle técnico (rutas, pasos,
  lock e identificador del plan), el mismo que imprime la línea de comandos. Cada acción se aplica solo
  cuando confirmas con `s` (o `enter`) y aprueba exactamente ese plan; si el plan cambió mientras lo
  mirabas, se muestra el nuevo y vuelve a preguntar. Mientras una operación corre, ninguna otra tecla de
  escritura actúa.
- **Deshacer:** después de aplicar algo, la línea de estado dice «Listo: … [z] deshacer»; `z` abre su
  deshacer, con resumen y confirmación. En el Historial, cada línea se lee en palabras simples
  («Desactivaste agent-browser — hace 2 horas — se puede deshacer [u]») y `u` deshace esa operación.
- **Actualizar:** cada actualización lleva un semáforo de riesgo con su consecuencia: `● bajo` (cambia
  solo texto, es decir, las instrucciones que lee el agente), `▲ medio` (scripts, binarios o una edición
  tuya) y `◆ revisar` (permisos de ejecución o texto sospechoso). `enter` muestra los cambios, `a` aplica
  esta, `o` omite esta versión y `t` aplica todas, una por una y sin las modificadas por ti ni las
  bloqueadas.
- **Instalar:** cuatro pasos, y dice en cuál estás: (1) de dónde la traigo, de GitHub (`owner/repo…`) o
  de un sitio web que publica skills (`https://sitio[/ruta][@skill]`); (2) revisa lo que trae, con el
  semáforo, los archivos y el diff; (3) elige los agentes con casillas; (4) confirma.
- **Salud:** los hallazgos se agrupan por skill; los que no tienen una skill asociada aparecen aparte.
  Cada hallazgo explica el problema y, cuando corresponde, propone una reparación. Las copias de
  gentle-ai se resumen en una sola línea, porque skillhub no las toca. `enter` muestra cómo reparar el
  hallazgo elegido. Ante dos skills con el mismo nombre, `m` compara sus rutas, los agentes que las ven
  y las diferencias entre archivos, sin modificar nada. El mismo atajo aparece en la ficha de la skill
  cuando hay otra con el mismo nombre; `esc` vuelve a la vista anterior.
- **Ajustes:** cada ajuste lleva una línea que dice qué hace. En «Ignoradas», `enter` abre una casilla
  por skill ignorada, todas marcadas: desmarca las que quieras volver a ver y confirma con `enter`. Un
  nombre de la lista que ya no corresponde a ninguna skill también aparece, para poder quitarlo.
- Los colores se adaptan al fondo claro u oscuro y desaparecen con `NO_COLOR`; los íconos dicen lo mismo
  sin ellos.

### Ajustes

Están en `~/.config/skillhub/settings.toml` (o bajo `XDG_CONFIG_HOME`). Se cambian con
`skillhub settings set <key> <value>` o desde los Ajustes de la interfaz (`c`):

| Clave | Por defecto | Qué hace |
|---|---|---|
| `check_interval` | `1h` | Cada cuánto busca actualizaciones la revisión en segundo plano; el mínimo es `15m` |
| `notifications` | `true` | `false` silencia todas las notificaciones |
| `ignore` | vacía | Nombres o `id` que el inventario deja fuera, separados por comas en `settings set` |

### Revisión en segundo plano

La revisión en segundo plano busca actualizaciones con la app cerrada, las prepara y te avisa con una
notificación de escritorio cuando aparece una actualización nueva, cuando una revisión empieza a fallar
o cuando hay una operación interrumpida. Una lista de pendientes sin cambios no se vuelve a anunciar.
Nunca aplica nada.

**Activarla es una acción tuya**: ningún otro comando ni la instalación la activan. Cuando quieras,
ejecuta:

```bash
skillhub background enable
```

Esto escribe `skillhub-check.service` y `skillhub-check.timer` en tu directorio de usuario de systemd
(`~/.config/systemd/user`), apuntando a la ruta del binario que lo ejecutó, y activa el temporizador.
El temporizador se dispara poco después de iniciar sesión y cada 15 minutos, y ejecuta
`skillhub check --if-due --notify`, que revisa solo cuando ya pasó `check_interval` desde la última
revisión exitosa. Cambiar el intervalo nunca reescribe las unidades. `skillhub background status`
muestra su estado y `skillhub background disable` la desactiva y borra las unidades. Si mueves el
binario, vuelve a ejecutar `enable`.

### El estado de skillhub y los binarios anteriores

skillhub guarda su estado en `~/.local/state/skillhub/state.json` (o bajo `XDG_STATE_HOME`). La
versión 2 de ese archivo añade las skills administradas, las desactivadas y los destinos preferidos.

- **Migración.** skillhub lee un `state.json` de la versión 1 tal como está. La primera vez que lo
  escribe, deja antes una copia idéntica en `state.v1.json`, en el mismo directorio, y después escribe la
  versión 2. Los candidatos, las omisiones, los resultados de cada revisión, las marcas de notificación y
  el historial (`journal.jsonl`) se conservan. Si `state.v1.json` ya existe, nunca se sobrescribe.
- **Migración en espera.** Si el `state.json` de la versión 1 registra una operación interrumpida, la
  migración espera: skillhub sigue escribiéndolo en la versión 1 hasta que `skillhub recover` resuelve
  esa operación, de modo que un binario anterior también puede recuperarla.
- **Binarios anteriores.** Un binario de la versión 1 se niega a leer un `state.json` de la versión 2 y
  termina con error antes de escribir nada, así que no puede estropearlo. Si la revisión en segundo
  plano quedó apuntando a un binario anterior, sus revisiones fallarán sin tocar nada: instala el nuevo
  en la misma ruta o vuelve a ejecutar `skillhub background enable`. Para volver a la versión 1, restaura
  `state.v1.json` como `state.json`; lo que se hizo después con la versión 2 no está en esa copia.

## Ejecutar las pruebas

```bash
go test ./...
go vet ./...
```

Las pruebas son herméticas: cada paquete apunta `HOME` y las variables `XDG_*` a un directorio
temporal, rechaza cualquier ruta dentro de tu home real y sustituye `gh`, `git`, `npx` y otras
herramientas por versiones que fallan, de modo que ninguna prueba toca tus skills ni la red. Las pruebas
contra herramientas reales son opcionales y van tras una etiqueta de compilación: `-tags network` para
`gh` y `curl`, y `-tags realgit` para `git`. La matriz de aceptación de la versión 2 corre siempre su
camino rápido (unos segundos); con `go test -tags acceptance ./internal/entrypoints/cli/` además provoca
una caída y un fallo en cada escritura de cada paso, seguidos de dos `recover` (varios minutos).
