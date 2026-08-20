//go:build !windows && !darwin

package main

// En Linux el programa se abre desde una terminal —es lo que dice el tutorial,
// y es lo que hace falta para darle permiso de ejecución—, así que el asistente
// de siempre alcanza. zenity y compañía existen, pero no en todas las máquinas:
// depender de algo que puede no estar es peor que la terminal, que está.

import "errors"

func nuevosCuadros() (dialogs, error) { return nil, errSinCuadros }

func soltarConsola() {}

var errSinCuadros = errors.New("en este sistema no hay cuadros de diálogo: el asistente va por la terminal")
