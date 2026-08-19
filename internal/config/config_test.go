package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSaveAndLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MANDUCAR_IMPRESION_CONFIG", filepath.Join(dir, "sub", "impresion.json"))

	in := Config{Server: "https://manduc.ar", Token: "abc", AgentID: 7, Mercure: Mercure{URL: "https://manduc.ar/.well-known/mercure", JWT: "jwt", Topic: "printing/agent/7"}}
	if err := Save(in); err != nil {
		t.Fatalf("save: %v", err)
	}
	out, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if out != in {
		t.Fatalf("round trip: %+v != %+v", out, in)
	}
	info, _ := os.Stat(filepath.Join(dir, "sub", "impresion.json"))
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("el archivo tiene el token: tiene que ser 0600, es %v", info.Mode().Perm())
	}
}

func TestLoadWithoutFileSaysSo(t *testing.T) {
	t.Setenv("MANDUCAR_IMPRESION_CONFIG", filepath.Join(t.TempDir(), "no.json"))
	if _, err := Load(); err == nil {
		t.Fatal("sin archivo tiene que fallar con un mensaje claro")
	}
}

// La configuración por default es la del usuario: es lo que hace que
// `vincular` no pida sudo ni administrador.
func TestUserPathLivesInsideTheUsersFolder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("AppData", filepath.Join(home, "AppData", "Roaming"))

	p, err := UserPath()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p, home) {
		t.Fatalf("%q tiene que estar adentro de la carpeta del usuario (%s)", p, home)
	}
	if filepath.Base(p) != "impresion.json" {
		t.Fatalf("el archivo se llama impresion.json, quedó %q", p)
	}
	carpeta := filepath.Base(filepath.Dir(p))
	quiero := "Manducar"
	if runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
		quiero = "manducar" // en Linux va en minúscula, como /etc/manducar
	}
	if carpeta != quiero {
		t.Fatalf("la carpeta tenía que ser %q y es %q (%s)", quiero, carpeta, p)
	}
}

// La del sistema es la de antes: la que escribe sólo un administrador.
func TestSystemPathIsTheOneOfTheServiceInstall(t *testing.T) {
	t.Setenv("ProgramData", `C:\ProgramData`)
	p, err := SystemPath()
	if err != nil {
		t.Fatal(err)
	}
	quiero := map[string]string{
		"windows": `C:\ProgramData\Manducar\impresion.json`,
		"darwin":  "/Library/Application Support/Manducar/impresion.json",
	}[runtime.GOOS]
	if quiero == "" {
		quiero = "/etc/manducar/impresion.json"
	}
	if p != quiero {
		t.Fatalf("quedó %q y tenía que ser %q", p, quiero)
	}
}

// Las tres formas de elegir archivo, en orden: la variable de entorno,
// después `--sistema`, después lo que exista.
func TestPathPrefersTheEnvAndThenTheMode(t *testing.T) {
	mía := filepath.Join(t.TempDir(), "mía.json")
	t.Setenv(EnvPath, mía)
	defer UseSystem(false)

	UseSystem(true)
	if p, err := Path(); err != nil || p != mía {
		t.Fatalf("la variable de entorno manda aun con --sistema: %q, %v", p, err)
	}
	os.Unsetenv(EnvPath)

	sys, _ := SystemPath()
	if p, err := Path(); err != nil || p != sys {
		t.Fatalf("con --sistema va la del sistema: %q, %v", p, err)
	}

	UseSystem(false)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("AppData", filepath.Join(home, "AppData", "Roaming"))
	user, _ := UserPath()
	if p, err := Path(); err != nil || p != user {
		t.Fatalf("sin nada instalado va la del usuario: %q, %v", p, err)
	}
}

// Sin que nadie diga cuál, manda el archivo que exista, y el del usuario
// primero: una instalación vieja —de las que se hacían con sudo— tiene que
// seguir andando sin tocar nada.
func TestResolvePicksTheFileThatExists(t *testing.T) {
	const user, sys = "/u/impresion.json", "/s/impresion.json"
	hay := func(existen ...string) func(string) bool {
		return func(p string) bool {
			for _, e := range existen {
				if e == p {
					return true
				}
			}
			return false
		}
	}
	casos := []struct {
		nombre string
		existe func(string) bool
		quiero string
	}{
		{"ninguno de los dos", hay(), user},
		{"sólo el del usuario", hay(user), user},
		{"sólo el del sistema", hay(sys), sys},
		{"los dos", hay(user, sys), user},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			if got := resolve(user, sys, c.existe); got != c.quiero {
				t.Fatalf("eligió %q y tenía que elegir %q", got, c.quiero)
			}
		})
	}
}

