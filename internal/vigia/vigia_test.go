package vigia

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestEspera(t *testing.T) {
	quiero := map[int]time.Duration{0: 0, 1: time.Second, 2: 2 * time.Second, 3: 5 * time.Second, 4: 15 * time.Second, 5: time.Minute, 9: time.Minute}
	for fallos, d := range quiero {
		if got := Espera(fallos); got != d {
			t.Errorf("Espera(%d) = %s, quiero %s", fallos, got, d)
		}
	}
}

// guion devuelve un lanzador que sale con esos códigos, uno por vuelta.
func guion(codigos ...int) (Lanzador, *int) {
	vueltas := 0
	return func(context.Context) (int, error) {
		c := codigos[vueltas]
		vueltas++
		return c, nil
	}, &vueltas
}

func TestLevantaAlQueSeCaeConEsperaCreciente(t *testing.T) {
	lanzar, vueltas := guion(2, 2, 0)
	var anotados []int
	var esperas []time.Duration

	err := Vigilar(context.Background(), lanzar,
		func(c int, _ time.Time) { anotados = append(anotados, c) },
		func(_ context.Context, d time.Duration) bool { esperas = append(esperas, d); return true })

	if err != nil || *vueltas != 3 {
		t.Fatalf("err %v, vueltas %d", err, *vueltas)
	}
	if !reflect.DeepEqual(anotados, []int{2, 2}) {
		t.Errorf("anotados = %v", anotados)
	}
	if !reflect.DeepEqual(esperas, []time.Duration{time.Second, 2 * time.Second}) {
		t.Errorf("esperas = %v", esperas)
	}
}

// La caída se anota recién cuando el vigía la va a levantar, después de la
// espera, y con la hora en que terminó. Si al hijo lo mató un «taskkill /F»
// que viene por el vigía también, el vigía muere en la espera y no queda una
// caída de mentira.
func TestLaCaidaSeAnotaDespuesDeLaEsperaConLaHoraDeLaSalida(t *testing.T) {
	lanzar, _ := guion(2, 2, 0)
	var pasos []string
	var horas []time.Time
	var durmio []time.Time

	_ = Vigilar(context.Background(), lanzar,
		func(c int, cuando time.Time) {
			pasos = append(pasos, fmt.Sprint("anotar ", c))
			horas = append(horas, cuando)
		},
		func(context.Context, time.Duration) bool {
			pasos = append(pasos, "dormir")
			durmio = append(durmio, time.Now())
			return true
		})

	if !reflect.DeepEqual(pasos, []string{"dormir", "anotar 2", "dormir", "anotar 2"}) {
		t.Fatalf("pasos = %v", pasos)
	}
	for i := range horas {
		if horas[i].After(durmio[i]) {
			t.Errorf("la caída %d se anotó con %v, después de empezar a esperar (%v): tiene que ser la hora de la salida", i, horas[i], durmio[i])
		}
	}
}

func TestSiMatanAlVigiaMientrasEsperaNoSeAnotaNada(t *testing.T) {
	lanzar, vueltas := guion(1, 0)
	anotados := 0

	err := Vigilar(context.Background(), lanzar, func(int, time.Time) { anotados++ },
		func(context.Context, time.Duration) bool { return false })

	if err != nil || *vueltas != 1 || anotados != 0 {
		t.Fatalf("err %v, vueltas %d, anotados %d", err, *vueltas, anotados)
	}
}

// Un hijo que no se pudo ni arrancar no dejó nada que contar: va al log del
// vigía, y no al servidor como una caída del agente.
func TestSiNoArrancaSeReintentaSinAnotar(t *testing.T) {
	vueltas := 0
	lanzar := func(context.Context) (int, error) {
		vueltas++
		if vueltas == 1 {
			return 0, errors.New("el binario está a medio reemplazar")
		}
		return 0, nil
	}
	var esperas []time.Duration
	anotados := 0

	err := Vigilar(context.Background(), lanzar, func(int, time.Time) { anotados++ }, dormirAnotando(&esperas))

	if err != nil || vueltas != 2 || anotados != 0 {
		t.Fatalf("err %v, vueltas %d, anotados %d", err, vueltas, anotados)
	}
	if !reflect.DeepEqual(esperas, []time.Duration{time.Second}) {
		t.Errorf("esperas = %v", esperas)
	}
}

