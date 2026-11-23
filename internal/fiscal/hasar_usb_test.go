package fiscal

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLaHasarSeReconocePorSusIdentificadoresUSB(t *testing.T) {
	for id, es := range map[string]bool{
		`USB\VID_03EB&PID_2310\6&2A3B4C5D&0&1`:       true,
		`usb\vid_03eb&pid_2310\0001`:                 true,
		`USB\VID_03EB&PID_2310&MI_00\7&1A2B3C4D&0&0`: true, // el puerto, cuando el dispositivo tiene varias funciones
		`USB\VID_03EB&PID_2311\0001`:                 false,
		`USB\VID_04B8&PID_0202\X5PF012345`:           false, // una Epson de tickets
		`USB\VID_0403&PID_6001\A9XYZ`:                false, // un adaptador USB a serie cualquiera
		`ACPI\PNP0501\1`:                             false,
		``:                                           false,
	} {
		if esIDDeHasarUSB(id) != es {
			t.Errorf("esIDDeHasarUSB(%q) = %v", id, !es)
		}
	}
}

// En Linux el puerto de la Hasar se encuentra mirando de qué dispositivo USB
// sale cada puerto serie. El árbol es el que arma el sistema en /sys.
func TestEnLinuxElPuertoDeLaHasarSaleDeLosIdentificadoresDelDispositivo(t *testing.T) {
	raiz := t.TempDir()
	// puerto → interfaz USB → dispositivo con sus identificadores
	enchufar := func(puerto, fabricante, producto string) {
		dispositivo := filepath.Join(raiz, "devices", puerto+"-usb")
		interfaz := filepath.Join(dispositivo, "1-1:1.0")
		if err := os.MkdirAll(interfaz, 0o755); err != nil {
			t.Fatal(err)
		}
		_ = os.WriteFile(filepath.Join(dispositivo, "idVendor"), []byte(fabricante+"\n"), 0o644)
		_ = os.WriteFile(filepath.Join(dispositivo, "idProduct"), []byte(producto+"\n"), 0o644)
		tty := filepath.Join(raiz, "class", "tty", puerto)
		if err := os.MkdirAll(tty, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(interfaz, filepath.Join(tty, "device")); err != nil {
			t.Skipf("acá no se pueden crear enlaces, que es como Linux arma ese árbol: %v", err)
		}
	}
	clase := filepath.Join(raiz, "class", "tty")
	_ = os.MkdirAll(filepath.Join(clase, "ttyS0"), 0o755) // un puerto de la placa, sin USB atrás

	if _, err := puertoHasarEnSysfs(clase); err == nil {
		t.Error("sin ninguna enchufada tenía que decirlo")
	}

	enchufar("ttyUSB0", "0403", "6001") // un adaptador USB a serie, con una balanza
	enchufar("ttyACM0", "04b8", "0202") // otra cosa
	if _, err := puertoHasarEnSysfs(clase); err == nil {
		t.Error("ninguno de esos es una Hasar")
	}

	enchufar("ttyACM1", "03eb", "2310")
	puerto, err := puertoHasarEnSysfs(clase)
	if err != nil || puerto != "/dev/ttyACM1" {
		t.Errorf("puerto = %q, %v; se esperaba /dev/ttyACM1", puerto, err)
	}
}

func TestBuscarSugiereLaHasarEnchufadaPorUSBConSusDatos(t *testing.T) {
	equipo := nuevaHasarPorPuerto(t)
	equipo.enchufar("COM7")
	buscarDePrueba(t, 9, nil, nil, "")
	puertoDeHasarUSB = func() (string, error) { return "COM7", nil }

	encontrados := Buscar(context.Background())

	if len(encontrados) != 1 {
		t.Fatalf("hay una enchufada por USB: %+v", encontrados)
	}
	e := encontrados[0]
	if e.Marca != MarcaHasar || e.Conexion != "usb" {
		t.Errorf("marca y conexión = %q, %q; se esperaba hasar por usb", e.Marca, e.Conexion)
	}
	if e.Equipo == nil || e.Equipo.Modelo != "Hasar SMH/PT-250F V: 02.00" || e.Equipo.PuntoDeVenta != 99998 || e.Equipo.CUIT != "30123456781" {
		t.Errorf("por USB se le puede preguntar quién es: %+v", e.Equipo)
	}
	// Preguntar quién es no emite ni cambia nada.
	for _, comando := range strings.Split(equipo.secuencia(), ",") {
		if comando != "ConsultarVersion" && comando != "ConsultarDatosInicializacion" {
			t.Errorf("buscar le mandó «%s» al equipo", comando)
		}
	}
	if _, abiertos, cerrados := equipo.vistos(); abiertos != cerrados {
		t.Errorf("buscar dejó el puerto tomado: abierto %d, cerrado %d", abiertos, cerrados)
	}
}

func TestLaHasarEnchufadaQueNoContestaApareceConElAviso(t *testing.T) {
	equipo := nuevaHasarPorPuerto(t)
	equipo.enchufar("COM7")
	buscarDePrueba(t, 9, nil, nil, "")
	puertoDeHasarUSB = func() (string, error) { return "COM7", nil }
	equipo.hasarFalsa.mu.Lock()
	equipo.apagada = true
	equipo.hasarFalsa.mu.Unlock()

	encontrados := Buscar(context.Background())

	if len(encontrados) != 1 || encontrados[0].Conexion != "usb" || encontrados[0].Equipo != nil {
		t.Fatalf("está enchufada: tiene que aparecer, sin sus datos. %+v", encontrados)
	}
	if !strings.Contains(encontrados[0].Aviso, "no contest") {
		t.Errorf("el aviso tiene que decir que no contestó: %q", encontrados[0].Aviso)
	}
}

func TestSinNingunaHasarPorUSBNoSeSugiereNada(t *testing.T) {
	buscarDePrueba(t, 9, nil, nil, "")

	if encontrados := Buscar(context.Background()); len(encontrados) != 0 {
		t.Errorf("no hay nada enchufado: %+v", encontrados)
	}
}

func TestDetectarDiceSiSePuedeDarDeAltaUnaHasarPorUSB(t *testing.T) {
	anterior := puertoHasarDePrueba
	defer func() { puertoHasarDePrueba = anterior }()

	puertoHasarDePrueba = ""
	if Detectar().HasarUSB != hasarUSBEnEsteSistema {
		t.Errorf("sin puerto de prueba, lo decide el sistema: %v", Detectar().HasarUSB)
	}
	puertoHasarDePrueba = "/dev/ttys004"
	if !Detectar().HasarUSB {
		t.Error("con un puerto de prueba puesto al compilar, se puede")
	}
}
