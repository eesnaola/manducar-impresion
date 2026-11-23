//go:build !windows

package fiscal

// usbDelSistema: fuera de Windows el agente no maneja las Epson, así que no
// hay para qué mirar qué hay enchufado.
func usbDelSistema() []string { return nil }
