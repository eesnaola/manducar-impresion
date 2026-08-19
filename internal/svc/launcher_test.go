package svc

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// El lanzador de Windows: es lo único que separa «arranca solo al entrar» de
// «le queda una consola negra abierta toda la noche». Se prueba acá y no en
// Windows porque es texto y nada más.
func TestVbsLauncherRunsItHiddenAndQuotesThePath(t *testing.T) {
	got := vbsLauncher(
		`C:\Users\Caja\AppData\Local\Manducar\manducar-impresion.exe`,
		`C:\Users\Caja\AppData\Roaming\Manducar\impresion.json`,
		"correr", "--log-archivo")

	quiero := `sh.Run """C:\Users\Caja\AppData\Local\Manducar\manducar-impresion.exe"" correr --log-archivo", 0, False`
	if !strings.Contains(got, quiero) {
		t.Fatalf("el .vbs quedó así:\n%s\ny tenía que tener:\n%s", got, quiero)
	}
	// El 0 es la ventana escondida y el False es «no lo esperes»: sin esos
	// dos, el que entra a la computadora ve una consola negra.
	if !strings.HasSuffix(strings.TrimRight(got, "\r\n"), ", 0, False") {
		t.Fatalf("tiene que terminar en «, 0, False»: %q", got)
	}
	if !strings.Contains(got, "\r\n") {
		t.Fatal("es un archivo de Windows: los renglones van con CRLF")
	}
	if !strings.HasPrefix(got, "'") {
		t.Fatalf("arranca con el comentario que dice quién lo escribió: %q", got)
	}
	for _, l := range strings.Split(strings.TrimRight(got, "\r\n"), "\r\n") {
		if l == "" {
			t.Fatal("no puede quedar un renglón vacío en el medio")
		}
	}
}

// La ruta de la configuración va clavada en el lanzador: instalado, el agente
// no la adivina. Sin esto, el que arranca al entrar puede leer la
// configuración del otro modo, o ninguna.
func TestVbsLauncherPinsTheConfigPath(t *testing.T) {
	got := vbsLauncher(`C:\Manducar\manducar-impresion.exe`,
		`C:\Users\Caja\AppData\Roaming\Manducar\impresion.json`, "correr")

	quiero := `sh.Environment("PROCESS").Item("MANDUCAR_IMPRESION_CONFIG") = "C:\Users\Caja\AppData\Roaming\Manducar\impresion.json"`
	if !strings.Contains(got, quiero) {
		t.Fatalf("quedó:\n%s\ny tenía que tener:\n%s", got, quiero)
	}
	// Y la variable se pone ANTES de largarlo, o no la hereda.
	if strings.Index(got, "Environment") > strings.Index(got, "sh.Run") {
		t.Fatal("la variable tiene que quedar puesta antes del Run")
	}
	// Sin ruta, no se inventa ningún renglón de entorno.
	sinRuta := vbsLauncher(`C:\Manducar\manducar-impresion.exe`, "", "correr")
	if strings.Contains(sinRuta, "Environment") {
		t.Fatalf("sin ruta no va la variable: %q", sinRuta)
	}
}

// Una ruta sin espacios tampoco puede quedar sin comillas: el .vbs se arma
// igual siempre, y así el que lo abre entiende qué está mirando.
func TestVbsLauncherAlwaysQuotesTheExecutable(t *testing.T) {
	got := vbsLauncher(`C:\Manducar\manducar-impresion.exe`, "", "correr")
	if !strings.Contains(got, `"""C:\Manducar\manducar-impresion.exe"" correr"`) {
		t.Fatalf("quedó %q", got)
	}
}

