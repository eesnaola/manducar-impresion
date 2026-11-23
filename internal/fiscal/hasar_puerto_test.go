package fiscal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// hasarPorPuerto es la Hasar de mentira de hasar_test.go atendida por su
// puerto serie: el mismo equipo —su estado, sus números, sus respuestas— pero
// del otro lado de un cable, hablando el protocolo empaquetado. Traduce cada
// comando que llega a su forma con nombres, se lo da al equipo de mentira y
// empaqueta lo que contesta.
type hasarPorPuerto struct {
	*hasarFalsa
	t  *testing.T
	pm sync.Mutex
	// Lo que hace con los próximos paquetes, para probar el diálogo.
	naks         int  // cuántas veces dice «no entendí» antes de aceptar
	demoras      int  // cuántos «esperá» manda antes de contestar
	pideAtencion bool // los «esperá» son de los que piden que alguien la atienda
	sumasMalas   int  // cuántas respuestas manda dañadas antes de la buena
	// Lo que vio.
	paquetes   [][]byte // cada paquete recibido, entero
	secuencias []byte   // el número de secuencia de cada comando aceptado
	abiertos   int
	cerrados   int
	acuses     []byte // lo que el agente contestó a cada respuesta (ACK o NAK)
	// Como un equipo de verdad: si llega otra vez el mismo número de
	// secuencia, repite la respuesta anterior sin hacer nada.
	ultimaSN        byte
	ultimaRespuesta []byte
}

func nuevaHasarPorPuerto(t *testing.T) *hasarPorPuerto {
	t.Helper()
	return &hasarPorPuerto{hasarFalsa: nuevaHasarFalsa(t), t: t}
}

// puertoDePrueba es el lado del agente: como un puerto serie de verdad, leer
// devuelve lo que haya o, si en un rato corto no llegó nada, cero bytes.
type puertoDePrueba struct {
	net.Conn
	alCerrar func()
	una      sync.Once
}

func (p *puertoDePrueba) Read(b []byte) (int, error) {
	_ = p.Conn.SetReadDeadline(time.Now().Add(15 * time.Millisecond))
	n, err := p.Conn.Read(b)
	var tiempo net.Error
	if errors.As(err, &tiempo) && tiempo.Timeout() {
		return n, nil
	}
	return n, err
}

func (p *puertoDePrueba) Close() error {
	p.una.Do(p.alCerrar)
	return p.Conn.Close()
}

// enchufar deja al equipo de mentira atendiendo en el puerto de ese nombre
// (y, si se pide, como «la Hasar que está enchufada por USB»), y acorta las
// esperas para que los tests no tarden.
func (f *hasarPorPuerto) enchufar(nombre string) {
	f.t.Helper()
	a, b, c, d := abrirPuertoSerie, puertoDeHasarUSB, silencioDeHasar, secuenciaInicial
	f.t.Cleanup(func() { abrirPuertoSerie, puertoDeHasarUSB, silencioDeHasar, secuenciaInicial = a, b, c, d })
	abrirPuertoSerie = func(puerto string, velocidad int) (puertoSerie, error) {
		if puerto != nombre {
			return nil, fmt.Errorf("no existe el puerto %s", puerto)
		}
		agente, equipo := net.Pipe()
		f.pm.Lock()
		f.abiertos++
		f.pm.Unlock()
		go f.atenderPuerto(equipo)
		return &puertoDePrueba{Conn: agente, alCerrar: func() {
			f.pm.Lock()
			f.cerrados++
			f.pm.Unlock()
			equipo.Close()
		}}, nil
	}
	puertoDeHasarUSB = func() (string, error) { return "", errors.New("no hay ninguna Hasar enchufada por USB") }
	silencioDeHasar = 400 * time.Millisecond
}

