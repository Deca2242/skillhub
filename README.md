# skillhub

skillhub es una app de terminal, con una interfaz de terminal (TUI) y una línea de comandos (CLI), que
administra las skills de agentes que instalaste por tu cuenta, con `npx skills` o clonándolas desde
GitHub. Reúne todas en un solo inventario, detecta cuándo cambian en upstream y aplica una actualización
solo cuando tú la apruebas.

Las versiones 1 (ver y actualizar) y 2 (gestionar las skills de todos los agentes desde un solo lugar)
están terminadas. Nada cambia en tus carpetas de skills sin tu aprobación: cada escritura muestra antes su
plan y se puede deshacer.

## Instalar

Requiere Go 1.27, y `gh` con una sesión iniciada (`gh auth login`): skillhub consulta GitHub solo a
través de `gh`.

```bash
go build -o bin/skillhub ./cmd/skillhub
install -m 0755 bin/skillhub ~/.local/bin/skillhub
```

## Primeros pasos, sin cambiar nada

Estos tres recorridos solo leen: no modifican ninguna skill.

1. **Ver qué skills tienes.** Ejecuta `skillhub`: la pestaña Skills las lista todas, con su estado y los
   agentes que las ven. Desde la CLI, `skillhub scan` da la misma lista (`--json` para scripts).
2. **Filtrar por agente.** En la pestaña Skills, `g` elige un agente y deja solo las skills que ese agente
   ve; `f` filtra por estado.
3. **Entender un problema.** La pestaña Salud explica cada hallazgo (enlaces rotos, duplicados, copias que
   difieren, skills que un agente sigue viendo aunque estén desactivadas) y qué reparación propone. Nada se
   repara sin tu confirmación. Desde la CLI: `skillhub doctor`.

## Usar

Sin argumentos, en una terminal, `skillhub` abre la interfaz de terminal. Cada comando tiene su ayuda
con `--help`.

```text
skillhub scan [--json]                          lista las skills instaladas
skillhub check [--if-due] [--notify] [--exit-code] [--json]
                                                  revisa si hay actualizaciones; nunca aplica nada
skillhub updates list [--json]                  actualizaciones pendientes y bloqueadas
skillhub updates show <skill> [--json]          etiquetas, archivos y diff de una actualización
skillhub updates apply <skill>... [--yes] [--dry-run]
skillhub updates apply --all [--yes] [--dry-run]
skillhub updates skip <skill> [--dry-run]       omite esa versión hasta que el autor publique otra
skillhub history [--json]                       lo que se aplicó, deshizo, omitió o recuperó
skillhub undo <skill> [--to <snapshot>] [--dry-run]
skillhub recover [--dry-run]                    restaura la versión anterior tras una interrupción
skillhub settings show | set <key> <value> [--dry-run]
skillhub background status | enable | disable [--dry-run]
```

- **`<skill>`** es el nombre de la skill o su `id` (`lock:<nombre>`, `git:<ruta>`, `dir:<ruta>`). Si un
  nombre corresponde a más de una skill, el comando se niega y lista los `id`: vuelve a ejecutarlo con
  el `id` que quieras.
- **Nada se aplica sin tu aprobación.** `updates apply` muestra el `id` de cada candidato, la versión a
  la que lleva y sus etiquetas de riesgo, y pide confirmación. La confirmación vale solo para lo que
  mostró: si una revisión reemplazó un candidato mientras tanto, se rechaza y hay que revisarlo de
  nuevo. `--yes` aprueba los candidatos pendientes en el momento de ejecutarse, sin mostrártelos antes.
  Sin una terminal y sin `--yes`, `apply` se niega.
- **Una skill que editaste a mano** se revisa con dos diffs: tus cambios (de la versión registrada a
  la instalada) y los del autor (de la versión registrada a upstream). `updates show` los muestra en dos
  secciones y, con `--json`, en los campos `registered_to_installed` y `registered_to_upstream`; en la
  interfaz de terminal, `v` alterna entre las dos vistas del diff. Si la versión registrada ya no está en
  upstream, se muestra solo el diff de la instalada a upstream (`installed_to_upstream` en JSON) y se
  indica. Aplicar reemplaza tus cambios por la versión del autor; se conservan en un snapshot.
