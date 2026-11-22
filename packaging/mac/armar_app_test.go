package mac

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// compilarMac compila el agente para Mac con esas opciones del enlazador.
func compilarMac(t *testing.T, raiz, destino, ldflags string) ([]byte, error) {
	t.Helper()
	cmd := exec.Command("go", "build", "-trimpath", "-ldflags", ldflags, "-o", destino, ".")
	cmd.Dir = raiz
	cmd.Env = append(os.Environ(), "GOOS=darwin", "GOARCH=arm64", "CGO_ENABLED=0")
	return cmd.CombinedOutput()
}

// tieneIdentificador dice si el ejecutable trae el LC_UUID de Mach-O.
func tieneIdentificador(t *testing.T, binario string) bool {
	t.Helper()
	salida, err := exec.Command("otool", "-l", binario).Output()
	if err != nil {
		t.Fatalf("otool no pudo leer %s: %v", binario, err)
	}
	return bytes.Contains(salida, []byte("LC_UUID"))
}

// A quien baja el zip, macOS le dice «“Manducar Impresión” está dañado y no
// puede abrirse» si el .app no está sellado, y de ese cartel no se sale desde
// la pantalla. Pasó con todos los releases hasta la 0.1.11: el binario de Go
// trae la firma del linker, que vale para un ejecutable suelto y no para un
// paquete con Info.plist y recursos.
//
// Acá se arma el zip como lo arma el release, se lo abre como lo abre quien
// lo bajó y se le pregunta a macOS si el paquete cierra.
func TestElAppQueSaleDelZipEstaSellado(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("el sello lo pone y lo revisa codesign, que sólo existe en Mac")
	}
	raiz, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()

	binario := filepath.Join(tmp, "manducar-impresion-darwin-arm64")
	// Como lo compila el release: con `-B gobuildid`, que es lo que le pone el
	// identificador (ver el test de abajo).
	if salida, err := compilarMac(t, raiz, binario, "-s -w -B gobuildid -X main.Version=0.0.0"); err != nil {
		t.Fatalf("no compiló el binario de Mac: %v\n%s", err, salida)
	}

	zip := filepath.Join(tmp, "manducar-impresion-mac.zip")
	armar := exec.Command(filepath.Join(raiz, "packaging", "mac", "armar-app.sh"), binario, "0.0.0", zip)
	armar.Dir = raiz
	if salida, err := armar.CombinedOutput(); err != nil {
		t.Fatalf("armar-app.sh falló: %v\n%s", err, salida)
	}

	bajado := filepath.Join(tmp, "bajado")
	if salida, err := exec.Command("ditto", "-x", "-k", zip, bajado).CombinedOutput(); err != nil {
		t.Fatalf("el zip no se pudo abrir: %v\n%s", err, salida)
	}
	app := filepath.Join(bajado, "Manducar Impresión.app")
	if _, err := os.Stat(app); err != nil {
		t.Fatalf("del zip no salió «Manducar Impresión.app»: %v", err)
	}

	if salida, err := exec.Command("codesign", "--verify", "--deep", "--strict", app).CombinedOutput(); err != nil {
		t.Fatalf("macOS da el paquete por roto, y a quien lo baja le dice que está dañado:\n%s", salida)
	}
}

// macOS 15 decide si un programa puede hablar con la red del local (el permiso
// de «Red local») identificándolo por el LC_UUID de su ejecutable, y a un
// agente de launchd —que es como queda el nuestro al instalarse— el permiso le
// aplica. Go 1.23, el que compila los releases, no pone ese identificador
// salvo que se le pida con `-B gobuildid`; desde Go 1.24 lo pone solo. Sin él,
// macOS le puede negar la conexión sin preguntar nada: la impresora de red da
// «no hay camino hasta esa dirección» con la red en perfecto estado
// (golang/go#68678, Apple TN3179). Así salieron todos los binarios de Mac
// hasta la 0.1.11.
//
// El script que arma el paquete es la última parada antes de publicar: un
// binario sin identificador no se empaqueta.
func TestElScriptNoEmpaquetaUnBinarioSinIdentificador(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("el identificador se lee con otool, que sólo existe en Mac")
	}
	raiz, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()

	// Un binario sin identificador: con Go 1.24 o más nuevo hay que pedirlo
	// (`-B none`); el 1.23 no conoce esa opción y lo saca así sin que se pida.
	binario := filepath.Join(tmp, "manducar-impresion-darwin-arm64")
	if _, err := compilarMac(t, raiz, binario, "-s -w -B none -X main.Version=0.0.0"); err != nil {
		if salida, err := compilarMac(t, raiz, binario, "-s -w -X main.Version=0.0.0"); err != nil {
			t.Fatalf("no compiló el binario de Mac: %v\n%s", err, salida)
		}
	}
	if tieneIdentificador(t, binario) {
		t.Fatal("no pude fabricar un binario sin LC_UUID: este Go lo pone igual, y el test no prueba nada")
	}

	zip := filepath.Join(tmp, "manducar-impresion-mac.zip")
	armar := exec.Command(filepath.Join(raiz, "packaging", "mac", "armar-app.sh"), binario, "0.0.0", zip)
	armar.Dir = raiz
	salida, err := armar.CombinedOutput()
	if err == nil {
		t.Fatal("armar-app.sh empaquetó un binario sin LC_UUID: en macOS 15 ese agente puede quedarse sin poder hablar con las impresoras de red")
	}
	if !strings.Contains(string(salida), "LC_UUID") {
		t.Errorf("armar-app.sh falló, pero sin decir que falta el LC_UUID:\n%s", salida)
	}
	if _, err := os.Stat(zip); err == nil {
		t.Error("armar-app.sh falló y dejó igual un zip: alguien lo va a publicar")
	}
}