func TestElQueSeActualizoArrancaDeNuevoSinEsperaNiAnotar(t *testing.T) {
	lanzar, vueltas := guion(CodigoActualizado, 0)
	anotados := 0
	esperas := 0

	_ = Vigilar(context.Background(), lanzar,
		func(int, time.Time) { anotados++ },
		func(context.Context, time.Duration) bool { esperas++; return true })

	if *vueltas != 2 || anotados != 0 || esperas != 0 {
		t.Fatalf("vueltas %d, anotados %d, esperas %d", *vueltas, anotados, esperas)
	}
}

func TestSiAguantoLaEsperaVuelveAEmpezar(t *testing.T) {
	viejo := aguante
	aguante = 0 // toda vuelta cuenta como larga
	defer func() { aguante = viejo }()
	lanzar, _ := guion(1, 1, 1, 0)
	var esperas []time.Duration

	_ = Vigilar(context.Background(), lanzar, func(int, time.Time) {},
		func(_ context.Context, d time.Duration) bool { esperas = append(esperas, d); return true })

	if !reflect.DeepEqual(esperas, []time.Duration{time.Second, time.Second, time.Second}) {
		t.Errorf("esperas = %v", esperas)
	}
}

func TestCancelarElContextoTerminaElVigia(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	lanzar := func(context.Context) (int, error) { cancel(); return 1, nil }

	if err := Vigilar(ctx, lanzar, func(int, time.Time) { t.Error("anotó una caída que fue un cierre") }, Dormir); err != nil {
		t.Fatal(err)
	}
}

// El proceso de verdad: el hijo es este mismo binario de test.
func TestProcesoDevuelveElCodigoYMarcaAlHijoComoVigilado(t *testing.T) {
	if os.Getenv("VIGIA_HIJO") == "1" {
		if os.Getenv(EnvVigilado) != "1" {
			os.Exit(7)
		}
		os.Exit(CodigoActualizado)
	}
	lanzar := Proceso(os.Args[0], []string{"-test.run=TestProcesoDevuelveElCodigoYMarcaAlHijoComoVigilado"}, filepath.Join(t.TempDir(), "impresion.crash"))
	t.Setenv("VIGIA_HIJO", "1")

	codigo, err := lanzar(context.Background())

	if err != nil || codigo != CodigoActualizado {
		t.Fatalf("codigo %d, err %v (7 = el hijo no vio %s)", codigo, err, EnvVigilado)
	}
}