- **`updates apply --all`** deja fuera las skills que editaste a mano y las bloqueadas, e indica cuáles.
  Las modificadas a mano se aplican de una en una, nombrándolas.
- **Todo comando que cambia algo** admite `--dry-run`, que muestra el plan sin cambiar nada.
- **Toda aplicación se puede deshacer** con `undo`. Si la carpeta cambió después de aplicar, `undo`
  guarda antes una copia de su estado actual y pide una confirmación aparte en la terminal.
- **Operación interrumpida.** Si skillhub se cortó a mitad de una aplicación o de un deshacer, todo
  comando lo avisa, y `updates apply`, `updates skip` y `undo` se niegan hasta que ejecutes
  `skillhub recover`. Nada lo repara solo.
- **Operación de gestión de skills sin terminar.** Mientras exista el registro de una operación de la
  versión 2 (`operation-v2.json`, en el directorio de estado), `check`, `updates apply`, `updates skip`
  y `undo` (también con `--dry-run`) se niegan sin escribir nada y lo explican: «hay una operación de
  gestión de skills sin terminar; ejecuta «skillhub recover»». `recover --dry-run` lo dice, y `recover`
  la resuelve: la revierte si no llegó a completarse o termina de cerrarla si ya se había completado. Si
  además quedó interrumpida una actualización, un segundo `recover` la resuelve. La revisión en segundo
  plano tampoco notifica ni guarda nada mientras tanto.

**Códigos de salida:** `0` éxito, `2` error. `check --exit-code` termina con `1` cuando hay
actualizaciones pendientes; sin esa opción, tenerlas sigue siendo un éxito. `updates apply` termina con
`2` si alguna actualización fue rechazada o falló.

### No ejecutes `npx skills` mientras skillhub aplica o deshace

`npx skills add`, `update` y `remove` reescriben el archivo de lock de skills sin ninguna protección, y
skillhub no puede impedirlo. No los ejecutes mientras `skillhub updates apply`, `undo` o `recover`
están en marcha. skillhub vuelve a leer el lock antes de escribir y revierte si cambió, pero si
`npx skills` escribe después a partir de una lectura anterior, la skill aparecerá como modificada
localmente.

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

## Gestionar skills (versión 2)

skillhub también instala, elimina, activa, desactiva y enlaza skills, y repara enlaces rotos y copias
duplicadas. Una skill administrada vive una sola vez en el almacén, `~/.agents/skills/<clave>`, y está
enlazada en cada harness de destino que no lee el almacén directamente.

```text
skillhub skills install <origen> [--to <harness>...]   instala desde GitHub o un sitio web tras revisarla
skillhub skills remove <skill>                         elimina una skill, activa o desactivada
skillhub skills disable <skill>                        la desactiva en todos sus destinos gestionados
skillhub skills enable <skill>                         la activa con los enlaces que tenía
skillhub skills unlink <skill> --from <harness>        quita su enlace en un harness
skillhub skills link <skill> --to <harness>            la enlaza en un harness
skillhub skills adopt-off                              adopta como desactivadas las skills de OFF/
skillhub doctor                                        lista los hallazgos; solo lee
skillhub doctor fix <hallazgo> [--repair <tipo>] [--canonical <ruta>] [--to <harness>]
skillhub undo v2:<acción> [--to <snapshot>]            deshace una operación de gestión
skillhub recover                                       resuelve una operación interrumpida
```

Todos los comandos que escriben admiten `--dry-run`, `--yes` y `--plan <id>`.

