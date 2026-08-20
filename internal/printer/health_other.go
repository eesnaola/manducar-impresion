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
	if st, ok := ippPrinterState(ctx, name); ok {
		if st == "stopped" {
			return HealthStopped
		}
		return HealthOK
	}
	out, err := lpstat(ctx, "-p", name)
	if err != nil {
		return HealthSilent
	}
	if strings.Contains(out, "disabled") {
		return HealthStopped
	}
	return HealthOK
}

// El pedido de ipptool: el estado de UNA impresora.
const ippPrinterStateTest = `{
    OPERATION Get-Printer-Attributes
    GROUP operation-attributes-tag
    ATTR charset attributes-charset utf-8
    ATTR naturalLanguage attributes-natural-language en
    ATTR uri printer-uri $PURI
    DISPLAY printer-state
}
`

// ippPrinterState: «idle», «processing» o «stopped». El false es «no se pudo
// saber por acá»: que decida el fallback.
func ippPrinterState(ctx context.Context, name string) (string, bool) {
	ipp, err := exec.LookPath("ipptool")
	if err != nil {
		return "", false
	}
	f, err := os.CreateTemp("", "manducar-ipp-*.test")
	if err != nil {
		return "", false
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(ippPrinterStateTest); err != nil {
		f.Close()
		return "", false
	}
	f.Close()
	c, cancel := context.WithTimeout(ctx, listTimeout)
	defer cancel()
	cmd := exec.CommandContext(c, ipp, "-tv", "-d", "PURI=ipp://localhost/printers/"+name, "ipp://localhost/", f.Name())
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	const marca = "printer-state (enum) = "
	i := strings.Index(string(out), marca)
	if i < 0 {
		return "", false
	}
	valor := string(out)[i+len(marca):]
	if j := strings.IndexAny(valor, " \n\r\t("); j >= 0 {
		valor = valor[:j]
	}
	return valor, true
}