// atenderPuerto es el equipo: lee paquetes y los contesta hasta que le
// desenchufan el cable.
func (f *hasarPorPuerto) atenderPuerto(cable net.Conn) {
	defer cable.Close()
	leer := func() (byte, bool) {
		b := make([]byte, 1)
		if _, err := cable.Read(b); err != nil {
			return 0, false
		}
		return b[0], true
	}
	for {
		c, ok := leer()
		if !ok {
			return
		}
		if c != hasarSTX {
			continue
		}
		paquete := []byte{c}
		for len(paquete) < 5 || paquete[len(paquete)-5] != hasarETX {
			if c, ok = leer(); !ok {
				return
			}
			paquete = append(paquete, c)
		}
		f.pm.Lock()
		f.paquetes = append(f.paquetes, paquete)
		naks := f.naks
		if naks > 0 {
			f.naks--
		}
		f.pm.Unlock()
		sn, contenido, err := desempaquetar(paquete)
		if err != nil || naks > 0 {
			_, _ = cable.Write([]byte{hasarNAK})
			continue
		}
		_, _ = cable.Write([]byte{hasarACK})

		f.pm.Lock()
		repetido := sn == f.ultimaSN && f.ultimaRespuesta != nil
		respuesta := f.ultimaRespuesta
		demoras, aviso := f.demoras, byte(hasarDC2)
		if f.pideAtencion {
			aviso = hasarDC4
		}
		f.demoras = 0
		f.pm.Unlock()
		if !repetido {
			var calla bool
			respuesta, calla = f.hacer(contenido)
			f.pm.Lock()
			f.secuencias = append(f.secuencias, sn)
			f.ultimaSN, f.ultimaRespuesta = sn, respuesta
			f.pm.Unlock()
			if calla {
				continue // lo hizo y la respuesta se perdió en el camino
			}
		}
		for i := 0; i < demoras; i++ {
			time.Sleep(20 * time.Millisecond)
			_, _ = cable.Write([]byte{aviso})
		}
		for {
			envio := empaquetar(sn, respuesta)
			f.pm.Lock()
			if f.sumasMalas > 0 {
				f.sumasMalas--
				envio[len(envio)-1] ^= 0x01
			}
			f.pm.Unlock()
			if _, err := cable.Write(envio); err != nil {
				return
			}
			acuse, ok := leer()
			if !ok {
				return
			}
			f.pm.Lock()
			f.acuses = append(f.acuses, acuse)
			f.pm.Unlock()
			if acuse != hasarNAK {
				break
			}
		}
	}
}

// hacer pasa un comando empaquetado a su forma con nombres, se lo da al
// equipo de mentira y empaqueta la respuesta. calla: lo hizo y no contesta.
func (f *hasarPorPuerto) hacer(contenido []byte) (respuesta []byte, calla bool) {
	var nombre string
	var comando comandoEmpaquetado
	for n, c := range comandosEmpaquetados {
		if len(contenido) >= 2 && c.id == contenido[1] {
			nombre, comando = n, c
		}
	}
	if nombre == "" {
		return []byte{hasarESC, contenido[1], hasarFS, '0', '0', '0', '0', hasarFS, '0', '2', '0', '8'}, false
	}
	// De posiciones y códigos a nombres e identificadores.
	objeto := map[string]string{}
	if len(contenido) > 3 {
		for i, valor := range strings.Split(string(contenido[3:]), string(rune(hasarFS))) {
			if i >= len(comando.entrada) || valor == "" {
				continue
			}
			campo := comando.entrada[i]
			tabla := valoresDeHasar[campo]
			if campo == "Operacion" {
				tabla = operacionesDeHasar[nombre]
			}
			valor = deCP437([]byte(valor))
			for identificador, codigo := range tabla {
				if codigo == valor {
					valor = identificador
				}
			}
			objeto[campo] = valor
		}
	}
	cuerpo, _ := json.Marshal(map[string]any{nombre: objeto})
	crudo, _ := json.Marshal(objeto)

	f.hasarFalsa.mu.Lock()
	if f.apagada {
		f.hasarFalsa.mu.Unlock()
		return nil, true
	}
	f.pedidos = append(f.pedidos, nombre)
	f.cuerpos = append(f.cuerpos, string(cuerpo))
	dicho := f.responder(nombre, crudo)
	calla = f.corta[nombre]
	if calla && f.apagarAlCortar {
		f.apagada = true
	}
	f.hasarFalsa.mu.Unlock()

	// Y la respuesta, de nombres a posiciones.
	var sobre map[string]map[string]json.RawMessage
	_ = json.Unmarshal([]byte(dicho), &sobre)
	datos, aceptado := sobre[nombre]
	if !aceptado {
		// Un rechazo: por el puerto no hay texto, sólo el bit de «no es válido en este estado».
		return []byte{hasarESC, comando.id, hasarFS, '0', '0', '0', '0', hasarFS, '0', '2', '2', '0'}, calla
	}
	var estado struct{ Impresora, Fiscal []string }
	_ = json.Unmarshal(datos["Estado"], &estado)
	enBits := func(nombres []string, bits map[int]string) string {
		var valor uint64
		for bit, n := range bits {
			for _, prendido := range nombres {
				if prendido == n {
					valor |= 1 << bit
				}
			}
		}
		return fmt.Sprintf("%04X", valor)
	}
	salida := comando.salida
	if nombre == "CerrarJornadaFiscal" {
		salida = salidaDeCierre[objeto["Reporte"][len("Reporte"):]]
	}
	partes := []string{enBits(estado.Impresora, bitsDeImpresora), enBits(estado.Fiscal, bitsFiscales)}
	for _, campo := range salida {
		crudo, vino := datos[campo]
		if !vino {
			break
		}
		if campo == "EstadoAuxiliar" {
			var lista []string
			_ = json.Unmarshal(crudo, &lista)
			partes = append(partes, enBits(lista, bitsAuxiliares))
			continue
		}
		var valor string
		if json.Unmarshal(crudo, &valor) != nil {
			valor = strings.Trim(string(crudo), `"`)
		}
		if valor == sinDocumento {
			valor = "0"
		}
		if seNombran[campo] {
			valor = enCodigo(nombre, campo, valor)
		}
		partes = append(partes, valor)
	}
	respuesta = []byte{hasarESC, comando.id}
	for _, parte := range partes {
		respuesta = append(respuesta, hasarFS)
		respuesta = append(respuesta, aCP437(parte)...)
	}
	return respuesta, calla
}