- **El plan y su identificador.** Cada comando muestra primero su plan: los pasos, los enlaces que se
  quitan o se crean, los harness que siguen viendo la skill, los enlaces de zonas que skillhub no toca y
  que quedarán rotos, las otras copias con el mismo nombre, las actualizaciones pendientes que pasarán a
  reemplazadas, los agentes abiertos ahora y cuáles conviene recargar después. Termina con el
  identificador del plan, que cubre la acción, los destinos, la canónica, el origen y el estado del lock.
- **La aprobación.** Sin opciones, el comando pide confirmación en la terminal, y la confirmación vale
  solo para el plan que mostró. Sin una terminal se niega. `--dry-run` muestra el plan y no escribe nada. Si hay una operación interrumpida, `--dry-run` se niega
  igual que la ejecución real y pide `skillhub recover`.
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
  Antes del plan muestra la revisión completa de la copia preparada:
  archivos, etiquetas de riesgo y diff contra una carpeta vacía. Toda instalación lleva la etiqueta
  «permisos nuevos», porque la skill entera es nueva en esta máquina. Sin `--to`, los destinos son los
  harness detectados que se proponen; es una propuesta que apruebas con el plan, nunca un permiso
  automático. El identificador de una instalación termina en `@<milisegundos>`: es la hora que registra
  su entrada del lock, y con ella `--yes --plan` vuelve a calcular exactamente el mismo plan. La copia
  preparada para la revisión vive en `~/.agents/.skillhub/staging/` solo mientras se usa: si el plan no
  se aplica (`--dry-run`, una negativa, un plan cambiado o una confirmación que no llega), se borra, y
  también al deshacer la instalación, así que el HOME queda como estaba. Si skillhub se cortó mientras
  preparaba o borraba esa copia, y no hay ninguna operación pendiente, lo que quede en `staging/` no es
  de nadie: `skillhub recover` lo borra y dice que eliminó restos incompletos, y la siguiente instalación
  también lo borra. Mientras haya una operación pendiente, `staging/` no se toca. Los errores de GitHub (un
  repositorio o un ref que no existen, el límite de uso, gh sin sesión o sin red) se explican en español.
- **Instalar desde un sitio web.** El origen se muestra como «sitio web uizze.sh (no es GitHub)». skillhub
  busca el índice donde lo busca `npx skills` 1.9.0: `/.well-known/agent-skills/index.json` y después
  `/.well-known/skills/index.json`; si la URL tiene una ruta, solo bajo esa ruta, nunca en la raíz del
  sitio. Acepta el índice antiguo (`files[]`) y el de la versión 0.2.0 (`url` y `digest`, que se
  verifica); una skill publicada como archivo comprimido no se instala. Se lee solo por `https`, con
  `curl`, sin seguir redirecciones, y solo archivos del mismo sitio y bajo la dirección indicada, con
  rutas simples (sin `..`, rutas absolutas, caracteres de control ni barras invertidas). Límites: 500
  archivos, 8 MiB por archivo y 32 MiB en total. La revisión, las etiquetas, el diff, el plan, la
  aprobación ligada al plan, el deshacer y `recover` son los de GitHub. La entrada del lock es la que
  escribe `npx skills` 1.9.0 (`sourceType: "well-known"`, `sourceBaseUrl` y `wellKnownDigest`, sin
  `ref`). Las actualizaciones de una skill de un sitio web todavía no se revisan: `skillhub check` la
  muestra como «origen web: la revisión de actualizaciones llegará después», y no cuenta como fallo.
- **Niveles propios vacíos.** Ninguna operación deja vacíos `~/.agents/.skillhub/disabled` ni
  `~/.agents/.skillhub`: al activar la última skill desactivada, por ejemplo, se quitan. Nunca se quita
  un directorio que no sea de skillhub ni uno que tenga algo, y si convertiste uno de esos niveles en un
  enlace simbólico o en un archivo, se deja tal cual. Mientras haya una operación pendiente, instalar se
  niega antes de escribir nada y pide `skillhub recover`.
