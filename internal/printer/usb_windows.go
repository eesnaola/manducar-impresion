//go:build windows

package printer

// El pulso por USB de una impresora del sistema enchufada ahí (USB001). El
// spooler dice «Lista» con la tapa abierta o la impresora apagada, así que
// se le pregunta a ella: se abre el aparato —la interfaz de impresión USB de
// Windows, la misma por la que escribe el spooler— y se le hacen las tres
// preguntas de DLE EOT, como a una de red.
//
// Probado a mano con la POS-58 de El Caracol (12/10/2026, script de
// diagnóstico): con la tapa cerrada contesta 16 / 12 / 12; con la tapa
// abierta no acepta ni un byte, y lo encolado sale al cerrarla. Eso último es
// «No responde».

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// La interfaz de las impresoras USB (GUID_DEVINTERFACE_USBPRINT).
const interfazUSBPrint = `{28d78fad-5a12-11d1-ae5b-0000f803a8c2}`

const (
	// Escribir tres bytes es instantáneo; la que no los toma en un segundo
	// no los va a tomar (la POS-58 con la tapa abierta).
	usbEscritura = time.Second
	// Lo que quedó de una pregunta vieja: con la tapa abierta la respuesta
	// llega recién al cerrarla, y se leería como la de la pregunta nueva.
	usbDrenaje = 100 * time.Millisecond
	// El tamaño de un paquete USB: leer de a menos no lo acepta cualquier
	// driver.
	usbLectura = 64
)

