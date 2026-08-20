# Probar el agente en Windows antes de un release

Dos capas. La primera corre sola; la segunda necesita ojos, una vez por
versión.

## 1. Lo automático: el workflow `windows-e2e`

Corre en cada push (Actions → windows-e2e). El agente real (`Run`) contra el
spooler del runner: una impresora «Generic / Text Only» que escribe a un
archivo hace de térmica. Verifica que imprime y reporta OK, que un papel
trabado queda «sin confirmar» a los 10 segundos, que al reanudar la cola el
vigilante avisa «al final salió», y mide qué pasa con un cancelado (con la
cola pausada Windows deja ver el DELETING; si el trabajo desaparece entre dos
miradas, sale como impreso: Windows no guarda historia).

## 2. Lo manual: una VM con ojos

En una Mac Apple Silicon: [UTM](https://mac.getutm.app) (gratis) con Windows
11 ARM — corre los `.exe` x64 por emulación, así se prueba el binario del
release, no una build especial. Si hay una PC real con Windows en la misma
red, mejor: el SmartScreen de verdad.

Preparación (una vez):

1. **Llegar al dev de la Mac.** En la VM, como administrador, agregar al
   `C:\Windows\System32\drivers\etc\hosts`:
   `192.168.64.1 pizzeria.manducar.localhost manducar.localhost`
   (la IP es la de la Mac vista desde la VM; en UTM con red compartida es
   192.168.64.1). El panel queda en
   `http://pizzeria.manducar.localhost:8081`.
2. **Una térmica de mentira.** Impresora nueva → «Generic / Text Only» con
   puerto TCP/IP crudo (RAW, 9100) a la IP de la Mac, con la impresora falsa
   (`tools/impresora-dummy.py` de manducar) escuchando. Pausar/cancelar se
   hace desde la ventanita de la cola, como un usuario real.

Checklist por versión (bajar el `.exe` DESDE EL NAVEGADOR de la VM, para que
traiga la marca de internet):

- [ ] SmartScreen: «Windows protegió tu PC» → Más información → Ejecutar de
      todas formas. Una sola vez.
- [ ] Doble clic: pregunta el código en un cuadro de diálogo, SIN ventana
      negra detrás.
- [ ] Código inválido y código vencido: lo dicen en un cuadro, ofrecen
      reintentar, a la tercera se rinden con gracia.
- [ ] Código bueno: vincula, instala en modo usuario (sin UAC), cuadro final
      «quedó vinculada… arranca solo».
- [ ] Cerrar sesión y volver a entrar: el agente arranca solo (aparece
      conectado en el panel).
- [ ] Imprimir prueba: sale por la térmica de mentira y queda «Impreso».
- [ ] Pausar la impresora en Windows → imprimir → «Sin confirmar» a los 10 s
      con su motivo.
- [ ] Reanudar → el papel sale y el panel pasa solo a «Impreso» (≤1 min).
- [ ] Pausar → imprimir → cancelar el trabajo en la ventanita → «Descartada»
      (si Windows lo borró demasiado rápido, queda «Impreso»: es el límite
      documentado).
- [ ] `estado` desde PowerShell: cuenta la verdad.
- [ ] Autoactualización: instalar el release anterior publicado, pegar el
      bloque del release nuevo en `printing.yaml` del dev, esperar el latido:
      se actualiza y sigue andando.
- [ ] Desinstalar: `.\manducar-impresion.exe desinstalar` deja todo limpio
      (HKCU Run sin la entrada, carpeta fuera).