- **Recargar.** La lista «Después, recarga las skills en:» incluye todos los harness que leen el
  directorio que cambia (OpenCode también lee `~/.claude/skills`) y nunca menos de los que muestra el
  plan. `adopt-off` cuenta entre sus destinos los harness cuyos enlaces quita, como Claude Code por
  `~/.claude/skills/OFF/`.
- **Activar con una vecina desactivada.** Las entradas del lock vuelven a su posición en el orden inverso
  al que salieron. Si una vecina sigue desactivada, `enable` se niega y nombra la skill que hay que
  activar primero.
- **Deshacer.** Al terminar, cada operación dice cómo deshacerla, por ejemplo
  `skillhub undo v2:disable --to <snapshot>`; `skillhub history` lista sus líneas. Deshacer muestra su
  plan y pide la misma aprobación. Se niega si algo cambió después de la operación.
- **Eliminar** deja todo en un snapshot. Si la eliminación se completó pero falló marcar como
  reemplazadas sus actualizaciones pendientes, se informa como un éxito con un aviso.
- **Doctor** solo lee. `doctor fix` aplica la única reparación que admite el hallazgo, o pide `--repair`
  si admite varias; si no admite ninguna, explica por qué (por ejemplo, las copias de gentle-ai solo se
  informan).
- **Reinstalar** una entrada del lock sin carpeta: `skillhub doctor fix <hallazgo> --repair reinstall`.
  Muestra la misma revisión que `skills install` del origen que registra la entrada en GitHub y después
  un plan que quita esa entrada y la vuelve a añadir con el árbol revisado, junto con la carpeta y los
  enlaces. `--to` elige los destinos, como al instalar, y solo se admite con esta reparación; del mismo modo,
  `--canonical` solo se admite con `adopt`. Se aprueba
  igual que una instalación (el identificador termina en `@<milisegundos>`) y se deshace con
  `skillhub undo v2:reinstall --to <snapshot>`. Se niega si el lock ya no tiene la entrada, si la entrada
  fija un `ref` (skillhub nunca quita un `ref`) o si registra otro origen; una entrada que no viene de
  GitHub no se puede reinstalar.
- **Dotfiles enlazados.** Si un directorio de configuración, como `~/.claude`, es un enlace simbólico
  (por ejemplo, gestionado con stow), los enlaces que crean `link`, `install` y las reparaciones son
  relativos a su ruta real, como los escribe `npx skills`, y por eso resuelven.
- **Eliminar una skill desactivada sin carpeta** (una entrada aparcada con `park_entry`) descarta solo su
  registro; el plan lo indica con «Sin carpeta».
- **Códigos de salida:** los mismos de siempre, `0` éxito y `2` error o rechazo.

### Buscar en skills.sh

```
skillhub skills search <consulta> [--limit n] [--json] [--refresh]   busca en skills.sh
skillhub skills popular [--limit n] [--json] [--refresh]             las más instaladas, por temas
```

- Los dos solo leen. Para instalar un resultado usa `skillhub skills install <origen>`, con la revisión,
  el plan y el deshacer de siempre: `owner/repo@skill` para GitHub, o `https://sitio@skill` para un
  resultado de un sitio web que publica `/.well-known/agent-skills/` (por ejemplo
  `https://uizze.sh@ui-taste`), que se marca como «sitio web (no es GitHub)». Un resultado que la
  instalación no admite se marca como no instalable.
- «Populares» no es el ranking oficial de skills.sh, que exige una cuenta: se arma con búsquedas por tema
  ordenadas por instalaciones.
- Los resultados se guardan una hora en la caché; `--refresh` vuelve a consultar. Si skills.sh no
  responde y hay resultados guardados, se usan con un aviso.
- Requiere `curl`. `SKILLS_API_URL` cambia la dirección del catálogo (solo `https`).

### La TUI

- **Cuatro pestañas:** 1 Skills, 2 Actualizar, 3 Instalar y 4 Salud; `tab` las recorre. Los números
  entre paréntesis cuentan lo que necesita atención (actualizaciones pendientes, problemas que se pueden
  reparar). h abre el Historial y c los Ajustes desde cualquier pestaña; esc vuelve.
