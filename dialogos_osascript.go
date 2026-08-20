package main

// El lado Mac de los cuadros, en texto: el AppleScript que corre osascript y
// cómo se lee lo que contesta. Igual que el .vbs de Windows, las funciones de
// acá son puras —arman y parten texto— así se prueban en cualquier sistema; lo
// que lanza el proceso vive en dialogos_darwin.go.

import "strings"

// Los botones, en castellano: son lo que lee la persona.
const (
	botonAceptar  = "Aceptar"
	botonCancelar = "Cancelar"
	botonSi       = "Sí"
	botonNo       = "No"
)

// osaPreguntar es el cuadro con un campo para escribir. El «cancel button» es
// lo que hace que Cancelar —y la tecla Esc— salgan por el camino de la
// cancelación y no como un botón más; sin eso, un botón que no se llame
// «Cancel» en inglés es un botón cualquiera.
func osaPreguntar(titulo, pregunta, icono string) string {
	return osaCuadro(
		osaTexto(pregunta) + ` default answer "" with title ` + osaTexto(titulo) +
			` buttons {` + osaTexto(botonCancelar) + `, ` + osaTexto(botonAceptar) + `}` +
			` cancel button ` + osaTexto(botonCancelar) + ` default button ` + osaTexto(botonAceptar) +
			osaIcono(icono))
}

// osaConfirmar es el sí o no. Acá no hay «cancel button» a propósito: cerrar
// la pregunta sin contestarla no es que no; es que no contestó, y eso se
// decide arriba.
func osaConfirmar(titulo, texto, icono string) string {
	return osaCuadro(
		osaTexto(texto) + ` with title ` + osaTexto(titulo) +
			` buttons {` + osaTexto(botonNo) + `, ` + osaTexto(botonSi) + `}` +
			` default button ` + osaTexto(botonSi) + osaIcono(icono))
}

// osaMensaje es el cuadro que sólo cuenta algo. El ícono es lo único que
// separa una buena noticia de una mala: la placa de Manducar (o «note», si no
// está) es la aplicación, «stop» es la mano roja.
func osaMensaje(titulo, texto, icono string) string {
	return osaCuadro(
		osaTexto(texto) + ` with title ` + osaTexto(titulo) +
			` buttons {` + osaTexto(botonAceptar) + `} default button 1` + osaIcono(icono))
}

// osaIcono: «note»/«stop» son los del sistema; una ruta es un .icns propio
// —la placa de la marca, que viaja adentro del .app— y va como POSIX file.
// Vacío es sin ícono.
func osaIcono(icono string) string {
	switch {
	case icono == "":
		return ""
	case strings.HasPrefix(icono, "/"):
		return ` with icon POSIX file ` + osaTexto(icono)
	default:
		return ` with icon ` + icono
	}
}

// osaCuadro le pone adelante el «display dialog» y el activate. El activate es
// para que la ventana venga al frente: osascript no es una aplicación que la
// persona haya abierto, y sin esto el cuadro puede quedar atrás de todo, con
// alguien esperando a que pase algo.
func osaCuadro(resto string) string {
	return "activate\ndisplay dialog " + resto
}

// osaTexto mete el texto adentro de un literal de AppleScript: la comilla y la
// contrabarra se escapan con contrabarra, y el salto de línea va como \n
// —el literal es de un solo renglón—.
func osaTexto(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\r", "", "\n", `\n`)
	return `"` + r.Replace(s) + `"`
}

// textoDevuelto saca lo que escribió la persona de lo que contesta osascript:
// «button returned:Aceptar, text returned:4286 2135». El texto va último, así
// que es todo lo que sigue a la marca —una coma adentro de lo que escribió no
// nos puede cortar el valor al medio—.
func textoDevuelto(salida string) (string, bool) {
	const marca = "text returned:"
	i := strings.Index(salida, marca)
	if i < 0 {
		return "", false
	}
	return strings.TrimSpace(salida[i+len(marca):]), true
}

// botonDevuelto es cuál de los botones tocaron.
func botonDevuelto(salida string) string {
	const marca = "button returned:"
	i := strings.Index(salida, marca)
	if i < 0 {
		return ""
	}
	resto := salida[i+len(marca):]
	if j := strings.Index(resto, ", "); j >= 0 {
		resto = resto[:j]
	}
	return strings.TrimSpace(resto)
}

// esCancelacion: para AppleScript, que la persona cancele es un error, el
// -128. El número no está traducido; el texto que lo acompaña, según la
// computadora, puede estarlo.
func esCancelacion(salidaDeError string) bool {
	s := strings.ToLower(salidaDeError)
	return strings.Contains(s, "-128") ||
		strings.Contains(s, "user canceled") ||
		strings.Contains(s, "user cancelled")
}

// mensajeDeOsascript deja el error del intérprete como para mostrárselo a
// alguien: sin el «execution error:» de adelante y sin el número de error de
// atrás, que no le dicen nada a nadie.
func mensajeDeOsascript(salidaDeError string) string {
	s := strings.TrimSpace(salidaDeError)
	if i := strings.LastIndex(s, "execution error:"); i >= 0 {
		s = s[i+len("execution error:"):]
	}
	if i := strings.LastIndex(s, " ("); i >= 0 && strings.HasSuffix(s, ")") {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}
