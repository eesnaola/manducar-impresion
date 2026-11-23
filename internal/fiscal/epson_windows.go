//go:build windows

package fiscal

import (
	"debug/pe"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// EnvDLLEpson: para decirle al agente dónde está la librería, si no está en
// ninguno de los lugares donde la busca.
const EnvDLLEpson = "MANDUCAR_EPSON_DLL"

// dllDeEpson es la librería de verdad.
type dllDeEpson struct{ dll *windows.DLL }

func nuevoEpson(c Controlador) (Driver, error) {
	dll, _, err := cargarEpson()
	if err != nil {
		return nil, err
	}
	return &epson{c: c, lib: &dllDeEpson{dll}}, nil
}

// epsonCargada guarda la librería una vez cargada: se pide en cada latido
// (para decir si está) y en cada operación, y es siempre la misma.
var epsonCargada struct {
	sync.Mutex
	dll  *windows.DLL
	ruta string
}

// cargarEpson busca la librería y la carga. Devuelve también de dónde salió.
//
// La librería que publica Epson no viene firmada, así que no hay firma que
// comprobar: la confianza la da el lugar (ver candidatasEpsonEn). Y se carga
// pidiéndole a Windows que lo que ella necesite lo busque sólo en su propia
// carpeta y en System32, no en la carpeta de trabajo ni en el PATH.
func cargarEpson() (*windows.DLL, string, error) {
	epsonCargada.Lock()
	defer epsonCargada.Unlock()
	if epsonCargada.dll != nil {
		return epsonCargada.dll, epsonCargada.ruta, nil
	}
	for _, ruta := range candidatasEpson() {
		if !es64Bits(ruta) {
			continue
		}
		h, err := windows.LoadLibraryEx(ruta, 0, windows.LOAD_LIBRARY_SEARCH_DLL_LOAD_DIR|windows.LOAD_LIBRARY_SEARCH_SYSTEM32)
		if err != nil {
			continue
		}
		epsonCargada.dll, epsonCargada.ruta = &windows.DLL{Name: ruta, Handle: h}, ruta
		return epsonCargada.dll, ruta, nil
	}
	return nil, "", fmt.Errorf("%w: no está la librería de Epson (%s). Instalá «Epson Fiscal Interface», de la página de soporte de Epson para la TM-T900FA", ErrNoDisponible, dllEpson)
}

// candidatasEpson: la indicada a mano, la que esté al lado del agente, y las
// de las carpetas de programas instalados. Esas carpetas se le piden al
// sistema y no a las variables de entorno, que las cambia cualquiera que
// lance el proceso.
func candidatasEpson() []string {
	var delAgente string
	if exe, err := os.Executable(); err == nil {
		delAgente = filepath.Dir(exe)
	}
	var programas []string
	for _, carpeta := range []*windows.KNOWNFOLDERID{windows.FOLDERID_ProgramFiles, windows.FOLDERID_ProgramFilesX86} {
		if ruta, err := windows.KnownFolderPath(carpeta, 0); err == nil && ruta != "" {
			programas = append(programas, ruta)
		}
	}
	return candidatasEpsonEn(os.Getenv(EnvDLLEpson), delAgente, programas)
}

// es64Bits: el agente es de 64 bits y no puede cargar la librería de 32, que
// viene en el mismo paquete con el mismo nombre.
func es64Bits(ruta string) bool {
	f, err := pe.Open(ruta)
	if err != nil {
		return false
	}
	defer f.Close()
	return f.Machine == pe.IMAGE_FILE_MACHINE_AMD64
}

// largoDeSalida es el tamaño del texto que se le da a la librería para que
// conteste.
const largoDeSalida = 512

// invocar llama a una función de la librería. Los args son int o string, como
// en la especificación; extras son los punteros de salida, que van al final.
// Devuelve el código de retorno: 0 es «sin error».
func (d *dllDeEpson) invocar(nombre string, args []any, extras ...uintptr) (int32, error) {
	proc, err := d.dll.FindProc(nombre)
	if err != nil {
		return 0, fmt.Errorf("la librería de Epson no tiene «%s»: ¿es una versión vieja?", nombre)
	}
	crudos := make([]uintptr, 0, len(args)+len(extras))
	var vivos [][]byte // los textos tienen que seguir en memoria hasta que vuelva la llamada
	for _, a := range args {
		switch v := a.(type) {
		case int:
			// Un entero de 32 bits con signo (las tasas a medida son negativas).
			crudos = append(crudos, uintptr(uint32(int32(v))))
		case string:
			texto := append(aANSI(v), 0)
			vivos = append(vivos, texto)
			crudos = append(crudos, uintptr(unsafe.Pointer(&texto[0])))
		default:
			return 0, fmt.Errorf("%s: argumento de un tipo que la librería no recibe (%T)", nombre, a)
		}
	}
	codigo, _, _ := proc.Call(append(crudos, extras...)...)
	runtime.KeepAlive(vivos)
	return int32(codigo), nil
}

func (d *dllDeEpson) resultado(nombre string, codigo int32, err error) error {
	if err != nil {
		return err
	}
	if codigo == 0 {
		return nil
	}
	return fmt.Errorf("%s: %s", nombre, d.descripcion(codigo))
}

func (d *dllDeEpson) llamar(nombre string, args ...any) error {
	codigo, err := d.invocar(nombre, args)
	return d.resultado(nombre, codigo, err)
}

func (d *dllDeEpson) texto(nombre string, args ...any) (string, error) {
	salida := make([]byte, largoDeSalida)
	codigo, err := d.invocar(nombre, args, uintptr(unsafe.Pointer(&salida[0])), uintptr(len(salida)))
	runtime.KeepAlive(salida)
	if err := d.resultado(nombre, codigo, err); err != nil {
		return "", err
	}
	return deANSI(salida), nil
}

func (d *dllDeEpson) entero(nombre string, args ...any) (int, error) {
	var salida int32
	codigo, err := d.invocar(nombre, args, uintptr(unsafe.Pointer(&salida)))
	if err := d.resultado(nombre, codigo, err); err != nil {
		return 0, err
	}
	return int(salida), nil
}

func (d *dllDeEpson) version(nombre string) (string, int, int, error) {
	salida := make([]byte, largoDeSalida)
	var mayor, menor int32
	codigo, err := d.invocar(nombre, nil, uintptr(unsafe.Pointer(&salida[0])), uintptr(len(salida)),
		uintptr(unsafe.Pointer(&mayor)), uintptr(unsafe.Pointer(&menor)))
	runtime.KeepAlive(salida)
	if err := d.resultado(nombre, codigo, err); err != nil {
		return "", 0, 0, err
	}
	return deANSI(salida), int(mayor), int(menor), nil
}

// descripcion le pide a la librería el texto de un código de error.
func (d *dllDeEpson) descripcion(codigo int32) string {
	if proc, err := d.dll.FindProc("ConsultarDescripcionDeError"); err == nil {
		salida := make([]byte, largoDeSalida)
		r, _, _ := proc.Call(uintptr(uint32(codigo)), uintptr(unsafe.Pointer(&salida[0])), uintptr(len(salida)))
		runtime.KeepAlive(salida)
		if texto := deANSI(salida); int32(r) == 0 && texto != "" {
			return fmt.Sprintf("%s (0x%08X)", texto, uint32(codigo))
		}
	}
	return fmt.Sprintf("error 0x%08X de la impresora fiscal", uint32(codigo))
}

// detectarEpson: ¿está la librería?, ¿qué versión?, ¿por qué puertos se puede
// dar de alta un equipo? No le habla a ninguna impresora.
func detectarEpson() LibreriaEpson {
	dll, _, err := cargarEpson()
	if err != nil {
		return LibreriaEpson{Motivo: "En esa computadora no está la librería de Epson. Instalá «Epson Fiscal Interface», que se baja de la página de soporte de Epson para la TM-T900FA. Si quedó fuera de Archivos de programa, copiá EpsonFiscalInterface.dll (la de 64 bits) al lado de manducar-impresion.exe."}
	}
	lib := LibreriaEpson{Disponible: true, Puertos: append([]string{"usb"}, puertosSerie()...)}
	if _, mayor, menor, err := (&dllDeEpson{dll}).version("ConsultarVersionDll"); err == nil {
		lib.Version = fmt.Sprintf("%d.%d", mayor, menor)
	}
	return lib
}

// puertosSerie: los COM que Windows tiene dados de alta, incluidos los de un
// adaptador USB a serie.
func puertosSerie() []string {
	clave, err := registry.OpenKey(registry.LOCAL_MACHINE, `HARDWARE\DEVICEMAP\SERIALCOMM`, registry.QUERY_VALUE)
	if err != nil {
		return nil
	}
	defer clave.Close()
	nombres, err := clave.ReadValueNames(-1)
	if err != nil {
		return nil
	}
	var puertos []string
	for _, n := range nombres {
		if valor, _, err := clave.GetStringValue(n); err == nil && valor != "" {
			puertos = append(puertos, strings.ToUpper(valor))
		}
	}
	sort.Strings(puertos)
	return puertos
}