- **Teclas:** todas son letras minúsculas. Si Bloq Mayús está activado, la mayúscula hace lo mismo, pero
  nunca se muestra; lo que escribes en un filtro o en un campo conserva sus mayúsculas. Ninguna letra
  tiene dos significados en la misma pestaña. Las listas se mueven con las flechas, PgUp, PgDn, `home` y
  `end`. La línea de abajo muestra siempre las teclas de la pestaña y de la fila actual; ? abre la ayuda
  de la pestaña.
- **Skills:** arriba, un resumen («17 skills · 12 activas · 5 desactivadas · 2 con actualización · 3 con
  problemas», más «· 2 ignoradas» cuando ignoras alguna). A la izquierda, la lista con un ícono por fila (`●` activa, `↑` con una actualización,
  `○` desactivada, `⚠` con problemas, `-` ignorada); a la derecha, el panel de la skill: primero su situación y el
  paso recomendado, después su descripción, los agentes que la ven de verdad, su origen y las acciones
  que admite. El panel solo ofrece acciones que la app puede planificar para esa fila. / busca, f filtra
  por estado (todas, activas, desactivadas, con problemas, con actualización, ignoradas) y g muestra solo lo que ve
  un agente («Qué ve Claude Code»).
- **Teclas de Skills:** d desactivar, a activar, e eliminar, l elegir en qué agentes está (casillas:
  espacio marca, enter confirma; cada enlace que cambia es un plan con su propia confirmación), o
  desactivarla de verdad (una skill apagada a mano en `OFF/` que algunos agentes siguen viendo: adopta
  todas las de `OFF/` en un solo plan y las nombra), p arreglar su enlace roto con la reparación de
  Salud, x ignorar (la oculta de skillhub; tus agentes la siguen usando). Con el filtro «ignoradas», x
  deja de ignorar la skill elegida y vuelve a aparecer en Skills. Ignorar o dejar de ignorar solo cambia
  `settings.toml`; nunca toca los archivos de la skill.
- **Confirmaciones:** empiezan con una pregunta («¿Desactivar agent-browser?») y unas pocas líneas en
  palabras simples: qué va a pasar, en qué agentes, qué no se pierde, cómo se deshace y qué hay que
  recargar. Debajo, `[s] Sí  [n] No  [v] Ver detalles`: v muestra el detalle técnico (rutas, pasos, lock
  e identificador del plan), el mismo que imprime la CLI. Cada acción se aplica solo con s y aprueba
  exactamente ese plan; si el plan cambió mientras lo mirabas, se muestra el nuevo y vuelve a preguntar.
  Mientras una operación corre, ninguna otra tecla de escritura actúa.
- **Deshacer:** después de aplicar algo, la línea de estado dice «Listo: … [z] deshacer»; z abre su
  deshacer, con resumen y confirmación. En el Historial, cada línea se lee en palabras simples
  («Desactivaste agent-browser — hace 2 horas — se puede deshacer [u]») y u deshace esa operación.
- **Actualizar:** cada actualización lleva un semáforo de riesgo con su consecuencia: `● bajo` (cambia
  solo texto, es decir, las instrucciones que lee el agente), `▲ medio` (scripts, binarios o una edición
  tuya) y `◆ revisar` (permisos de ejecución o texto sospechoso). enter muestra los cambios, a aplica
  esta, o omite esta versión y t aplica todas, de a una y sin las modificadas por ti ni las bloqueadas.
- **Instalar:** cuatro pasos, y dice en cuál estás: (1) de dónde la traigo, de GitHub (`owner/repo…`) o
  de un sitio web que publica skills (`https://sitio[/ruta][@skill]`); (2) revisa lo que trae, con el
  semáforo, los archivos y el diff; (3) elige los agentes con casillas; (4) confirma.