// Al desinstalar, la ruta del lanzador sale de lo que está anotado en la clave
// Run y no de volver a calcularla: si el agente se instaló en otra carpeta,
// calcularla borraría un archivo que no es y dejaría el de verdad arrancando.
func TestVbsFromRunValue(t *testing.T) {
	casos := []struct {
		nombre string
		valor  string
		quiero string
	}{
		{
			"lo que escribimos nosotros",
			`"C:\Windows\System32\wscript.exe" "C:\Users\Caja\AppData\Local\Manducar\manducar-impresion.vbs"`,
			`C:\Users\Caja\AppData\Local\Manducar\manducar-impresion.vbs`,
		},
		{
			"una carpeta distinta de la que calcularíamos",
			`"C:\Windows\System32\wscript.exe" "D:\Manducar\viejo\manducar-impresion.vbs"`,
			`D:\Manducar\viejo\manducar-impresion.vbs`,
		},
		{
			"editado a mano, sin comillas",
			`wscript.exe C:\Manducar\manducar-impresion.vbs`,
			`C:\Manducar\manducar-impresion.vbs`,
		},
		{"con mayúsculas", `"wscript.exe" "C:\M\LANZADOR.VBS"`, `C:\M\LANZADOR.VBS`},
		{"cualquier cosa", `otra cosa`, ""},
		{"vacío", ``, ""},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			if got := vbsFromRunValue(c.valor); got != c.quiero {
				t.Fatalf("dio %q y quería %q", got, c.quiero)
			}
		})
	}
}

// En VBScript la comilla se escapa duplicándola. Windows no deja poner una en
// el nombre de un archivo, pero una cadena mal escapada corta el renglón al
// medio y el agente no arranca nunca más: barato de cubrir.
func TestVbsStringDoublesTheQuotes(t *testing.T) {
	casos := map[string]string{
		`simple`:      `"simple"`,
		`con "algo"`:  `"con ""algo"""`,
		`C:\ruta\a.x`: `"C:\ruta\a.x"`,
		``:            `""`,
	}
	for in, quiero := range casos {
		if got := vbsString(in); got != quiero {
			t.Errorf("vbsString(%q) = %q, quería %q", in, got, quiero)
		}
	}
}

// Lo que se anota en la clave Run: wscript —el que no abre ventana— y el
// .vbs, los dos entrecomillados porque las dos rutas tienen espacios seguido.
func TestRunKeyCommandQuotesBoth(t *testing.T) {
	got := runKeyCommand(`C:\Windows\System32\wscript.exe`, `C:\Users\Caja\AppData\Local\Manducar\manducar-impresion.vbs`)
	quiero := `"C:\Windows\System32\wscript.exe" "C:\Users\Caja\AppData\Local\Manducar\manducar-impresion.vbs"`
	if got != quiero {
		t.Fatalf("quedó %q y tenía que ser %q", got, quiero)
	}
}

func TestQuoteWindowsArgOnlyWhenItHasTo(t *testing.T) {
	casos := map[string]string{
		"correr":             "correr",
		"--log-archivo":      "--log-archivo",
		`C:\Manducar\a.exe`:  `C:\Manducar\a.exe`,
		`C:\Archivos de p\a`: `"C:\Archivos de p\a"`,
		"":                   `""`,
	}
	for in, quiero := range casos {
		if got := quoteWindowsArg(in); got != quiero {
			t.Errorf("quoteWindowsArg(%q) = %q, quería %q", in, got, quiero)
		}
	}
}

// El ejecutable de la instalación sin permisos tiene que caer adentro del
// usuario: si cayera en una carpeta del sistema, ni la copia ni la
// actualización automática podrían escribirla.
func TestInstallPathUserVsSystem(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("LocalAppData", filepath.Join(home, "AppData", "Local"))
	os.Unsetenv("MANDUCAR_IMPRESION_BIN")

	usuario := installPath(true)
	if !strings.HasPrefix(usuario, home) {
		t.Fatalf("la instalación de usuario tiene que quedar adentro de %s, quedó %s", home, usuario)
	}
	nombre := "manducar-impresion"
	if runtime.GOOS == "windows" {
		nombre += ".exe"
	}
	if filepath.Base(usuario) != nombre {
		t.Fatalf("el ejecutable se llama %s, quedó %s", nombre, usuario)
	}

	sistema := installPath(false)
	if strings.HasPrefix(sistema, home) {
		t.Fatalf("la del sistema no puede estar adentro del usuario: %s", sistema)
	}

	// Y la variable de entorno sigue pisando a las dos: es con lo que se
	// prueba sin ensuciar la computadora.
	t.Setenv("MANDUCAR_IMPRESION_BIN", filepath.Join(home, "probando"))
	if installPath(true) != filepath.Join(home, "probando") || installPath(false) != filepath.Join(home, "probando") {
		t.Fatal("MANDUCAR_IMPRESION_BIN tiene que mandar en los dos modos")
	}
}