// La salida de error del hijo va derecho a impresion.crash: ahí escribe Go el
// motivo de una caída, también lo que escribe antes de darse por caído (el
// «fatal error: …»). La salida normal sigue yendo al log del vigía.
func TestProcesoMandaLaSalidaDeErrorDelHijoAlArchivoDeCaidas(t *testing.T) {
	if os.Getenv("VIGIA_HIJO_SALIDAS") == "1" {
		fmt.Fprintln(os.Stderr, "fatal error: esto va a la salida de error")
		fmt.Fprintln(os.Stdout, "esto va a la salida normal")
		os.Exit(2)
	}
	caidas := filepath.Join(t.TempDir(), "impresion.crash")
	if err := os.WriteFile(caidas, []byte("lo que ya estaba\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var logDelVigia bytes.Buffer
	antes := log.Writer()
	log.SetOutput(&logDelVigia)
	defer log.SetOutput(antes)
	t.Setenv("VIGIA_HIJO_SALIDAS", "1")

	codigo, err := Proceso(os.Args[0], []string{"-test.run=^TestProcesoMandaLaSalidaDeErrorDelHijoAlArchivoDeCaidas$"}, caidas)(context.Background())

	if err != nil || codigo != 2 {
		t.Fatalf("codigo %d, err %v", codigo, err)
	}
	raw, err := os.ReadFile(caidas)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "lo que ya estaba\nfatal error: esto va a la salida de error\n" {
		t.Errorf("impresion.crash = %q", raw)
	}
	if !strings.Contains(logDelVigia.String(), "esto va a la salida normal") || strings.Contains(logDelVigia.String(), "fatal error") {
		t.Errorf("log del vigía = %q", logDelVigia.String())
	}
}

func TestProcesoSinBinarioEsUnError(t *testing.T) {
	if _, err := Proceso("/no/existe/agente", nil, filepath.Join(t.TempDir(), "impresion.crash"))(context.Background()); err == nil {
		t.Fatal("tenía que fallar: el vigía lo deja en el log, sin anotarlo como caída, y reintenta")
	}
}

// dormirAnotando devuelve un dormir de mentira que guarda lo que le pidieron.
func dormirAnotando(esperas *[]time.Duration) func(context.Context, time.Duration) bool {
	return func(_ context.Context, d time.Duration) bool { *esperas = append(*esperas, d); return true }
}

// Un binario nuevo que sale con 3 una y otra vez (un chequeo que siempre ve una
// versión más nueva) no puede hacer girar al vigía a toda velocidad.
func TestActualizarYActualizarEnSegundosEsperaSinAnotar(t *testing.T) {
	lanzar, vueltas := guion(CodigoActualizado, CodigoActualizado, CodigoActualizado, 0)
	var esperas []time.Duration
	anotados := 0

	err := Vigilar(context.Background(), lanzar, func(int, time.Time) { anotados++ }, dormirAnotando(&esperas))

	// La primera actualización es instantánea; desde la segunda seguida se espera 1 s, 2 s...
	if err != nil || *vueltas != 4 || anotados != 0 {
		t.Fatalf("err %v, vueltas %d, anotados %d", err, *vueltas, anotados)
	}
	if !reflect.DeepEqual(esperas, []time.Duration{time.Second, 2 * time.Second}) {
		t.Errorf("esperas = %v", esperas)
	}
}

func TestLaEsperaDeActualizacionesLlegaATopeDeUnMinuto(t *testing.T) {
	cod := CodigoActualizado
	lanzar, _ := guion(cod, cod, cod, cod, cod, cod, cod, 0)
	var esperas []time.Duration

	_ = Vigilar(context.Background(), lanzar, func(int, time.Time) {}, dormirAnotando(&esperas))

	quiero := []time.Duration{time.Second, 2 * time.Second, 5 * time.Second, 15 * time.Second, time.Minute, time.Minute}
	if !reflect.DeepEqual(esperas, quiero) {
		t.Errorf("esperas = %v, quiero %v", esperas, quiero)
	}
}

func TestSiLaActualizacionAguantoNoEsUnBucle(t *testing.T) {
	viejo := ventanaDeActualizacion
	ventanaDeActualizacion = 0 // toda vuelta cuenta como larga
	defer func() { ventanaDeActualizacion = viejo }()
	lanzar, vueltas := guion(CodigoActualizado, CodigoActualizado, CodigoActualizado, 0)
	var esperas []time.Duration

	_ = Vigilar(context.Background(), lanzar, func(int, time.Time) {}, dormirAnotando(&esperas))

	if *vueltas != 4 || len(esperas) != 0 {
		t.Errorf("vueltas %d, esperas %v: tenía que arrancar de nuevo sin esperar", *vueltas, esperas)
	}
}

func TestUnaCaidaEnMedioCortaLaCuentaDeActualizaciones(t *testing.T) {
	cod := CodigoActualizado
	lanzar, _ := guion(cod, 1, cod, cod, 0)
	var esperas []time.Duration
	var anotados []int

	_ = Vigilar(context.Background(), lanzar, func(c int, _ time.Time) { anotados = append(anotados, c) }, dormirAnotando(&esperas))

	// 3 (instantáneo), 1 (caída: 1 s), 3 (primero de nuevo: instantáneo), 3 (segundo seguido: 1 s).
	if !reflect.DeepEqual(esperas, []time.Duration{time.Second, time.Second}) {
		t.Errorf("esperas = %v", esperas)
	}
	if !reflect.DeepEqual(anotados, []int{1}) {
		t.Errorf("anotados = %v: sólo la caída se anota", anotados)
	}
}

func TestSiCancelanMientrasEsperaLaActualizacionTermina(t *testing.T) {
	lanzar, vueltas := guion(CodigoActualizado, CodigoActualizado, 0)

	err := Vigilar(context.Background(), lanzar, func(int, time.Time) {},
		func(context.Context, time.Duration) bool { return false })

	if err != nil || *vueltas != 2 {
		t.Fatalf("err %v, vueltas %d", err, *vueltas)
	}
}