- **Salud:** los problemas agrupados por tipo, con un conteo y una línea que explica el problema y el
  arreglo sugerido. Las copias de gentle-ai se resumen en una sola línea, porque skillhub no las toca.
  enter muestra cómo reparar el hallazgo elegido.
- **Ajustes:** cada ajuste lleva una línea que dice qué hace. En «Ignoradas», enter abre una casilla por
  skill ignorada, todas marcadas: desmarca las que quieras volver a ver y confirma con enter. Un nombre
  de la lista que ya no corresponde a ninguna skill también aparece, para poder quitarlo.
- **Operación interrumpida:** al abrirse, si hay una, la TUI lo dice y no muestra nada más hasta
  resolverla: r o enter ejecuta el mismo `recover` que la CLI, y q sale. Nunca se resuelve sola.
- Los colores se adaptan al fondo claro u oscuro y desaparecen con `NO_COLOR`; los íconos dicen lo mismo
  sin ellos. Por debajo de 100 columnas, el panel va debajo de la lista.

## Ajustes

`~/.config/skillhub/settings.toml` (o bajo `XDG_CONFIG_HOME`), que puedes cambiar con
`skillhub settings set`:

| Clave | Por defecto | Qué hace |
|---|---|---|
| `check_interval` | `1h` | Cada cuánto revisa la revisión en segundo plano; el mínimo es `15m` |
| `notifications` | `true` | `false` silencia todas las notificaciones |
| `ignore` | vacía | Nombres o `id` que el inventario deja fuera, separados por comas en `settings set` |

### Directorios de agentes: `agents.local.toml`

skillhub trae una tabla de agentes (harness) con el directorio global de skills de cada uno. Si en tu
máquina un agente guarda sus skills en otro lugar, corrígelo en
`~/.config/skillhub/agents.local.toml`. Cada tabla `[[agent]]` lleva `id` y `global_dir`, y
opcionalmente `reads_universal`; un `id` que ya está en la tabla corrige ese agente y uno nuevo lo
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

El ejemplo completo y comentado está en `internal/data/agents.local.example.toml`.

## Revisión en segundo plano

La revisión en segundo plano busca actualizaciones con la app cerrada, las prepara y te avisa con una
notificación de escritorio cuando aparece una actualización nueva, cuando una revisión empieza a fallar o
cuando hay una operación interrumpida. Una lista de pendientes sin cambios no se vuelve a anunciar.
Nunca aplica nada.

**Activarla es una acción tuya**: ningún otro comando ni la instalación la activan. Cuando quieras,
ejecuta:

```bash
skillhub background enable
```

Esto escribe `skillhub-check.service` y `skillhub-check.timer` en tu directorio de usuario de systemd
(`~/.config/systemd/user`), apuntando a la ruta del binario que lo ejecutó, y activa el timer. El timer
se dispara poco después de iniciar sesión y cada 15 minutos, y ejecuta
`skillhub check --if-due --notify`, que revisa solo cuando ya pasó `check_interval` desde la última
revisión exitosa. Cambiar el intervalo nunca reescribe las unidades. `skillhub background status`
muestra su estado y `skillhub background disable` la desactiva y borra las unidades. Si mueves el
binario, vuelve a ejecutar `enable`.

## Ejecutar las pruebas

```bash
go test ./...
go vet ./...
```

Las pruebas son herméticas: cada paquete apunta `HOME` y las variables `XDG_*` a un directorio
temporal, rechaza cualquier ruta dentro de tu home real y sustituye `gh`, `git`, `npx` y otras
herramientas por versiones que fallan, de modo que ninguna prueba toca tus skills ni la red. Las pruebas
contra herramientas reales son opcionales y van tras una etiqueta de compilación: `-tags network` para
`gh` y `-tags realgit` para `git`. La matriz de aceptación de la versión 2 corre siempre su camino rápido
(unos segundos); con `go test -tags acceptance ./internal/entrypoints/cli/` además provoca una caída y un
fallo en cada escritura de cada paso, seguidos de dos `recover` (varios minutos).