func (f *hasarPorPuerto) driver(conexion string) Driver {
	f.t.Helper()
	d, err := Abrir(Controlador{Marca: MarcaHasar, Conexion: conexion})
	if err != nil {
		f.t.Fatal(err)
	}
	return d
}

func (f *hasarPorPuerto) vistos() (secuencias []byte, abiertos, cerrados int) {
	f.pm.Lock()
	defer f.pm.Unlock()
	return append([]byte(nil), f.secuencias...), f.abiertos, f.cerrados
}

// «Probar conexión» con la Hasar enchufada por USB: dice lo mismo que por la red.
func TestPorElPuertoLaHasarDiceQuienEsIgualQuePorLaRed(t *testing.T) {
	equipo := nuevaHasarPorPuerto(t)
	equipo.enchufar("COM7")

	dicho, err := equipo.driver("COM7").Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	quiero := Equipo{
		Marca: "hasar", Modelo: "Hasar SMH/PT-250F V: 02.00", PuntoDeVenta: 99998,
		CUIT: "30123456781", RazonSocial: "KROKANTISSIMO SA", Registro: "HHAASS1234567890",
		Responsabilidad: "I", Estado: []string{"MemoriaFiscalInicializada"},
	}
	if a, b := mustJSON(t, dicho), mustJSON(t, quiero); a != b {
		t.Errorf("el equipo salió\n %s\nquiero\n %s", a, b)
	}
	// Al abrir el puerto se presenta con una consulta de versión; después, las dos de Info.
	if vino := equipo.secuencia(); vino != "ConsultarVersion,ConsultarVersion,ConsultarDatosInicializacion" {
		t.Errorf("le mandó %q", vino)
	}
	if _, abiertos, cerrados := equipo.vistos(); abiertos != 1 || cerrados != 1 {
		t.Errorf("el puerto se abre para la operación y se suelta al terminar: abierto %d, cerrado %d", abiertos, cerrados)
	}
}

