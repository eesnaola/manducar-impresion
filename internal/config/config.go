// Package config guarda lo único que el agente necesita recordar entre
// arranques: a qué servidor le habla, con qué token, y cómo se suscribe al hub.
//
// Por default todo eso vive en la carpeta del usuario que corre el agente
// —%AppData% en Windows, ~/Library/Application Support en Mac, ~/.config en
// Linux—: así vincular e instalar no piden sudo ni administrador, que es lo
// que quiere el local. Con `--sistema` vuelve a la carpeta del sistema, que
// es la de la instalación como servicio para todos los usuarios.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// EnvPath es la variable con la que se le pide al agente otra ruta: la usa el
// que prueba en su máquina sin ensuciar la configuración de verdad.
const EnvPath = "MANDUCAR_IMPRESION_CONFIG"

// Los dos modos en los que puede quedar instalado el agente.
const (
	ModeUser   = "user"   // en la carpeta del usuario, sin permisos especiales
	ModeSystem = "system" // servicio del sistema, con sudo o administrador
)

type Mercure struct {
	URL   string `json:"url"`
	JWT   string `json:"jwt"`
	Topic string `json:"topic"`
}

type Config struct {
	Server  string  `json:"server"`
	Token   string  `json:"token"`
	AgentID int     `json:"agentId"`
	Mercure Mercure `json:"mercure"`
	// Store es el nombre del local, tal como lo contestó el servidor al
	// vincular. Para imprimir no hace falta: está para poder decirle a la
	// persona a qué local quedó atada esta computadora sin preguntarle nada
	// al servidor. Una configuración vieja no lo tiene, y ahí se muestra el
	// servidor.
	Store string `json:"store,omitempty"`
	// Mode es cómo quedó instalado: lo anotan `vincular` e `instalar`, y lo
	// leen `desinstalar` y `correr` para hablarle al gestor que corresponde
	// sin que haya que repetir `--sistema` cada vez.
	Mode string `json:"mode,omitempty"`
}

// system lo prende `--sistema`: para el resto del proceso, la configuración
// que manda es la del sistema, sin adivinar.
var system bool

// UseSystem fija el modo del proceso. Lo llama main al ver `--sistema`.
func UseSystem(v bool) { system = v }

// Path es la ruta del archivo de configuración: la que diga EnvPath, la del
// modo pedido con `--sistema`, o —si nadie dijo nada— la que exista.
func Path() (string, error) {
	if p := os.Getenv(EnvPath); p != "" {
		return p, nil
	}
	if system {
		return SystemPath()
	}
	user, userErr := UserPath()
	sys, sysErr := SystemPath()
	if userErr != nil {
		if sysErr != nil {
			return "", userErr
		}
		return sys, nil
	}
	if sysErr != nil {
		sys = ""
	}
	return resolve(user, sys, fileExists), nil
}

// resolve elige entre el archivo del usuario y el del sistema cuando nadie
// dijo cuál: manda el que exista, y el del usuario primero. Así una
// instalación vieja —las que se hacían con sudo— sigue andando sin tocar
// nada, y una nueva no pide permisos.
func resolve(user, sys string, exists func(string) bool) string {
	switch {
	case exists(user):
		return user
	case sys != "" && exists(sys):
		return sys
	default:
		return user
	}
}

