package main

// El lado Windows de los cuadros, en texto: un .vbs de unos pocos renglones
// que muestra el cuadro y deja la respuesta en un archivo —wscript no tiene
// salida estándar donde escribirla—. Las funciones de acá son puras a
// propósito —arman texto y nada más—, así se prueban en cualquier sistema; lo
// que escribe los temporales y lanza wscript vive en dialogos_windows.go.

import (
	"encoding/binary"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/eesnaola/manducar-impresion/internal/svc"
)

// Las constantes de MsgBox, con los nombres que tienen en VBScript.
const (
	vbCritical    = 16 // la cruz roja: algo salió mal
	vbQuestion    = 32 // el signo de pregunta, para el sí o no
	vbInformation = 64 // la «i», para contar cómo quedó
	vbYesNo       = 4  // los dos botones
	vbYes         = 6  // lo que devuelve MsgBox si tocaron «Sí»
)

// vbsPreguntar arma el cuadro que pide un dato. InputBox devuelve la cadena
// vacía tanto si cancelaron como si aceptaron sin escribir: no hay forma de
// distinguirlos, así que los dos son «no contestó».
func vbsPreguntar(titulo, pregunta string) string {
	return vbsConRespuesta(
		"r = InputBox("+vbsTexto(pregunta)+", "+vbsTexto(titulo)+")",
		"f.Write r",
	)
}

// vbsConfirmar es el sí o no. MsgBox devuelve un número —6 es «Sí»—, y ese
// número es lo que queda en el archivo.
func vbsConfirmar(titulo, texto string) string {
	return vbsConRespuesta(
		"r = MsgBox("+vbsTexto(texto)+", "+strconv.Itoa(vbYesNo|vbQuestion)+", "+vbsTexto(titulo)+")",
		"f.Write CStr(r)",
	)
}

// vbsMensaje es el cuadro que sólo cuenta algo: no hay nada que leer después,
// así que tampoco hace falta el archivo.
func vbsMensaje(titulo, texto string, icono int) string {
	return vbsCuerpo("MsgBox " + vbsTexto(texto) + ", " + strconv.Itoa(icono) + ", " + vbsTexto(titulo))
}

// vbsConRespuesta envuelve el cuadro con lo que hace falta para dejar la
// respuesta en el archivo que le pasan por argumento.
func vbsConRespuesta(cuadro, escribir string) string {
	return vbsCuerpo(
		// El archivo viene por argumento y no clavado adentro del script: así
		// el texto no depende de en qué carpeta de temporales cayó.
		"salida = WScript.Arguments(0)",
		cuadro,
		`Set fso = CreateObject("Scripting.FileSystemObject")`,
		// El primer True es «pisá lo que haya» y el segundo es Unicode: la
		// respuesta vuelve con sus acentos y no con lo que entienda la
		// codificación vieja de esa computadora.
		"Set f = fso.CreateTextFile(salida, True, True)",
		escribir,
		"f.Close",
	)
}

func vbsCuerpo(lineas ...string) string {
	todo := append([]string{
		"' Un cuadro de diálogo del agente de impresión de Manducar.",
		"' Lo escribe el programa cada vez que tiene algo que preguntar, y lo borra al cerrarlo.",
	}, lineas...)
	// CRLF y un renglón al final: es un archivo de Windows y lo puede llegar a
	// abrir el Bloc de notas.
	return strings.Join(append(todo, ""), "\r\n")
}

// vbsTexto arma la cadena de VBScript de un texto que puede tener varios
// renglones: el salto de línea no se puede escribir adentro de un literal, va
// como vbCrLf entre dos. El escapado de la comilla es el mismo que usa el
// lanzador del arranque automático.
func vbsTexto(s string) string {
	partes := strings.Split(s, "\n")
	for i, p := range partes {
		partes[i] = svc.VBSString(strings.TrimRight(p, "\r"))
	}
	return strings.Join(partes, " & vbCrLf & ")
}

// utf16LE deja el script como lo espera el Windows Script Host cuando no es
// ASCII: UTF-16 con su BOM adelante. Sin el BOM, wscript lo lee con la
// codificación vieja de la computadora y «Pegá el código» le aparece roto a la
// persona.
func utf16LE(s string) []byte {
	u := utf16.Encode([]rune(s))
	b := make([]byte, 0, 2*len(u)+2)
	b = binary.LittleEndian.AppendUint16(b, 0xFEFF)
	for _, r := range u {
		b = binary.LittleEndian.AppendUint16(b, r)
	}
	return b
}

// textoDelArchivo lee la respuesta que dejó el .vbs. La escribimos en UTF-16,
// que es lo que sale con el BOM adelante; se contemplan las otras dos por si
// alguna computadora contesta distinto, que sale gratis. Los saltos de línea y
// los espacios de las puntas no son parte de lo que escribió la persona.
func textoDelArchivo(b []byte) string {
	switch {
	case len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE:
		return strings.TrimSpace(deUTF16(b[2:], binary.LittleEndian))
	case len(b) >= 2 && b[0] == 0xFE && b[1] == 0xFF:
		return strings.TrimSpace(deUTF16(b[2:], binary.BigEndian))
	case len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF:
		return strings.TrimSpace(string(b[3:]))
	}
	return strings.TrimSpace(string(b))
}

func deUTF16(b []byte, orden binary.ByteOrder) string {
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		u = append(u, orden.Uint16(b[i:]))
	}
	return string(utf16.Decode(u))
}
