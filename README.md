# manducar-impresion

El agente de impresión de [Manducar](https://manduc.ar). Es un programita
chico que corre en una computadora del local e imprime comandas y tickets
directo en una o varias impresoras, aunque el navegador esté cerrado. Sirve
para locales con más de una impresora (cocina, barra, caja) o más de una
computadora que las maneja.

Lo de siempre —el navegador imprime la pantalla que tenés abierta— sigue
andando igual. El agente es una opción del local, no un reemplazo.

La guía completa, con capturas del panel, está en el tutorial del local:
**Configuraciones → Impresión** dentro de Manducar.

## Ponerlo a andar

Tres pasos, sin escribir un solo comando. No hace falta ser administrador ni
usar `sudo`: el agente guarda todo en la carpeta de tu usuario y arranca solo
cada vez que entrás a la computadora.

1. **Descargar** el archivo de tu sistema. Los links los muestra el panel en
   **Configuraciones → Impresión → Vincular una computadora**, al lado del
   código. También están en la
   [última versión publicada](https://github.com/eesnaola/manducar-impresion/releases/latest):
   `manducar-impresion-windows-amd64.exe`, `manducar-impresion-mac.zip` (en
   Mac bajá el zip: al descomprimirlo con doble clic queda «Manducar
   Impresión», una app lista para abrir) y `manducar-impresion-linux-amd64`.
2. **Abrirlo.** Si lo abrís con doble clic, te pregunta el código en un cuadro
   de diálogo del sistema —sin ventana negra en Windows ni Terminal en Mac—; en
   una terminal, en la terminal. La primera vez el sistema desconfía —el
   programa todavía no está firmado—: ver
   [los avisos de la primera vez](#los-avisos-de-la-primera-vez).
3. **Pegar el código** que muestra el panel.

Con eso queda vinculado, instalado y arrancando solo cada vez que entrás a esa
computadora. Abrirlo de nuevo más adelante muestra a qué local está vinculado
y ofrece reinstalarlo, volver a vincularlo o ver el estado.

### Si preferís la terminal

Los mismos dos pasos, a mano, parado en la carpeta donde lo bajaste
(PowerShell en Windows, Terminal en Mac o Linux). En Linux, antes:
`chmod +x manducar-impresion`. En Mac el ejecutable está adentro de la app:
`"Manducar Impresión.app/Contents/MacOS/manducar-impresion"`.

```
.\manducar-impresion.exe vincular CODIGO   # Windows
.\manducar-impresion.exe instalar

./manducar-impresion vincular CODIGO       # Mac y Linux
./manducar-impresion instalar
```

`CODIGO` es el de ocho dígitos que muestra el panel (vence a los 10 minutos).
`manducar-impresion asistente` es el asistente del doble clic, y
`manducar-impresion estado` dice a qué local quedó vinculada la computadora y
dónde están la configuración y el log.

**En pruebas o desarrollo**, `--servidor URL` vincula contra otro servidor que
no sea `manduc.ar` —por ejemplo un local levantado a mano—, tanto en
`vincular` como en `manducar-impresion asistente --servidor URL`; para el
asistente sirve también la variable de entorno `MANDUCAR_IMPRESION_SERVIDOR`.

### Dónde queda

`instalar` —y el asistente— **copian el programa a un lugar fijo** y lo dejan
arrancando solo, así que después podés borrar el archivo que bajaste. Te dicen
adónde fue:

| | programa | configuración y log |
|---|---|---|
| Windows | `%LocalAppData%\Manducar\` | `%AppData%\Manducar\` |
| Mac | `~/Library/Application Support/Manducar/` | la misma |
| Linux | `~/.local/lib/manducar/` | `~/.config/manducar/` |

Cómo arranca solo, según el sistema:

- **Mac**: un LaunchAgent en `~/Library/LaunchAgents`. Arranca cuando entrás a
  la computadora y, si se cae, launchd lo levanta.
- **Linux**: una unidad de systemd de usuario en `~/.config/systemd/user`.
  Corre mientras tu usuario tenga sesión iniciada; para que siga con la
  sesión cerrada: `loginctl enable-linger $USER`.
- **Windows**: la clave `Run` de tu usuario, apuntando a un `.vbs` que lo
  arranca **sin ventana**. Windows no tiene servicios de usuario: sin
  administrador, esto es lo que hay. La contra es que acá **nadie lo levanta
  si se cae** —en Mac y Linux sí—; en la práctica el agente no se cierra solo:
  si el servidor deja de reconocerlo se queda esperando, y los errores los
  reintenta.

Si todavía no vinculaste la computadora, `instalar` se niega y te lo dice. Y
si el agente ya estaba instalado, tampoco lo pisa: primero corré
`manducar-impresion desinstalar`.

Para sacarlo:

```
manducar-impresion desinstalar
```

La configuración queda guardada: si volvés a instalar, no hace falta vincular
de nuevo. `desinstalar` va **sin sudo y con el mismo usuario** que instaló: es
en su carpeta donde está anotado el arranque automático.

## Los avisos de la primera vez

El ejecutable todavía no tiene firma de código, así que la primera vez el
sistema avisa. Es esperable, no es un virus.

- **Windows**: aparece un cartel azul, «Windows protegió tu PC» (SmartScreen).
  Tocá **«Más información»** y después **«Ejecutar de todas formas»**. Pasado
  eso, el asistente pregunta en cuadros de diálogo y no queda ninguna ventana
  negra abierta detrás.
- **Mac**: dice «Apple no pudo verificar que “Manducar Impresión” no contenga
  software malicioso». Cerrá el aviso, andá a **Ajustes del Sistema →
  Privacidad y seguridad**, bajá hasta el final y tocá **«Abrir de todos
  modos»** (pide la contraseña); después abrilo de nuevo y de ahí en adelante
  el doble clic anda. (Equivalente en la terminal:
  `xattr -dr com.apple.quarantine "Manducar Impresión.app"`.)
- **Linux**: `chmod +x manducar-impresion` y abrilo desde una terminal.

## Volver a vincular

Si la computadora ya estaba vinculada a otro local (o le cambiaste el código),
abrí el programa y elegí **«Volver a vincular con otro código»** —o corré
`vincular` de nuevo—: el agente se reinicia solo para tomar el vínculo nuevo,
y si no puede, te dice qué correr a mano.

## Como servicio del sistema (opcional, con administrador)

La instalación de arriba alcanza para cualquier local. Si la computadora la
comparten varios usuarios y querés que el agente imprima **aunque nadie tenga
la sesión iniciada**, está el modo sistema: los mismos comandos con
`--sistema`, corridos con `sudo` (Mac y Linux) o en una terminal abierta como
administrador (Windows).

```
sudo manducar-impresion vincular CODIGO --sistema
sudo manducar-impresion instalar --sistema
sudo manducar-impresion desinstalar --sistema
```

Ahí el agente queda como servicio del sistema —launchd, systemd o el Service
Control Manager de Windows—, arranca con la computadora y el gestor lo
reinicia si se cae. Todo vive en carpetas del sistema:

| | programa | configuración y log |
|---|---|---|
| Windows | `C:\Program Files\Manducar\` | `C:\ProgramData\Manducar\` |
| Mac | `/Library/Application Support/Manducar/` | la misma |
| Linux | `/usr/local/lib/manducar/` | `/etc/manducar/` |

No hace falta repetir `--sistema` cada vez: `instalar` anota el modo en la
configuración, y `desinstalar` lo lee de ahí. Y si vinculaste sin `--sistema`
y después instalás con él, la configuración se copia sola a la carpeta del
sistema.

Los dos modos juntos no: dos agentes en la misma computadora imprimen todo dos
veces. `instalar` se niega si ve que ya está el otro, en cualquiera de los dos
sentidos; sacá el que sobra y recién ahí instalá el que va.

Al pasar de un modo al otro, la configuración se copia sola y la que queda
atrás se aparta como `impresion.json.migrado`, para que no queden dos y el
agente termine leyendo la que no es.

## Ver qué está haciendo

De un vistazo —a qué local está vinculada, en qué modo, si arranca sola y
dónde están la configuración y el log—:

```
manducar-impresion estado
```

En primer plano, sin instalar nada, para probar:

```
manducar-impresion correr
```

Ojo: eso levanta **otro** agente. Si en esa computadora ya lo instalaste, los
dos se pelean los trabajos y algunas comandas salen dos veces; ahí lo que hay
que mirar es el log.

Instalado, el log queda en un archivo `impresion.log` al lado de la
configuración (las tablas de arriba). Al abrirlo, si el que había ya pasaba
los 10 MB, se lo guarda como `impresion.log.1` y se arranca uno nuevo: nunca
hay más de dos. (El corte se mira cuando el agente arranca o reabre el
archivo, no en el medio de una corrida larga.)

`correr --log-archivo` es lo mismo que `correr` pero mandando el log a ese
archivo en vez de a la pantalla: es como lo lanza el `.vbs` de Windows, donde
no hay ninguna ventana donde mirar. La variable de entorno
`MANDUCAR_IMPRESION_LOG=1` hace lo mismo.

Con `MANDUCAR_IMPRESION_CONFIG` se le puede pedir otra ruta para la
configuración —para probar sin tocar la de verdad—; hay que ponerla tanto al
vincular como al correr.

## Cómo se actualiza

Solo. Cuando el servidor anuncia una versión nueva, el agente se baja el
binario, verifica su SHA-256 y se reemplaza a sí mismo. No hace falta bajar
nada a mano ni reinstalar. Funciona igual en los dos modos: en el de usuario
el programa vive en una carpeta tuya, que es justamente donde puede escribir.

## Licencia

MIT. Ver [LICENSE](LICENSE).
