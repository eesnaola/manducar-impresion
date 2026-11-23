//go:build windows

package fiscal

import (
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// hasarUSBEnEsteSistema: acá el agente encuentra solo a la Hasar enchufada por USB.
const hasarUSBEnEsteSistema = true

// serieWindows es un puerto COM abierto.
type serieWindows struct{ puerto windows.Handle }

// Los bits de DCB.Flags que se usan: datos binarios, y las líneas DTR y RTS
// arriba, que es como un adaptador USB sabe que hay alguien escuchando.
const (
	dcbBinario = 0x0001
	dcbDTR     = 0x0010
	dcbRTS     = 0x1000
)

// abrirSerieDelSistema abre el COM a 8 bits sin paridad y sin control de
// flujo, y de modo que leer espere una décima de segundo y devuelva lo que
// haya. Nunca se ejecutó en un Windows de verdad.
func abrirSerieDelSistema(nombre string, velocidad int) (puertoSerie, error) {
	if velocidad == 0 {
		velocidad = velocidadDeHasar
	}
	// Con el prefijo, que es como se abren los COM de dos cifras.
	ruta, err := windows.UTF16PtrFromString(`\\.\` + nombre)
	if err != nil {
		return nil, err
	}
	puerto, err := windows.CreateFile(ruta, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", nombre, err)
	}
	var modo windows.DCB
	modo.DCBlength = uint32(unsafe.Sizeof(modo))
	if err := windows.GetCommState(puerto, &modo); err != nil {
		_ = windows.CloseHandle(puerto)
		return nil, fmt.Errorf("%s no es un puerto serie: %w", nombre, err)
	}
	modo.BaudRate = uint32(velocidad)
	modo.ByteSize = 8
	modo.Parity = 0   // sin paridad
	modo.StopBits = 0 // un bit de parada
	modo.Flags = dcbBinario | dcbDTR | dcbRTS
	if err := windows.SetCommState(puerto, &modo); err != nil {
		_ = windows.CloseHandle(puerto)
		return nil, fmt.Errorf("no se pudo configurar %s: %w", nombre, err)
	}
	// Leer: lo que haya llegado, o nada pasada una décima de segundo.
	esperas := windows.CommTimeouts{
		ReadIntervalTimeout:        0xFFFFFFFF,
		ReadTotalTimeoutMultiplier: 0xFFFFFFFF,
		ReadTotalTimeoutConstant:   100,
		WriteTotalTimeoutConstant:  2000,
	}
	if err := windows.SetCommTimeouts(puerto, &esperas); err != nil {
		_ = windows.CloseHandle(puerto)
		return nil, fmt.Errorf("no se pudo configurar %s: %w", nombre, err)
	}
	return &serieWindows{puerto: puerto}, nil
}

func (s *serieWindows) Read(b []byte) (int, error) {
	var leidos uint32
	err := windows.ReadFile(s.puerto, b, &leidos, nil)
	return int(leidos), err
}

func (s *serieWindows) Write(b []byte) (int, error) {
	var escritos uint32
	err := windows.WriteFile(s.puerto, b, &escritos, nil)
	return int(escritos), err
}

func (s *serieWindows) Close() error { return windows.CloseHandle(s.puerto) }

// puertoHasarDelSistema busca, entre lo enchufado por USB, el dispositivo con
// los identificadores de la Hasar, y devuelve el COM que Windows le dio (el
// «PortName» de su configuración). No abre ningún dispositivo. Nunca se
// ejecutó en un Windows de verdad.
func puertoHasarDelSistema() (puerto string, err error) {
	defer func() {
		if recover() != nil {
			puerto, err = "", errNoHayHasarUSB
		}
	}()
	lista, err := windows.SetupDiGetClassDevsEx(nil, "USB", 0, windows.DIGCF_PRESENT|windows.DIGCF_ALLCLASSES, 0, "")
	if err != nil {
		return "", errNoHayHasarUSB
	}
	defer lista.Close()
	for i := 0; i < topeDeUSB; i++ {
		dispositivo, err := lista.EnumDeviceInfo(i)
		if err != nil {
			break // no hay más
		}
		id, err := lista.DeviceInstanceID(dispositivo)
		if err != nil || !esIDDeHasarUSB(id) {
			continue
		}
		clave, err := lista.OpenDevRegKey(dispositivo, windows.DICS_FLAG_GLOBAL, 0, windows.DIREG_DEV, windows.KEY_QUERY_VALUE)
		if err != nil {
			continue
		}
		nombre, _, err := registry.Key(clave).GetStringValue("PortName")
		_ = registry.Key(clave).Close()
		if nombre = strings.ToUpper(strings.TrimSpace(nombre)); err == nil && esPuertoCOM.MatchString(nombre) {
			return nombre, nil
		}
	}
	return "", errNoHayHasarUSB
}
