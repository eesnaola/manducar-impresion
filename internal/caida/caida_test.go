package caida

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/eesnaola/manducar-impresion/internal/api"
	"github.com/eesnaola/manducar-impresion/internal/vigia"
)

func escribir(t *testing.T, dir, nombre, contenido string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, nombre), []byte(contenido), 0o644); err != nil {
		t.Fatal(err)
	}
}

func agregar(t *testing.T, dir, nombre, contenido string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(dir, nombre), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(contenido); err != nil {
		t.Fatal(err)
	}
}

// anotarCaida deja en el archivo lo que dejaría una caída del agente que el
// vigía vio terminar: el panic de Go y, atrás, la salida que anota el vigía.
func anotarCaida(t *testing.T, dir, titulo, pila string, codigo int, cuando time.Time) {
	t.Helper()
	agregar(t, dir, Archivo, "panic: "+titulo+"\n\ngoroutine 1 [running]:\n"+pila+"()\n")
	if err := AnotarSalida(dir, codigo, cuando); err != nil {
		t.Fatal(err)
	}
}

// informe lee la caída y corta el test si no hay ninguna: los tests que miran
// sus campos no pueden desreferenciar un nil.
func informe(t *testing.T, dir string) *api.CrashReport {
	t.Helper()
	inf, err := Leer(dir, "0.1.9")
	if err != nil || inf == nil {
		t.Fatalf("Leer = %v, %v", inf, err)
	}
	return inf
}

func TestSinArchivoNoHayInforme(t *testing.T) {
	inf, err := Leer(t.TempDir(), "0.1.9")
	if err != nil || inf != nil {
		t.Fatalf("Leer = %v, %v; quiero nil, nil", inf, err)
	}
}

func TestUnArchivoVacioNoEsUnaCaida(t *testing.T) {
	dir := t.TempDir()
	escribir(t, dir, Archivo, "\n  \n")
	if inf, err := Leer(dir, "0.1.9"); err != nil || inf != nil {
		t.Fatalf("un archivo vacío dio Leer = %v, %v", inf, err)
	}
}

// Lo que el agente vigilado escribe suelto en la salida de error termina en
// impresion.crash. Sin marca del vigía ni nada de lo que Go deja al caerse no
// es una caída: Leer no arma informe y dice qué había.
func TestTextoSueltoNoEsUnaCaida(t *testing.T) {
	dir := t.TempDir()
	escribir(t, dir, Archivo, "\n2026/10/13 10:00:00 no se pudo rotar el log: el archivo está en uso\n")
	inf, err := Leer(dir, "0.1.9")
	if inf != nil || !errors.Is(err, ErrNoEsCaida) {
		t.Fatalf("Leer = %v, %v; quiero nil y ErrNoEsCaida", inf, err)
	}
	if !strings.Contains(err.Error(), "no se pudo rotar el log: el archivo está en uso") {
		t.Errorf("el error no dice qué había: %v", err)
	}
}