// Variable para que los tests puedan contar qué archivo existe sin tener que
// escribir en /etc ni en /Library.
var fileExists = func(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// PathFor es la ruta de un modo puntual, sin adivinar. EnvPath sigue
// mandando: el que prueba en su máquina apunta a una sola ruta y le vale
// para los dos modos.
func PathFor(system bool) (string, error) {
	if p := os.Getenv(EnvPath); p != "" {
		return p, nil
	}
	if system {
		return SystemPath()
	}
	return UserPath()
}

// WritePath es adónde tiene que escribir `vincular`. Casi siempre es la misma
// que Path(); la excepción es la que importa: una computadora que quedó
// instalada como servicio del sistema y alguien que la quiere sacar de ahí sin
// ser administrador. Ese archivo no lo puede tocar, y seguir eligiéndolo es un
// callejón sin salida —falla, y el consejo de correrlo con sudo lo deja en el
// mismo lugar—. En ese caso se pasa a la del usuario y avisa que lo hizo.
func WritePath() (path string, fellBack bool, err error) {
	p, err := Path()
	if err != nil {
		return "", false, err
	}
	// Con `--sistema` o con la variable de entorno, el que pidió sabe lo que
	// pidió: se escribe ahí o se falla ahí.
	if system || os.Getenv(EnvPath) != "" {
		return p, false, nil
	}
	sp, err := SystemPath()
	if err != nil || p != sp || writable(p) {
		return p, false, nil
	}
	up, err := UserPath()
	if err != nil {
		return p, false, nil
	}
	return up, true, nil
}

// writable dice si podemos escribir ese archivo. Se abre y se cierra sin
// tocarle nada: mirar los bits de permiso es adivinar —lo que manda es el uid
// efectivo, y en Windows ni siquiera son bits—.
var writable = func(p string) bool {
	f, err := os.OpenFile(p, os.O_WRONLY, 0)
	if err != nil {
		return false
	}
	f.Close()
	return true
}

// UserPath es la configuración del usuario que corre el agente:
//   - Windows: %AppData%\Manducar\impresion.json
//   - Mac:     ~/Library/Application Support/Manducar/impresion.json
//   - Linux:   ~/.config/manducar/impresion.json
func UserPath() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("no encontré la carpeta de configuración de tu usuario: %w", err)
	}
	return filepath.Join(base, brandDir(), "impresion.json"), nil
}

// SystemPath es la de la instalación como servicio del sistema, la que sólo
// escribe un administrador.
func SystemPath() (string, error) {
	switch runtime.GOOS {
	case "windows":
		base := os.Getenv("ProgramData")
		if base == "" {
			base = `C:\ProgramData`
		}
		return filepath.Join(base, "Manducar", "impresion.json"), nil
	case "darwin":
		return "/Library/Application Support/Manducar/impresion.json", nil
	default:
		return "/etc/manducar/impresion.json", nil
	}
}

// brandDir: en Windows y Mac las carpetas de aplicación van con mayúscula; en
// Linux, en minúscula, como /etc/manducar.
func brandDir() string {
	switch runtime.GOOS {
	case "windows", "darwin":
		return "Manducar"
	default:
		return "manducar"
	}
}

// Dir es la carpeta donde viven la configuración, el log y el outbox: los
// tres juntos, así encontrar uno es encontrar los otros.
func Dir() (string, error) {
	p, err := Path()
	if err != nil {
		return "", err
	}
	return filepath.Dir(p), nil
}

// ModeFor dice qué modo le corresponde a una ruta: la del sistema es
// «system», cualquier otra es «user».
func ModeFor(p string) string {
	if sp, err := SystemPath(); err == nil && p == sp {
		return ModeSystem
	}
	return ModeUser
}

// InstalledMode es cómo quedó instalado el agente en esta computadora. Lo
// dice el archivo; si es uno viejo, sin el campo, lo dice dónde está: los que
// se instalaban con sudo viven en la carpeta del sistema.
func InstalledMode() string {
	p, err := Path()
	if err != nil {
		return ModeUser
	}
	if c, err := LoadFrom(p); err == nil && c.Mode != "" {
		return c.Mode
	}
	return ModeFor(p)
}

func Load() (Config, error) {
	p, err := Path()
	if err != nil {
		return Config{}, err
	}
	return LoadFrom(p)
}

func LoadFrom(p string) (Config, error) {
	raw, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, fmt.Errorf("el agente no está vinculado todavía: corré «manducar-impresion vincular CODIGO --servidor URL» (busqué %s)", p)
	}
	if err != nil {
		return Config{}, err
	}
	var c Config
	if err := json.Unmarshal(raw, &c); err != nil {
		return Config{}, fmt.Errorf("la configuración en %s está rota: %w", p, err)
	}
	return c, nil
}

func Save(c Config) error {
	p, err := Path()
	if err != nil {
		return err
	}
	return SaveTo(p, c)
}

func SaveTo(p string, c Config) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	raw, _ := json.MarshalIndent(c, "", "  ")
	// 0600: adentro está el token.
	if err := os.WriteFile(p, raw, 0o600); err != nil {
		return err
	}
	// WriteFile no toca los permisos de un archivo que ya existía: si quedó
	// abierto de una versión vieja o de un editor, acá se cierra.
	return os.Chmod(p, 0o600)
}