func TestPorElPuertoSeEmiteElMismoTiqueConLosMismosComandos(t *testing.T) {
	equipo := nuevaHasarPorPuerto(t)
	equipo.enchufar("COM7")

	emitido, err := equipo.driver("COM7").Emitir(context.Background(), tiqueDePrueba())
	if err != nil {
		t.Fatalf("%v\n secuencia: %s", err, equipo.secuencia())
	}

	if emitido.Numero != 1 || emitido.PuntoDeVenta != 99998 || emitido.Tipo != TipoTique {
		t.Errorf("emitido = %+v", emitido)
	}
	quiero := "ConsultarVersion,ConsultarDatosInicializacion,ConsultarEstado,AbrirDocumento,ImprimirItem,ImprimirItem,ImprimirPago,CerrarDocumento"
	if vino := equipo.secuencia(); vino != quiero {
		t.Errorf("comandos:\n vino   %s\n quiero %s", vino, quiero)
	}
	igual(t, "el primer renglón", equipo.campos(t, "ImprimirItem", 0), map[string]string{
		"Descripcion": "Pizza muzzarella", "Cantidad": "2", "PrecioUnitario": "8500.00", "CondicionIVA": "Gravado", "AlicuotaIVA": "21.00",
		"OperacionMonto": "ModoSumaMonto", "TipoImpuestoInterno": "IIVariableKIVA", "MagnitudImpuestoInterno": "0.00",
		"ModoDisplay": "DisplayNo", "ModoBaseTotal": "ModoPrecioTotal", "UnidadReferencia": "1",
	})
	// Cada comando con su número de secuencia, uno más que el anterior.
	secuencias, abiertos, cerrados := equipo.vistos()
	for i := 1; i < len(secuencias); i++ {
		if secuencias[i] != siguienteSecuencia(secuencias[i-1]) {
			t.Errorf("la secuencia no avanza de a uno: %x", secuencias)
			break
		}
	}
	if abiertos != 1 || cerrados != 1 {
		t.Errorf("un comprobante es una sola conversación: abierto %d, cerrado %d", abiertos, cerrados)
	}
}

func TestSiElEquipoNoEntendioElPaqueteSeLoMandaDeNuevo(t *testing.T) {
	equipo := nuevaHasarPorPuerto(t)
	equipo.enchufar("COM7")
	equipo.naks = 2

	if _, err := equipo.driver("COM7").Info(context.Background()); err != nil {
		t.Fatal(err)
	}

	equipo.pm.Lock()
	defer equipo.pm.Unlock()
	if len(equipo.paquetes) < 3 || string(equipo.paquetes[0]) != string(equipo.paquetes[1]) || string(equipo.paquetes[1]) != string(equipo.paquetes[2]) {
		t.Errorf("tenía que mandar tres veces el mismo paquete, con el mismo número de secuencia: %q", equipo.paquetes)
	}
}

func TestSiElEquipoNuncaEntiendeSeDejaDeInsistir(t *testing.T) {
	equipo := nuevaHasarPorPuerto(t)
	equipo.enchufar("COM7")
	equipo.naks = 50

	_, err := equipo.driver("COM7").Info(context.Background())

	if err == nil || !strings.Contains(err.Error(), "no entendió") {
		t.Fatalf("quiero un error que diga que no entendió, vino: %v", err)
	}
	equipo.pm.Lock()
	defer equipo.pm.Unlock()
	if len(equipo.paquetes) > 5 {
		t.Errorf("insistió %d veces", len(equipo.paquetes))
	}
}

func TestMientrasElEquipoAvisaQueSigueTrabajandoSeLoEspera(t *testing.T) {
	equipo := nuevaHasarPorPuerto(t)
	equipo.enchufar("COM7")
	// Más tiempo avisando que el silencio que se tolera: lo que cuenta es que avisa.
	equipo.demoras = 40

	if _, err := equipo.driver("COM7").Info(context.Background()); err != nil {
		t.Fatalf("avisó todo el tiempo que seguía trabajando y no se lo esperó: %v", err)
	}
}

func TestUnaRespuestaQueLlegaDanadaSePideDeNuevo(t *testing.T) {
	equipo := nuevaHasarPorPuerto(t)
	equipo.enchufar("COM7")
	equipo.sumasMalas = 1

	if _, err := equipo.driver("COM7").Info(context.Background()); err != nil {
		t.Fatal(err)
	}

	equipo.pm.Lock()
	defer equipo.pm.Unlock()
	if len(equipo.acuses) < 2 || equipo.acuses[0] != hasarNAK || equipo.acuses[1] != hasarACK {
		t.Errorf("a la respuesta dañada se le contesta «no entendí» y a la buena «entendí»: %x", equipo.acuses)
	}
}

