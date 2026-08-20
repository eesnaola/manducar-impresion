//go:build windows

package printer

// El pulso a una impresora del sistema, del lado Windows. La cola del
// spooler dice poco por sí sola («Ready» eterno), así que se mira lo que sí
// es confiable: la pausa y el «usar sin conexión» del usuario, y —si el
// puerto es un Standard TCP/IP en crudo, la térmica típica— el socket de
// atrás, pulsado igual que una impresora de red.

import (
	"context"
	"fmt"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

func probeSystem(ctx context.Context, name string) Health {
	if name == "" {
		return HealthMissing
	}
	names, err := listSystemCtx(ctx)
	if err != nil {
		return HealthSilent
	}
	if !contains(names, name) {
		return HealthMissing
	}

	info, err := infoDelSpooler(name)
	if err != nil {
		// Está en la lista pero no se deja mirar: existe, y más no sabemos.
		return HealthOK
	}
	if info.pausada || info.sinConexion {
		return HealthStopped
	}
	// El puerto TCP crudo se pulsa de verdad: el spooler dice «listo» aunque
	// la térmica esté apagada, porque no se entera hasta que imprime.
	if dir, ok := puertoTCPCrudo(info.puerto); ok {
		return probeNetwork(ctx, dir)
	}
	return HealthOK
}

type spoolerInfo struct {
	puerto      string
	pausada     bool
	sinConexion bool
}

const (
	printerStatusPaused         = 0x00000001
	printerAttributeWorkOffline = 0x00000400
)

type printerInfo5 struct {
	PrinterName              *uint16
	PortName                 *uint16
	Attributes               uint32
	DeviceNotSelectedTimeout uint32
	TransmissionRetryTimeout uint32
}

type printerInfo6 struct {
	Status uint32
}

var (
	winspool        = windows.NewLazySystemDLL("winspool.drv")
	procOpenPrinter = winspool.NewProc("OpenPrinterW")
	procGetPrinter  = winspool.NewProc("GetPrinterW")
	procClosePrint  = winspool.NewProc("ClosePrinter")
)

// infoDelSpooler pregunta por la impresora con GetPrinter: el puerto y los
// atributos (nivel 5) y el estado (nivel 6, donde vive la pausa).
func infoDelSpooler(name string) (spoolerInfo, error) {
	var out spoolerInfo
	n16, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return out, err
	}
	var h windows.Handle
	r, _, e := procOpenPrinter.Call(uintptr(unsafe.Pointer(n16)), uintptr(unsafe.Pointer(&h)), 0)
	if r == 0 {
		return out, fmt.Errorf("OpenPrinter: %v", e)
	}
	defer procClosePrint.Call(uintptr(h))

	buf, err := getPrinter(h, 5)
	if err != nil {
		return out, err
	}
	info5 := (*printerInfo5)(unsafe.Pointer(&buf[0]))
	if info5.PortName != nil {
		out.puerto = windows.UTF16PtrToString(info5.PortName)
	}
	out.sinConexion = info5.Attributes&printerAttributeWorkOffline != 0

	if buf, err = getPrinter(h, 6); err == nil {
		out.pausada = (*printerInfo6)(unsafe.Pointer(&buf[0])).Status&printerStatusPaused != 0
	}
	return out, nil
}

func getPrinter(h windows.Handle, nivel uint32) ([]byte, error) {
	var necesita uint32
	procGetPrinter.Call(uintptr(h), uintptr(nivel), 0, 0, uintptr(unsafe.Pointer(&necesita)))
	if necesita == 0 {
		return nil, fmt.Errorf("GetPrinter(%d): sin tamaño", nivel)
	}
	buf := make([]byte, necesita)
	r, _, e := procGetPrinter.Call(uintptr(h), uintptr(nivel), uintptr(unsafe.Pointer(&buf[0])), uintptr(necesita), uintptr(unsafe.Pointer(&necesita)))
	if r == 0 {
		return nil, fmt.Errorf("GetPrinter(%d): %v", nivel, e)
	}
	return buf, nil
}

// puertoTCPCrudo: si el puerto es un Standard TCP/IP en protocolo RAW, el
// host y el puerto de atrás (su configuración vive en el registro). Un
// puerto de archivo, USB o LPR no es cosa nuestra.
func puertoTCPCrudo(portName string) (string, bool) {
	if portName == "" {
		return "", false
	}
	// Una impresora puede listar varios puertos separados por coma.
	if i := strings.Index(portName, ","); i >= 0 {
		portName = portName[:i]
	}
	k, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`SYSTEM\CurrentControlSet\Control\Print\Monitors\Standard TCP/IP Port\Ports\`+portName,
		registry.QUERY_VALUE)
	if err != nil {
		return "", false
	}
	defer k.Close()
	if proto, _, err := k.GetIntegerValue("Protocol"); err != nil || proto != 1 { // 1 = RAW
		return "", false
	}
	host, _, err := k.GetStringValue("HostName")
	if err != nil || host == "" {
		if host, _, err = k.GetStringValue("IPAddress"); err != nil || host == "" {
			return "", false
		}
	}
	puerto, _, err := k.GetIntegerValue("PortNumber")
	if err != nil || puerto == 0 {
		puerto = 9100
	}
	return fmt.Sprintf("%s:%d", host, puerto), true
}
