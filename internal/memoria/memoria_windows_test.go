//go:build windows

package memoria

import (
	"os"
	"os/exec"
	"runtime"
	"testing"
)

func TestMideLaComputadoraYElProceso(t *testing.T) {
	m := Seguir(os.Getpid()).Medir()

	if m.LimiteMB == 0 || m.ComprometidaMB == 0 || m.ComprometidaMB > m.LimiteMB {
		t.Errorf("comprometida/límite = %d/%d MB", m.ComprometidaMB, m.LimiteMB)
	}
	if m.RAMMB == 0 || m.RAMLibreMB > m.RAMMB {
		t.Errorf("RAM/libre = %d/%d MB", m.RAMMB, m.RAMLibreMB)
	}
	if m.AgenteMB == 0 {
		t.Errorf("el pico del proceso salió en cero: %+v", m)
	}
}

// El vigía mide cuando el hijo ya terminó: el pico tiene que seguir ahí,
// porque Go ya cerró su handle del proceso y la que queda es la de Seguir.
func TestElPicoDeUnHijoQueYaTermino(t *testing.T) {
	if os.Getenv("MEMORIA_HIJO") == "1" {
		b := make([]byte, 300<<20)
		for i := 0; i < len(b); i += 4096 {
			b[i] = 1
		}
		runtime.KeepAlive(b)
		os.Exit(2)
	}
	c := exec.Command(os.Args[0], "-test.run=^TestElPicoDeUnHijoQueYaTermino$")
	c.Env = append(os.Environ(), "MEMORIA_HIJO=1")
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	s := Seguir(c.Process.Pid)
	if err := c.Wait(); err == nil {
		t.Fatal("el hijo tenía que salir con 2")
	}

	if m := s.Medir(); m.AgenteMB < 300 {
		t.Errorf("pico del hijo = %d MB, quiero 300 o más (%+v)", m.AgenteMB, m)
	}
}

// Sin handle (el proceso no existe) igual se mide la computadora.
func TestSinProcesoSeMideIgualLaComputadora(t *testing.T) {
	m := Seguir(-1).Medir()
	if m.AgenteMB != 0 || m.LimiteMB == 0 {
		t.Errorf("medida = %+v", m)
	}
}