// El mismo texto con una salida mala que anotó el vigía sí es una caída.
func TestTextoSueltoConLaMarcaDelVigiaEsUnaCaida(t *testing.T) {
	dir := t.TempDir()
	escribir(t, dir, Archivo, "config: no hay token\n")
	if err := AnotarSalida(dir, 1, time.Date(2026, 10, 13, 10, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	inf := informe(t, dir)
	if inf.Codigo == nil || *inf.Codigo != 1 || inf.Resumen != "config: no hay token" {
		t.Errorf("informe = %+v", inf)
	}
}

func TestElResumenEsLaLineaDelPanicYElCodigoLoAnotoElVigia(t *testing.T) {
	dir := t.TempDir()
	escribir(t, dir, Archivo, "panic: runtime error: index out of range [3] with length 3\n\ngoroutine 7 [running]:\nmain.algo()\n")
	cuando := time.Date(2026, 10, 12, 23, 32, 0, 0, time.UTC)
	if err := AnotarSalida(dir, 2, cuando); err != nil {
		t.Fatal(err)
	}

	inf := informe(t, dir)
	if inf.Resumen != "panic: runtime error: index out of range [3] with length 3" {
		t.Errorf("Resumen = %q", inf.Resumen)
	}
	if inf.Codigo == nil || *inf.Codigo != 2 {
		t.Errorf("Codigo = %v, quiero 2", inf.Codigo)
	}
	if inf.Ocurrio != "2026-10-12T23:32:00Z" {
		t.Errorf("Ocurrio = %q", inf.Ocurrio)
	}
	if inf.Version != "0.1.9" || inf.Sistema == "" {
		t.Errorf("Version/Sistema = %q/%q", inf.Version, inf.Sistema)
	}
	if !strings.Contains(inf.Detalle, "goroutine 7") {
		t.Errorf("el detalle no trae la pila: %q", inf.Detalle)
	}
}

func TestSinPanicElResumenEsLaSalidaQueVioElVigia(t *testing.T) {
	dir := t.TempDir()
	if err := AnotarSalida(dir, 1, time.Date(2026, 10, 12, 23, 32, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}

	// Sin la fecha: ésa ya va aparte, en Ocurrio.
	if inf := informe(t, dir); inf.Resumen != "el agente terminó con código 1" {
		t.Fatalf("Resumen = %q", inf.Resumen)
	}
}

func TestSinMarcaDelVigiaNoHayCodigo(t *testing.T) {
	dir := t.TempDir()
	escribir(t, dir, Archivo, "panic: algo\n\ngoroutine 1 [running]:\nmain.algo()\n")

	inf := informe(t, dir)
	if inf.Codigo != nil {
		t.Errorf("Codigo = %d, quiero nil: nadie lo anotó", *inf.Codigo)
	}
}

func TestDosCaidasSinInformarSeCuentaSoloLaUltima(t *testing.T) {
	dir := t.TempDir()
	anotarCaida(t, dir, "caída A", "main.pilaDeA", 1, time.Date(2026, 10, 12, 22, 0, 0, 0, time.UTC))
	anotarCaida(t, dir, "caída B", "main.pilaDeB", 2, time.Date(2026, 10, 12, 23, 0, 0, 0, time.UTC))

	inf := informe(t, dir)
	if inf.Resumen != "panic: caída B" {
		t.Errorf("Resumen = %q", inf.Resumen)
	}
	if inf.Codigo == nil || *inf.Codigo != 2 {
		t.Errorf("Codigo = %v, quiero 2", inf.Codigo)
	}
	if inf.Ocurrio != "2026-10-12T23:00:00Z" {
		t.Errorf("Ocurrio = %q", inf.Ocurrio)
	}
	if !strings.Contains(inf.Detalle, "main.pilaDeB") {
		t.Errorf("el detalle no trae la pila de B: %q", inf.Detalle)
	}
	if strings.Contains(inf.Detalle, "pilaDeA") || strings.Contains(inf.Detalle, "caída A") {
		t.Errorf("el detalle arrastra la caída A: %q", inf.Detalle)
	}
}

func TestTresCaidasSinInformarSeCuentaSoloLaUltima(t *testing.T) {
	dir := t.TempDir()
	anotarCaida(t, dir, "caída A", "main.pilaDeA", 1, time.Date(2026, 10, 12, 21, 0, 0, 0, time.UTC))
	anotarCaida(t, dir, "caída B", "main.pilaDeB", 2, time.Date(2026, 10, 12, 22, 0, 0, 0, time.UTC))
	anotarCaida(t, dir, "caída C", "main.pilaDeC", 3, time.Date(2026, 10, 12, 23, 0, 0, 0, time.UTC))

	inf := informe(t, dir)
	if inf.Resumen != "panic: caída C" || inf.Codigo == nil || *inf.Codigo != 3 {
		t.Errorf("Resumen/Codigo = %q/%v", inf.Resumen, inf.Codigo)
	}
	if strings.Contains(inf.Detalle, "pilaDeA") || strings.Contains(inf.Detalle, "pilaDeB") {
		t.Errorf("el detalle arrastra caídas anteriores: %q", inf.Detalle)
	}
}

// Sin vigía (Mac, Linux) no hay marcas: la última caída empieza en el último
// encabezado que está en la columna 0. Los panics anidados de un mismo volcado
// van con tabulación y no cuentan.
func TestDosPanicsSinVigiaSeCuentaElUltimo(t *testing.T) {
	dir := t.TempDir()
	escribir(t, dir, Archivo, ""+
		"panic: primero\n\ngoroutine 1 [running]:\nmain.pilaDelPrimero()\n"+
		"panic: segundo [recovered]\n\tpanic: anidado\n\ngoroutine 1 [running]:\nmain.pilaDelSegundo()\n")

	inf := informe(t, dir)
	if inf.Resumen != "panic: segundo [recovered]" {
		t.Errorf("Resumen = %q", inf.Resumen)
	}
	if inf.Codigo != nil {
		t.Errorf("Codigo = %d, quiero nil", *inf.Codigo)
	}
	if !strings.Contains(inf.Detalle, "main.pilaDelSegundo") || !strings.Contains(inf.Detalle, "panic: anidado") {
		t.Errorf("el detalle no trae el segundo volcado entero: %q", inf.Detalle)
	}
	if strings.Contains(inf.Detalle, "pilaDelPrimero") {
		t.Errorf("el detalle arrastra el primer panic: %q", inf.Detalle)
	}
}

func TestUnFatalErrorTambienEncabezaUnaCaida(t *testing.T) {
	dir := t.TempDir()
	escribir(t, dir, Archivo, "panic: viejo\n\ngoroutine 1 [running]:\nmain.viejo()\n"+
		"fatal error: all goroutines are asleep - deadlock!\n\ngoroutine 1 [chan receive]:\nmain.nuevo()\n")

	inf := informe(t, dir)
	if inf.Resumen != "fatal error: all goroutines are asleep - deadlock!" {
		t.Errorf("Resumen = %q", inf.Resumen)
	}
	if strings.Contains(inf.Detalle, "main.viejo") {
		t.Errorf("el detalle arrastra el panic viejo: %q", inf.Detalle)
	}
}

// pilaDelRuntime imita lo que Go escribe después del encabezado de un error
// fatal: la pila del runtime y el volcado de cada goroutine, hasta pasar los
// 16 KB del detalle.
func pilaDelRuntime() string {
	var b strings.Builder
	b.WriteString("runtime stack:\n" +
		"runtime.throw({0x1029c3a5e?, 0x14000107e48?})\n" +
		"\t/usr/local/go/src/runtime/panic.go:1101 +0x38\n" +
		"runtime.newstack()\n" +
		"\t/usr/local/go/src/runtime/stack.go:1107 +0x4c8\n" +
		"runtime.morestack()\n" +
		"\t/usr/local/go/src/runtime/asm_arm64.s:392 +0x70\n\n" +
		"goroutine 1 gp=0x14000002380 m=0 mp=0x102bfc1c0 [running]:\n")
	for i := 0; b.Len() < 40<<10; i++ {
		fmt.Fprintf(&b, "main.recursivo(0x%x)\n\t/agente/main.go:12 +0x24 fp=0x14020160390 sp=0x14020160370\n", i)
	}
	b.WriteString("\ngoroutine 6 gp=0x140000a2000 m=nil [select]:\nmain.otra()\n")
	return b.String()
}

// Un error fatal del runtime (desborde de pila, un deadlock) con la salida de
// error del agente en el archivo, como la deja el vigía: el encabezado viene
// primero y atrás kilos de pila. El resumen es el encabezado, y el detalle
// arranca con él.
func TestUnErrorFatalDeVerdadSeResumeConSuEncabezado(t *testing.T) {
	dir := t.TempDir()
	escribir(t, dir, Archivo, "fatal error: stack overflow\n\n"+pilaDelRuntime())
	if err := AnotarSalida(dir, 2, time.Date(2026, 10, 12, 23, 32, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}

	inf := informe(t, dir)
	if inf.Resumen != "fatal error: stack overflow" {
		t.Errorf("Resumen = %q", inf.Resumen)
	}
	if !strings.HasPrefix(inf.Detalle, "fatal error: stack overflow\n") || len(inf.Detalle) > detalleMax {
		t.Errorf("detalle: %d bytes, empieza con %q", len(inf.Detalle), inf.Detalle[:min(40, len(inf.Detalle))])
	}
	if inf.Codigo == nil || *inf.Codigo != 2 {
		t.Errorf("Codigo = %v, quiero 2", inf.Codigo)
	}
}

// Sin el encabezado (lo que copia SetCrashOutput de un error fatal: Go
// escribe «fatal error: …» antes de darse por caído), el resumen no puede ser
// «runtime stack:» ni el renglón de una goroutine: es el primero que dice algo.
func TestSinEncabezadoElResumenSaltaLoQueNoDiceNada(t *testing.T) {
	casos := map[string]struct{ texto, quiero string }{
		"pila del runtime": {pilaDelRuntime(), "runtime.throw({0x1029c3a5e?, 0x14000107e48?})"},
		"señal y goroutine": {"[signal SIGSEGV: segmentation violation code=0x2 addr=0x0 pc=0x1028]\n\n" +
			"goroutine 7 gp=0x14000102a80 m=4 mp=0x14000100008 [running]:\nmain.algo()\n", "main.algo()"},
		"goroutine a secas": {"goroutine 1 [running]:\nmain.otro()\n", "main.otro()"},
		"excepción de Windows": {"Exception 0xc0000005 0x0 0x0 0x7ffd3c2a1b2c\nPC=0x7ffd3c2a1b2c\n\nruntime.cgocall(0x7ff6)\n",
			"Exception 0xc0000005 0x0 0x0 0x7ffd3c2a1b2c"},
	}
	for nombre, c := range casos {
		t.Run(nombre, func(t *testing.T) {
			dir := t.TempDir()
			escribir(t, dir, Archivo, c.texto)
			if err := AnotarSalida(dir, 2, time.Date(2026, 10, 12, 23, 32, 0, 0, time.UTC)); err != nil {
				t.Fatal(err)
			}
			if inf := informe(t, dir); inf.Resumen != c.quiero {
				t.Errorf("Resumen = %q, quiero %q", inf.Resumen, c.quiero)
			}
		})
	}
}

// Un archivo que creció sin que nadie lo vacíe: sólo se mira el final.
func TestLeerSoloMiraLosUltimos64KB(t *testing.T) {
	dir := t.TempDir()
	escribir(t, dir, Archivo, "panic: lejano\n"+strings.Repeat("relleno de la pila\n", 10<<10))
	if err := AnotarSalida(dir, 3, time.Date(2026, 10, 12, 23, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}

	inf := informe(t, dir)
	// El «panic: lejano» quedó a casi 200 KB del final: no se lo vio. Lo que
	// queda es el primer renglón que sí se ve.
	if inf.Resumen != "relleno de la pila" || strings.Contains(inf.Detalle, "lejano") {
		t.Errorf("Resumen = %q", inf.Resumen)
	}
	if inf.Codigo == nil || *inf.Codigo != 3 {
		t.Errorf("Codigo = %v, quiero 3", inf.Codigo)
	}
}

func TestElLogTraeLasUltimasDoscientasLineas(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	for i := 1; i <= 500; i++ {
		fmt.Fprintf(&b, "renglón %03d\n", i)
	}
	escribir(t, dir, "impresion.log", b.String())
	escribir(t, dir, "impresion.log.1", "de la rotación\n")
	escribir(t, dir, Archivo, "panic: algo\n")

	inf := informe(t, dir)
	lineas := strings.Split(strings.TrimRight(inf.Log, "\n"), "\n")
	if len(lineas) != 200 || lineas[0] != "renglón 301" || lineas[199] != "renglón 500" {
		t.Fatalf("log: %d líneas, primera %q, última %q", len(lineas), lineas[0], lineas[len(lineas)-1])
	}
}

// Al arrancar el agente rota impresion.log a impresion.log.1 si pasó de 10 MB,
// antes de que se lea la caída: el log nuevo casi no tiene nada.
func TestElLogCortoSeCompletaConElRotado(t *testing.T) {
	dir := t.TempDir()
	var viejo, nuevo strings.Builder
	for i := 1; i <= 300; i++ {
		fmt.Fprintf(&viejo, "viejo %03d\n", i)
	}
	for i := 1; i <= 50; i++ {
		fmt.Fprintf(&nuevo, "nuevo %02d\n", i)
	}
	escribir(t, dir, "impresion.log.1", viejo.String())
	escribir(t, dir, "impresion.log", nuevo.String())
	escribir(t, dir, Archivo, "panic: algo\n")

	inf := informe(t, dir)
	lineas := strings.Split(strings.TrimRight(inf.Log, "\n"), "\n")
	if len(lineas) != 200 {
		t.Fatalf("log: %d líneas, quiero 200", len(lineas))
	}
	if lineas[0] != "viejo 151" || lineas[149] != "viejo 300" || lineas[150] != "nuevo 01" || lineas[199] != "nuevo 50" {
		t.Errorf("log: primera %q, [149] %q, [150] %q, última %q", lineas[0], lineas[149], lineas[150], lineas[199])
	}
}

func TestSiFaltaElLogActualAlcanzaConElRotado(t *testing.T) {
	dir := t.TempDir()
	escribir(t, dir, "impresion.log.1", "uno\ndos\n")
	escribir(t, dir, Archivo, "panic: algo\n")

	if inf := informe(t, dir); inf.Log != "uno\ndos\n" {
		t.Errorf("Log = %q", inf.Log)
	}
}

func TestUnLogVacioNoDejaUnSaltoDeLinea(t *testing.T) {
	dir := t.TempDir()
	escribir(t, dir, Archivo, "panic: algo\n")

	if inf := informe(t, dir); inf.Log != "" {
		t.Errorf("sin log, Log = %q", inf.Log)
	}
	escribir(t, dir, "impresion.log", "")
	if inf := informe(t, dir); inf.Log != "" {
		t.Errorf("con el log vacío, Log = %q", inf.Log)
	}
}

// El detalle se queda con el principio de la caída —Go escribe primero el
// motivo y la goroutine que se cayó, y atrás el resto—; el log, con el final.
func TestLosTopesGuardanElPrincipioDelDetalleYElFinalDelLog(t *testing.T) {
	dir := t.TempDir()
	escribir(t, dir, Archivo, "panic: grande\n"+strings.Repeat("x", 40<<10)+"FIN")
	escribir(t, dir, "impresion.log", strings.Repeat(strings.Repeat("y", 1000)+"\n", 100)+"ULTIMA\n")

	inf := informe(t, dir)
	if len(inf.Detalle) > detalleMax || !strings.HasPrefix(inf.Detalle, "panic: grande\n") || strings.Contains(inf.Detalle, "FIN") {
		t.Errorf("detalle: %d bytes, empieza con %q", len(inf.Detalle), inf.Detalle[:min(20, len(inf.Detalle))])
	}
	if len(inf.Log) > logMax || !strings.Contains(inf.Log, "ULTIMA") {
		t.Errorf("log: %d bytes", len(inf.Log))
	}
	if inf.Resumen != "panic: grande" {
		t.Errorf("Resumen = %q", inf.Resumen)
	}
}

func TestVaciarDejaElArchivoSinCaida(t *testing.T) {
	dir := t.TempDir()
	escribir(t, dir, Archivo, "panic: algo\n")
	if err := Vaciar(dir); err != nil {
		t.Fatal(err)
	}
	if inf, err := Leer(dir, "0.1.9"); err != nil || inf != nil {
		t.Fatalf("después de vaciar, Leer = %v, %v", inf, err)
	}
}

// caerseConPreparar corre el test de abajo como proceso hijo, que llama a
// Preparar sobre dir y se cae de verdad. Con salidaDeError, el hijo es un
// agente vigilado: lleva la marca y su salida de error va ahí, que es lo que
// arma el vigía.
func caerseConPreparar(t *testing.T, dir string, salidaDeError *os.File) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestPrepararDejaElPanicEnElArchivo$")
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, vigia.EnvVigilado+"=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, "CAIDA_HIJO="+dir)
	if salidaDeError != nil {
		cmd.Env = append(cmd.Env, vigia.EnvVigilado+"=1")
		cmd.Stderr = salidaDeError
	}
	if err := cmd.Run(); err == nil {
		t.Fatal("el hijo tenía que caerse")
	}
}

// El proceso de verdad se cae después de Preparar: el panic queda en el archivo.
func TestPrepararDejaElPanicEnElArchivo(t *testing.T) {
	if dir := os.Getenv("CAIDA_HIJO"); dir != "" {
		if err := Preparar(dir); err != nil {
			fmt.Println(err)
			os.Exit(9)
		}
		// En otra goroutine, como una caída de verdad: un panic en la función
		// de test lo atrapa el paquete testing y lo relanza con «[recovered]».
		go func() { panic("se cayó a propósito") }()
		time.Sleep(5 * time.Second)
		os.Exit(8) // no se cayó
	}
	dir := t.TempDir()
	caerseConPreparar(t, dir, nil)
	inf := informe(t, dir)
	if inf.Resumen != "panic: se cayó a propósito" {
		t.Errorf("Resumen = %q", inf.Resumen)
	}
}

// Un archivo que nadie vació en meses: Preparar lo achica antes de abrirlo para
// que el proceso escriba.
func TestPrepararCompactaElArchivoGrande(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	for i := 1; b.Len() < 2<<20; i++ {
		fmt.Fprintf(&b, "renglón viejo %07d\n", i)
	}
	b.WriteString("panic: el último\n")
	escribir(t, dir, Archivo, b.String())

	caerseConPreparar(t, dir, nil)

	info, err := os.Stat(filepath.Join(dir, Archivo))
	if err != nil {
		t.Fatal(err)
	}
	// Lo que quedó de antes (64 KB como mucho) más el volcado del hijo.
	if info.Size() > 64<<10+16<<10 {
		t.Errorf("el archivo pesa %d bytes después de Preparar", info.Size())
	}
	raw, _ := os.ReadFile(filepath.Join(dir, Archivo))
	if strings.Contains(string(raw), "renglón viejo 0000001\n") || !strings.Contains(string(raw), "panic: el último\n") {
		t.Errorf("no se quedó con el final del archivo")
	}
	if inf := informe(t, dir); inf.Resumen != "panic: se cayó a propósito" {
		t.Errorf("Resumen = %q", inf.Resumen)
	}
}

// Vigilado, la salida de error del agente ya va a impresion.crash (la abrió el
// vigía): Preparar no le pide además a Go la copia, que dejaría la caída dos
// veces. El archivo grande lo achica igual.
func TestVigiladoPrepararNoDuplicaLaCaidaPeroCompacta(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	for i := 1; b.Len() < 2<<20; i++ {
		fmt.Fprintf(&b, "renglón viejo %07d\n", i)
	}
	escribir(t, dir, Archivo, b.String())
	f, err := os.OpenFile(filepath.Join(dir, Archivo), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	caerseConPreparar(t, dir, f)

	raw, err := os.ReadFile(filepath.Join(dir, Archivo))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(raw), "panic: se cayó a propósito"); n != 1 {
		t.Errorf("la caída quedó %d veces en el archivo; quiero 1", n)
	}
	if len(raw) > 64<<10+16<<10 || strings.Contains(string(raw), "renglón viejo 0000001\n") {
		t.Errorf("no se achicó: %d bytes", len(raw))
	}
	if inf := informe(t, dir); inf.Resumen != "panic: se cayó a propósito" {
		t.Errorf("Resumen = %q", inf.Resumen)
	}
}

func TestCompactarSoloAchicaLoQuePasaDeUnMega(t *testing.T) {
	dir := t.TempDir()
	chico := strings.Repeat("renglón\n", 100<<10) // casi 900 KB
	escribir(t, dir, Archivo, chico)
	if err := compactar(dir); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(filepath.Join(dir, Archivo)); string(raw) != chico {
		t.Fatalf("un archivo bajo el umbral se tocó: %d bytes", len(raw))
	}

	var b strings.Builder
	for i := 1; b.Len() < 2<<20; i++ {
		fmt.Fprintf(&b, "renglón %07d\n", i)
	}
	b.WriteString("FIN\n")
	escribir(t, dir, Archivo, b.String())
	if err := compactar(dir); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, Archivo))
	if len(raw) > colaMax || len(raw) < colaMax-100 {
		t.Errorf("quedaron %d bytes, quiero poco menos de %d", len(raw), colaMax)
	}
	if !strings.HasPrefix(string(raw), "renglón ") || !strings.HasSuffix(string(raw), "\nFIN\n") {
		t.Errorf("no empieza en un renglón entero o no conserva el final: %q ... %q", raw[:20], raw[len(raw)-20:])
	}
}

func TestCompactarSinArchivoNoHaceNada(t *testing.T) {
	if err := compactar(t.TempDir()); err != nil {
		t.Fatal(err)
	}
}

// Los topes del detalle (el principio) y del log (el final) no cortan a la
// mitad de una letra: el servidor recibiría un carácter roto.
func TestElCorteNoPartePorLaMitadUnCaracter(t *testing.T) {
	s := final("ñ"+strings.Repeat("a", 9), 10) // 11 bytes: el corte cae dentro de la ñ
	if !utf8.ValidString(s) || s != strings.Repeat("a", 9) {
		t.Errorf("final = %q", s)
	}
	s = principio(strings.Repeat("a", 9)+"ñ", 10)
	if !utf8.ValidString(s) || s != strings.Repeat("a", 9) {
		t.Errorf("principio = %q", s)
	}
	if s := principio(strings.Repeat("a", 8)+"ñ", 10); s != strings.Repeat("a", 8)+"ñ" {
		t.Errorf("principio cortó una ñ que entraba entera: %q", s)
	}
}
