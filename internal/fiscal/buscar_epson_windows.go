//go:build windows

package fiscal

import "golang.org/x/sys/windows"

// topeDeUSB: de ahí para arriba no es la lista de una computadora de local.
const topeDeUSB = 2000

// usbDelSistema lista los dispositivos USB que están enchufados ahora, con el
// identificador que les da Windows (`USB\VID_04B8&PID_0202\SERIE`). Es la
// lista del Administrador de dispositivos: no abre ni le habla a ninguno.
func usbDelSistema() (ids []string) {
	// Esto no se pudo probar en un Windows de verdad. Si tropieza, que deje
	// la búsqueda sin sugerencia de Epson y no al agente caído.
	defer func() {
		if recover() != nil {
			ids = nil
		}
	}()
	lista, err := windows.SetupDiGetClassDevsEx(nil, "USB", 0, windows.DIGCF_PRESENT|windows.DIGCF_ALLCLASSES, 0, "")
	if err != nil {
		return nil
	}
	defer lista.Close()
	for i := 0; i < topeDeUSB; i++ {
		dispositivo, err := lista.EnumDeviceInfo(i)
		if err != nil {
			break // no hay más
		}
		if id, err := lista.DeviceInstanceID(dispositivo); err == nil {
			ids = append(ids, id)
		}
	}
	return ids
}
