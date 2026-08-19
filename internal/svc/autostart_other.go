//go:build !windows

package svc

import (
	"errors"
	"os"
)

// Mac y Linux sí tienen servicios de usuario —LaunchAgent y unidad `--user`—,
// así que el rebusque de la clave Run es sólo de Windows.
const runKeyAutostart = false

func autostartInstall(string, string) error { return errNoAutostart }
func autostartUninstall() error             { return errNoAutostart }
func autostartRestart() error               { return errNoAutostart }
func autostartInstalled() (bool, error)     { return false, errNoAutostart }

// checkPrivileges se planta antes de empezar: instalar el servicio del sistema
// a medias es peor que no empezar, y la instalación de usuario hecha con sudo
// es peor todavía —anda, no se queja, y queda en la carpeta de root, donde no
// la ve nadie—.
func checkPrivileges(system bool) error {
	root := os.Geteuid() == 0
	switch {
	case system && !root:
		return errors.New("«--sistema» es el servicio del sistema, el de toda la computadora, y eso lo toca root: repetí el comando con sudo adelante. Sin --sistema no hace falta ningún permiso")
	case !system && root:
		return errors.New("no lo corras con sudo: sin --sistema el agente se instala en tu carpeta de usuario, y con sudo terminaría en la de root —arrancaría con root y no con vos, y la configuración quedaría donde no la ve nadie—. Repetí el comando sin sudo. Si lo que querés es el servicio del sistema, para toda la computadora, agregale --sistema")
	}
	return nil
}