func TestSiElEquipoNoContestaNadaElErrorLoDice(t *testing.T) {
	equipo := nuevaHasarPorPuerto(t)
	equipo.enchufar("COM7")
	equipo.hasarFalsa.mu.Lock()
	equipo.apagada = true
	equipo.hasarFalsa.mu.Unlock()

	empezo := time.Now()
	_, err := equipo.driver("COM7").Info(context.Background())

	if err == nil || !strings.Contains(err.Error(), "no contest") {
		t.Fatalf("quiero un error que diga que no contesta, vino: %v", err)
	}
	if tardo := time.Since(empezo); tardo > 3*time.Second {
		t.Errorf("tardó %s en darse cuenta", tardo)
	}
	if _, abiertos, cerrados := equipo.vistos(); abiertos != cerrados {
		t.Errorf("el puerto quedó tomado: abierto %d, cerrado %d", abiertos, cerrados)
	}
}

func TestSiPideQueLaAtiendanYSeAcabaElTiempoElErrorLoDice(t *testing.T) {
	equipo := nuevaHasarPorPuerto(t)
	equipo.enchufar("COM7")
	equipo.pideAtencion = true
	equipo.demoras = 1000

	ctx, cancelar := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancelar()
	_, err := equipo.driver("COM7").Info(ctx)

	if err == nil || !strings.Contains(err.Error(), "atienda") {
		t.Fatalf("quiero un error que diga que está esperando que la atiendan, vino: %v", err)
	}
}

// El caso que no perdona: se manda a cerrar y la respuesta no llega. No se
// reenvía el cierre: se le pregunta al equipo en qué quedó.
func TestSiSePierdeLaRespuestaDelCierrePorElPuertoNoSeCierraDosVeces(t *testing.T) {
	equipo := nuevaHasarPorPuerto(t)
	equipo.enchufar("COM7")
	equipo.corta["CerrarDocumento"] = true

	emitido, err := equipo.driver("COM7").Emitir(context.Background(), tiqueDePrueba())
	if err != nil {
		t.Fatalf("el equipo lo emitió y se dio por no emitido: %v", err)
	}

	if emitido.Numero != 1 {
		t.Errorf("número = %d", emitido.Numero)
	}
	cierres := 0
	for _, comando := range strings.Split(equipo.secuencia(), ",") {
		if comando == "CerrarDocumento" {
			cierres++
		}
	}
	if cierres != 1 {
		t.Errorf("se mandó a cerrar %d veces: lo que el equipo emite no se deshace", cierres)
	}
}

func TestSiSeCortaElCableAlCerrarElErrorEsIncierto(t *testing.T) {
	equipo := nuevaHasarPorPuerto(t)
	equipo.enchufar("COM7")
	equipo.corta["CerrarDocumento"] = true
	equipo.apagarAlCortar = true

	_, err := equipo.driver("COM7").Emitir(context.Background(), tiqueDePrueba())

	if !errors.Is(err, ErrIncierto) {
		t.Fatalf("no se puede saber si salió: el error tiene que ser incierto, vino %v", err)
	}
}

func TestUnRechazoDelEquipoPorElPuertoCancelaElComprobante(t *testing.T) {
	equipo := nuevaHasarPorPuerto(t)
	equipo.enchufar("COM7")
	equipo.falla["ImprimirPago"] = "falta papel"

	_, err := equipo.driver("COM7").Emitir(context.Background(), tiqueDePrueba())

	if err == nil || !strings.Contains(err.Error(), "ImprimirPago") {
		t.Fatalf("quiero el rechazo con el comando que no aceptó, vino: %v", err)
	}
	if !strings.HasSuffix(equipo.secuencia(), "ImprimirPago,Cancelar") {
		t.Errorf("después del rechazo se cancela: %s", equipo.secuencia())
	}
}