// aparatosUSB: las rutas de los aparatos que el registro anota en ese
// puerto. Puede haber más de una —quedan entradas de una impresora que se
// desenchufó—, así que se prueban todas. El número de puerto vive en
// «Enum\<instancia>\Device Parameters» como «Base Name» + «Port Number»
// (USB + 1 = USB001): así lo leyó el diagnóstico en El Caracol.
func aparatosUSB(puerto string) []string {
	if i := strings.Index(puerto, ","); i >= 0 {
		puerto = puerto[:i]
	}
	if !strings.HasPrefix(strings.ToUpper(puerto), "USB") {
		return nil
	}
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\DeviceClasses\`+interfazUSBPrint, registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return nil
	}
	defer k.Close()
	nombres, err := k.ReadSubKeyNames(-1)
	if err != nil {
		return nil
	}
	var rutas []string
	for _, n := range nombres {
		// «##?#USB#VID_0416&PID_5011#…#{28d78fad-…}» es la ruta del aparato,
		// con «\\?\» escrito así.
		if !strings.HasPrefix(n, "##?#") {
			continue
		}
		inst := valorTexto(k, n, "DeviceInstance")
		if inst == "" {
			continue
		}
		dp, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Enum\`+inst+`\Device Parameters`, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		base, _, _ := dp.GetStringValue("Base Name")
		numero, ok := valorNumero(dp, "Port Number")
		dp.Close()
		if ok && coincidePuerto(base, numero, puerto) {
			rutas = append(rutas, `\\?\`+n[4:])
		}
	}
	return rutas
}

func valorTexto(padre registry.Key, sub, nombre string) string {
	k, err := registry.OpenKey(padre, sub, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	v, _, _ := k.GetStringValue(nombre)
	return v
}

// valorNumero: «Port Number» es un DWORD; si algún driver lo guardó como
// texto, también vale.
func valorNumero(k registry.Key, nombre string) (uint64, bool) {
	if v, _, err := k.GetIntegerValue(nombre); err == nil {
		return v, true
	}
	if s, _, err := k.GetStringValue(nombre); err == nil {
		if v, err := strconv.ParseUint(strings.TrimSpace(s), 10, 32); err == nil {
			return v, true
		}
	}
	return 0, false
}

// probeUSB abre el aparato y le hace las preguntas. Apagada o desenchufada
// —no se deja abrir— o sin aceptar la pregunta —la tapa abierta de la
// POS-58—, no responde. Si la tiene abierta otro programa que está
// imprimiendo, está viva: no se la marca caída por estar ocupada.
func probeUSB(ctx context.Context, rutas []string) Health {
	if ctx.Err() != nil {
		return HealthOK
	}
	h, ocupada, ok := abrirUSB(rutas)
	if !ok {
		if ocupada {
			return HealthOK
		}
		return HealthUnreachable
	}
	defer windows.CloseHandle(h)

	usbLeer(h, usbDrenaje)

	noAcepta := false
	salud := diagnosticar(func(n byte) (byte, bool) {
		if !usbEscribir(h, []byte{0x10, 0x04, n}) {
			noAcepta = true
			return 0, false
		}
		r := usbLeer(h, probeReadTimeout)
		if len(r) == 0 {
			return 0, false
		}
		return r[len(r)-1], true
	})
	if noAcepta {
		return HealthUnreachable
	}
	return salud
}

// abrirUSB prueba las rutas en orden. ocupada dice que alguna existe pero la
// tiene otro (el spooler imprimiendo lo de otro programa).
func abrirUSB(rutas []string) (h windows.Handle, ocupada bool, ok bool) {
	for _, ruta := range rutas {
		p, err := windows.UTF16PtrFromString(ruta)
		if err != nil {
			continue
		}
		h, err = windows.CreateFile(p, windows.GENERIC_READ|windows.GENERIC_WRITE,
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING,
			windows.FILE_FLAG_OVERLAPPED, 0)
		if err == nil {
			return h, false, true
		}
		if errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_BUSY) {
			ocupada = true
		}
	}
	return 0, ocupada, false
}

func usbEscribir(h windows.Handle, b []byte) bool {
	n, ok := usbOp(h, true, b, usbEscritura)
	return ok && n == len(b)
}

// usbLeer espera hasta plazo a que la impresora diga algo. Hay drivers que
// completan la lectura con cero bytes cuando no hay nada: se vuelve a leer.
func usbLeer(h windows.Handle, plazo time.Duration) []byte {
	vence := time.Now().Add(plazo)
	for {
		falta := time.Until(vence)
		if falta <= 0 {
			return nil
		}
		buf := make([]byte, usbLectura)
		n, ok := usbOp(h, false, buf, falta)
		if !ok {
			return nil
		}
		if n > 0 {
			return buf[:n]
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// abandonadas guarda lo de una operación que el driver no soltó al
// cancelarla: todavía puede escribir ahí, y liberarlo sería dejarle memoria
// ajena. Es un caso raro y chico; se pierde a propósito.
var (
	abandonadasMu sync.Mutex
	abandonadas   []any
)

// usbOp escribe o lee con plazo (E/S superpuesta: sin plazo, una lectura a
// una impresora que no contesta no vuelve nunca). Devuelve cuántos bytes
// pasaron y si terminó a tiempo.
func usbOp(h windows.Handle, escribir bool, buf []byte, plazo time.Duration) (int, bool) {
	ev, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return 0, false
	}
	ov := &windows.Overlapped{HEvent: ev}
	if escribir {
		err = windows.WriteFile(h, buf, nil, ov)
	} else {
		err = windows.ReadFile(h, buf, nil, ov)
	}
	if err != nil && !errors.Is(err, windows.ERROR_IO_PENDING) {
		windows.CloseHandle(ev)
		return 0, false
	}
	ms := uint32(plazo / time.Millisecond)
	if r, _ := windows.WaitForSingleObject(ev, ms); r != windows.WAIT_OBJECT_0 {
		_ = windows.CancelIoEx(h, ov)
		if r, _ := windows.WaitForSingleObject(ev, 2000); r != windows.WAIT_OBJECT_0 {
			abandonadasMu.Lock()
			abandonadas = append(abandonadas, ov, buf)
			abandonadasMu.Unlock()
			return 0, false
		}
		windows.CloseHandle(ev)
		return 0, false
	}
	var n uint32
	err = windows.GetOverlappedResult(h, ov, &n, false)
	windows.CloseHandle(ev)
	if err != nil {
		return 0, false
	}
	return int(n), true
}
