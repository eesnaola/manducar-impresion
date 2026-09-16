//go:build windows

package main

// La cuenta de procesos de la consola, contra la consola de verdad. Es el
// único test del asistente que necesita Windows, y es el que faltaba: la regla
// vieja miraba stdin, en Mac acertaba, y acá se equivocaba siempre —el doble
// clic quedaba clasificado como «lo abrieron desde una terminal» y la persona
// veía la ventana negra en vez de los cuadros—. Una tabla de casos no lo podía
// mostrar: la premisa que estaba mal era justamente la que el test daba por
// buena.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"
)

const (
	// envHijo hace que el binario de test, en vez de probar, se limite a
	// anotar cuántos procesos ve en su consola. Es el mismo binario relanzado.
	envHijo = "MANDUCAR_TEST_CONSOLA_SALIDA"
)

// TestConsolaHeredada: a esto lo largó `go test` desde la consola del que lo
// corrió, así que ahí arriba hay por lo menos un shell además de nosotros.
// Es el caso «desde la terminal», y tiene que dar compartida.
func TestConsolaHeredada(t *testing.T) {
	if os.Getenv(envHijo) != "" {
		t.Skip("esta corrida es el ayudante de TestConsolaPropia")
	}
	n := procesosEnLaConsola()
	if n == 0 {
		t.Skip("esta corrida no tiene consola: no hay nada que comparar")
	}
	if !consolaCompartida(n) {
		t.Fatalf("heredamos la consola de quien corrió el test y vimos %d proceso(s): tendría que haber más de uno", n)
	}
}

// TestConsolaPropia es el doble clic: una consola recién hecha para el proceso
// y nadie más colgado de ella. CREATE_NEW_CONSOLE es exactamente lo que le da
// el Explorador a un .exe de consola que se abre de un doble clic.
func TestConsolaPropia(t *testing.T) {
	if os.Getenv(envHijo) != "" {
		t.Skip("esta corrida es el ayudante de TestConsolaPropia")
	}
	// El hijo no puede contestar por la salida estándar: con una consola
	// propia no comparte ninguna con nosotros. Deja el número en un archivo.
	salida := filepath.Join(t.TempDir(), "procesos.txt")

	c := exec.Command(os.Args[0], "-test.run=TestAyudanteDeLaConsola", "-test.v")
	c.Env = append(os.Environ(), envHijo+"="+salida)
	c.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_CONSOLE}
	if err := c.Run(); err != nil {
		t.Fatalf("no se pudo relanzar el test con una consola propia: %v", err)
	}

	b, err := os.ReadFile(salida)
	if err != nil {
		t.Fatalf("el hijo no dejó la cuenta: %v", err)
	}
	n, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 32)
	if err != nil {
		t.Fatalf("el hijo anotó «%s», que no es un número: %v", b, err)
	}
	if n != 1 {
		t.Fatalf("con una consola propia vimos %d proceso(s): tendría que ser sólo el nuestro", n)
	}
	if consolaCompartida(uint32(n)) {
		t.Fatalf("con %d proceso(s) la consola se dio por compartida: el doble clic vuelve a caer en la ventana negra", n)
	}
}

// TestAyudanteDeLaConsola no prueba nada: es el hijo de TestConsolaPropia, que
// corre con su propia consola y anota cuántos procesos ve. Sin la variable de
// entorno se saltea, para no molestar en una corrida normal.
func TestAyudanteDeLaConsola(t *testing.T) {
	salida := os.Getenv(envHijo)
	if salida == "" {
		t.Skip("sólo corre relanzado por TestConsolaPropia")
	}
	if err := os.WriteFile(salida, []byte(strconv.FormatUint(uint64(procesosEnLaConsola()), 10)), 0o644); err != nil {
		t.Fatalf("no se pudo anotar la cuenta: %v", err)
	}
}