// Cada conversación empieza con una consulta que no hace nada. Si el equipo
// repite la respuesta anterior porque el número de secuencia coincide con el
// de la última conversación, lo que se repite es esa consulta y no un cierre.
func TestUnaConversacionNuevaNoRecibeLaRespuestaDeLaAnterior(t *testing.T) {
	equipo := nuevaHasarPorPuerto(t)
	equipo.enchufar("COM7")
	// Las dos conversaciones arrancan en el mismo número, que es el peor caso.
	secuenciaInicial = func() byte { return 0x30 }

	primero, err := equipo.driver("COM7").Cerrar(context.Background(), CierreX)
	if err != nil {
		t.Fatal(err)
	}
	segundo, err := equipo.driver("COM7").Cerrar(context.Background(), CierreX)
	if err != nil {
		t.Fatal(err)
	}

	if primero.Numero != 12 || segundo.Numero != 12 {
		t.Errorf("cierres = %+v, %+v", primero, segundo)
	}
	if vino := equipo.secuencia(); vino != "ConsultarVersion,CerrarJornadaFiscal,ConsultarVersion,CerrarJornadaFiscal" {
		t.Errorf("el segundo cierre no llegó al equipo como un cierre nuevo: %s", vino)
	}
}

func TestLaConexionDiceSiEsPorElPuertoOPorLaRed(t *testing.T) {
	equipo := nuevaHasarPorPuerto(t)
	equipo.enchufar("COM7")

	for _, conexion := range []string{"COM7", "com7", " COM7 "} {
		if _, err := equipo.driver(conexion).Info(context.Background()); err != nil {
			t.Errorf("%q tenía que ir por el puerto COM7: %v", conexion, err)
		}
	}
	// «usb» es «la que está enchufada»: el puerto lo averigua el agente.
	if _, err := equipo.driver("usb").Info(context.Background()); err == nil || !strings.Contains(err.Error(), "USB") {
		t.Errorf("sin ninguna enchufada, «usb» tiene que decirlo: %v", err)
	}
	puertoDeHasarUSB = func() (string, error) { return "COM7", nil }
	if _, err := equipo.driver("USB").Info(context.Background()); err != nil {
		t.Errorf("con una enchufada en el COM7, «usb» tenía que encontrarla: %v", err)
	}
	// Y una dirección sigue yendo por la red.
	if _, err := equipo.driver(equipo.direccion()).Info(context.Background()); err != nil {
		t.Errorf("una dirección de red tiene que ir por HTTP: %v", err)
	}
	for conexion, puerto := range map[string]string{"/dev/ttyACM0": "/dev/ttyACM0", "COM12": "COM12", "192.168.1.50": "", "caja.local:8080": "", "": ""} {
		if p, _ := puertoDeConexion(conexion); p != puerto {
			t.Errorf("puertoDeConexion(%q) = %q, se esperaba %q", conexion, p, puerto)
		}
	}
}

func TestElCierreZPorElPuertoTraeLosMismosTotales(t *testing.T) {
	equipo := nuevaHasarPorPuerto(t)
	equipo.enchufar("COM7")

	cierre, err := equipo.driver("COM7").Cerrar(context.Background(), CierreZ)
	if err != nil {
		t.Fatal(err)
	}

	quiero := Cierre{Tipo: CierreZ, Numero: 40, Fecha: "2015-01-30", Total: "780.00", Comprobantes: 3, TotalNotasDeCredito: "0.00"}
	if a, b := mustJSON(t, cierre), mustJSON(t, quiero); a != b {
		t.Errorf("cierre\n %s\nquiero\n %s", a, b)
	}
}

func TestElNumeroDeSecuenciaArrancaEnCualquierLadoDelRango(t *testing.T) {
	vistos := map[byte]bool{}
	for i := 0; i < 400; i++ {
		sn := secuenciaInicial()
		if sn < 0x20 || sn > 0x7F {
			t.Fatalf("secuencia inicial fuera de rango: %s", strconv.Itoa(int(sn)))
		}
		vistos[sn] = true
	}
	if len(vistos) < 20 {
		t.Errorf("arranca casi siempre en el mismo lugar (%d distintos en 400)", len(vistos))
	}
}
