//go:build !windows

package printer

// El pulso a una impresora del sistema, del lado CUPS: lpstat -p dice si está
// habilitada («enabled») o frenada («disabled»); la que no aparece en la
// lista de destinos ya no existe en esta computadora.

import (
	"context"
	"os"
	"os/exec"
	"strings"
)

func probeSystem(ctx context.Context, name string) Health {
	if name == "" {
		return HealthMissing
	}
	names, err := listSystemCtx(ctx)
	if err != nil {
		// Sin CUPS no hay veredicto sobre la impresora: no inventar.
		return HealthSilent
	}
	if !contains(names, name) {
		return HealthMissing
	}
	// La verdad locale-proof la da IPP: el lpstat de macOS contesta en el
	// idioma de la computadora aunque le pongas LC_ALL=C, y buscar
	// «disabled» ahí es el mismo pozo que ya pisamos con los cancelados.
	st, uri, ok := ippPrinterInfo(ctx, name)
	if !ok {
		out, err := lpstat(ctx, "-p", name)
		if err != nil {
			return HealthSilent
		}
		if strings.Contains(out, "disabled") {
			return HealthStopped
		}
		return HealthOK
	}
	if st == "stopped" {
		return HealthStopped
	}
	// CUPS dice «idle» aunque el fierro de atrás esté apagado: no se entera
	// hasta que intenta imprimir. Si la cola apunta a un socket crudo —una
	// térmica—, se pulsa también ese socket, que es lo que a la persona le
	// importa que esté vivo.
	if dir, esSocket := socketHostPort(uri); esSocket {
		return probeNetwork(ctx, dir)
	}
	return HealthOK
}

// socketHostPort saca «host:puerto» de un device-uri socket:// de CUPS
// (socket://192.168.0.50:9100/?waiteof=false). Sin puerto, el 9100 de rigor.
func socketHostPort(uri string) (string, bool) {
	resto, ok := strings.CutPrefix(uri, "socket://")
	if !ok || resto == "" {
		return "", false
	}
	if i := strings.IndexAny(resto, "/?"); i >= 0 {
		resto = resto[:i]
	}
	if resto == "" {
		return "", false
	}
	if !strings.Contains(resto, ":") {
		resto += ":9100"
	}
	return resto, true
}

// El pedido de ipptool: el estado de UNA impresora y a qué aparato apunta.
const ippPrinterStateTest = `{
    OPERATION Get-Printer-Attributes
    GROUP operation-attributes-tag
    ATTR charset attributes-charset utf-8
    ATTR naturalLanguage attributes-natural-language en
    ATTR uri printer-uri $PURI
    DISPLAY printer-state
    DISPLAY device-uri
}
`

// ippPrinterInfo: el estado («idle», «processing», «stopped») y el device-uri
// de la cola. El false es «no se pudo saber por acá»: que decida el fallback.
func ippPrinterInfo(ctx context.Context, name string) (string, string, bool) {
	ipp, err := exec.LookPath("ipptool")
	if err != nil {
		return "", "", false
	}
	f, err := os.CreateTemp("", "manducar-ipp-*.test")
	if err != nil {
		return "", "", false
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(ippPrinterStateTest); err != nil {
		f.Close()
		return "", "", false
	}
	f.Close()
	c, cancel := context.WithTimeout(ctx, listTimeout)
	defer cancel()
	cmd := exec.CommandContext(c, ipp, "-tv", "-d", "PURI=ipp://localhost/printers/"+name, "ipp://localhost/", f.Name())
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	out, err := cmd.Output()
	if err != nil {
		return "", "", false
	}
	estado := valorIPP(string(out), "printer-state (enum) = ")
	if estado == "" {
		return "", "", false
	}
	return estado, valorIPP(string(out), "device-uri (uri) = "), true
}

// valorIPP saca el valor que sigue a una marca en la salida de ipptool.
func valorIPP(salida, marca string) string {
	i := strings.Index(salida, marca)
	if i < 0 {
		return ""
	}
	valor := salida[i+len(marca):]
	if j := strings.IndexAny(valor, " \n\r\t("); j >= 0 {
		valor = valor[:j]
	}
	return valor
}
