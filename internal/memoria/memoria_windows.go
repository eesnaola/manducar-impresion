//go:build windows

package memoria

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32                 = windows.NewLazySystemDLL("kernel32.dll")
	procGlobalMemoryStatusEx = kernel32.NewProc("GlobalMemoryStatusEx")
	procGetProcessMemoryInfo = kernel32.NewProc("K32GetProcessMemoryInfo")
)

// memoryStatusEx es MEMORYSTATUSEX. El «page file» de Windows es en realidad
// la memoria comprometible entera: RAM más archivo de paginación.
type memoryStatusEx struct {
	length               uint32
	memoryLoad           uint32
	totalPhys            uint64
	availPhys            uint64
	totalPageFile        uint64
	availPageFile        uint64
	totalVirtual         uint64
	availVirtual         uint64
	availExtendedVirtual uint64
}

// processMemoryCounters es PROCESS_MEMORY_COUNTERS. PeakPagefileUsage es lo
// más que llegó a comprometer el proceso, que es lo que cuenta contra el tope.
type processMemoryCounters struct {
	cb                         uint32
	pageFaultCount             uint32
	peakWorkingSetSize         uintptr
	workingSetSize             uintptr
	quotaPeakPagedPoolUsage    uintptr
	quotaPagedPoolUsage        uintptr
	quotaPeakNonPagedPoolUsage uintptr
	quotaNonPagedPoolUsage     uintptr
	pagefileUsage              uintptr
	peakPagefileUsage          uintptr
}

// Seguimiento guarda una handle propia del proceso: la de Go se cierra cuando
// termina de esperarlo, y con ésta el pico se puede leer después.
type Seguimiento struct{ h windows.Handle }

// Seguir abre el proceso apenas arrancó. Si no se puede, Medir mide sólo la
// computadora.
func Seguir(pid int) *Seguimiento {
	if pid <= 0 {
		return &Seguimiento{}
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_VM_READ, false, uint32(pid))
	if err != nil {
		return &Seguimiento{}
	}
	return &Seguimiento{h: h}
}

// Medir toma la memoria de la computadora en este momento y el pico del
// proceso, y suelta la handle: se mide una sola vez. Son dos llamadas con las
// estructuras en la pila: el que mide es el vigía, y justo puede ser cuando a
// la computadora no le queda memoria para darle.
func (s *Seguimiento) Medir() Medida {
	var m Medida
	st := memoryStatusEx{length: uint32(unsafe.Sizeof(memoryStatusEx{}))}
	if r, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&st))); r != 0 {
		m.ComprometidaMB = enMB(st.totalPageFile - st.availPageFile)
		m.LimiteMB = enMB(st.totalPageFile)
		m.RAMMB = enMB(st.totalPhys)
		m.RAMLibreMB = enMB(st.availPhys)
	}
	if s.h != 0 {
		pmc := processMemoryCounters{cb: uint32(unsafe.Sizeof(processMemoryCounters{}))}
		if r, _, _ := procGetProcessMemoryInfo.Call(uintptr(s.h), uintptr(unsafe.Pointer(&pmc)), uintptr(pmc.cb)); r != 0 {
			m.AgenteMB = enMB(uint64(pmc.peakPagefileUsage))
		}
		windows.CloseHandle(s.h)
		s.h = 0
	}
	return m
}