// `desinstalar` no le pide al del local que se acuerde de cómo instaló: lo
// dice el archivo, y si es uno viejo —sin el campo— lo dice dónde está.
func TestInstalledModeReadsTheFileAndFallsBackToWhereItIs(t *testing.T) {
	p := filepath.Join(t.TempDir(), "impresion.json")
	t.Setenv(EnvPath, p)

	if err := SaveTo(p, Config{Server: "https://x", Mode: ModeSystem}); err != nil {
		t.Fatal(err)
	}
	if m := InstalledMode(); m != ModeSystem {
		t.Fatalf("con «mode: system» en el archivo quedó %q", m)
	}
	if err := SaveTo(p, Config{Server: "https://x"}); err != nil {
		t.Fatal(err)
	}
	if m := InstalledMode(); m != ModeUser {
		t.Fatalf("sin campo y fuera de la carpeta del sistema es de usuario, quedó %q", m)
	}

	sys, _ := SystemPath()
	if m := ModeFor(sys); m != ModeSystem {
		t.Fatalf("la carpeta del sistema es modo sistema, quedó %q", m)
	}
	user, _ := UserPath()
	if m := ModeFor(user); m != ModeUser {
		t.Fatalf("la del usuario es modo usuario, quedó %q", m)
	}
}

// El modo viaja en el archivo: si no sobrevive a la ida y vuelta, `correr` y
// `desinstalar` le hablan al gestor equivocado.
func TestModeSurvivesTheRoundTrip(t *testing.T) {
	t.Setenv(EnvPath, filepath.Join(t.TempDir(), "impresion.json"))
	if err := Save(Config{Server: "https://x", Token: "t", Mode: ModeSystem}); err != nil {
		t.Fatal(err)
	}
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Mode != ModeSystem {
		t.Fatalf("el modo se perdió: %+v", c)
	}
}

// Salir del modo sistema sin ser administrador no puede ser un callejón sin
// salida: si el archivo del sistema no es nuestro, `vincular` escribe el del
// usuario y lo dice, en vez de fallar y mandar a correr todo con sudo.
func TestWritePathFallsBackWhenTheSystemConfigIsNotOurs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("AppData", filepath.Join(home, "AppData", "Roaming"))
	t.Setenv(EnvPath, "")
	defer UseSystem(false)
	UseSystem(false)

	sys, _ := SystemPath()
	user, _ := UserPath()
	viejoE, viejoW := fileExists, writable
	t.Cleanup(func() { fileExists, writable = viejoE, viejoW })
	// La computadora quedó de una instalación vieja: sólo está la del sistema.
	fileExists = func(p string) bool { return p == sys }

	// Corriendo como administrador, se escribe la del sistema, como siempre.
	writable = func(string) bool { return true }
	if p, cambió, err := WritePath(); err != nil || p != sys || cambió {
		t.Fatalf("con permisos va la del sistema: %q, cambió=%v, %v", p, cambió, err)
	}

	// Sin permisos, se pasa a la del usuario y avisa que se pasó.
	writable = func(string) bool { return false }
	p, cambió, err := WritePath()
	if err != nil {
		t.Fatal(err)
	}
	if p != user || !cambió {
		t.Fatalf("sin permisos tenía que pasarse a %q y avisar; dio %q, cambió=%v", user, p, cambió)
	}
}

// Con --sistema o con la variable de entorno no hay adivinanza que valga: el
// que pidió sabe lo que pidió, y si falla tiene que fallar ahí.
func TestWritePathDoesNotSecondGuessAnExplicitChoice(t *testing.T) {
	defer UseSystem(false)
	viejoE, viejoW := fileExists, writable
	t.Cleanup(func() { fileExists, writable = viejoE, viejoW })
	fileExists = func(string) bool { return true }
	writable = func(string) bool { return false }

	t.Setenv(EnvPath, "")
	UseSystem(true)
	sys, _ := SystemPath()
	if p, cambió, _ := WritePath(); p != sys || cambió {
		t.Fatalf("con --sistema se escribe la del sistema: %q, cambió=%v", p, cambió)
	}

	UseSystem(false)
	mía := filepath.Join(t.TempDir(), "mía.json")
	t.Setenv(EnvPath, mía)
	if p, cambió, _ := WritePath(); p != mía || cambió {
		t.Fatalf("la variable de entorno manda: %q, cambió=%v", p, cambió)
	}
}
