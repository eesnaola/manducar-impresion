package fiscal

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// difusionFalsa hace de las impresoras fiscales Hasar 2G en lo que toca a la
// búsqueda: a quien le manda la «@» por UDP le contesta otra «@», que es lo
// que espera el buscador de Hasar (discover.exe).
type difusionFalsa struct {
	mu        sync.Mutex
	recibidos [][]byte
	conexion  *net.UDPConn
	// contesta: lo que devuelve; una Hasar devuelve la misma «@».
	contesta []byte
}

func nuevaDifusionFalsa(t *testing.T) *difusionFalsa {
	t.Helper()
	conexion, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	d := &difusionFalsa{conexion: conexion, contesta: []byte{'@'}}
	t.Cleanup(func() { conexion.Close() })
	go func() {
		buf := make([]byte, 64)
		for {
			n, quien, err := conexion.ReadFromUDP(buf)
			if err != nil {
				return
			}
			d.mu.Lock()
			d.recibidos = append(d.recibidos, append([]byte(nil), buf[:n]...))
			contesta := d.contesta
			d.mu.Unlock()
			_, _ = conexion.WriteToUDP(contesta, quien)
		}
	}()
	return d
}

func (d *difusionFalsa) contestar(b []byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.contesta = b
}

func (d *difusionFalsa) puerto() int { return d.conexion.LocalAddr().(*net.UDPAddr).Port }

func (d *difusionFalsa) pedidos() [][]byte {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([][]byte(nil), d.recibidos...)
}

// buscarDePrueba arma la búsqueda para un test: a quién se le pregunta por
// difusión, a qué vecinos se barre, y en qué puertos atienden los de mentira.
// Sin esto buscaría en la red de la máquina que corre los tests.
func buscarDePrueba(t *testing.T, puertoUDP int, difusion []net.IP, vecinos []string, puertoHTTP string) {
	t.Helper()
	a, b, c, d, e, f := puertoDescubrirHasar, destinosDeDifusion, vecinosDeRed, puertoHTTPHasar, esperaDeDifusion, plazoDeBarrido
	g, h, i := usbEnchufados, libreriaDeEpson, puertoDeHasarUSB
	t.Cleanup(func() {
		puertoDescubrirHasar, destinosDeDifusion, vecinosDeRed, puertoHTTPHasar, esperaDeDifusion, plazoDeBarrido = a, b, c, d, e, f
		usbEnchufados, libreriaDeEpson, puertoDeHasarUSB = g, h, i
	})
	puertoDeHasarUSB = func() (string, error) { return "", errNoHayHasarUSB }
	// Tampoco mira qué hay enchufado en la máquina que corre los tests.
	usbEnchufados = func() []string { return nil }
	libreriaDeEpson = func() LibreriaEpson { return LibreriaEpson{Disponible: true} }
	puertoDescubrirHasar = puertoUDP
	destinosDeDifusion = func() []net.IP { return difusion }
	vecinosDeRed = func() []string { return vecinos }
	puertoHTTPHasar = puertoHTTP
	esperaDeDifusion = 150 * time.Millisecond
	plazoDeBarrido = 300 * time.Millisecond
}

func puertoDe(direccion string) string {
	_, puerto, _ := net.SplitHostPort(direccion)
	return ":" + puerto
}

var aca = []net.IP{net.IPv4(127, 0, 0, 1)}

func TestBuscarEncuentraLaHasarQueContestaLaDifusion(t *testing.T) {
	equipo := nuevaHasarFalsa(t)
	difusion := nuevaDifusionFalsa(t)
	buscarDePrueba(t, difusion.puerto(), aca, nil, puertoDe(equipo.direccion()))

	encontrados := Buscar(context.Background())

	if len(encontrados) != 1 {
		t.Fatalf("tenía que encontrar una, encontró %d: %+v", len(encontrados), encontrados)
	}
	e := encontrados[0]
	if e.Marca != MarcaHasar || e.Conexion != equipo.direccion() {
		t.Errorf("marca y conexión = %q, %q; se esperaba hasar en %s", e.Marca, e.Conexion, equipo.direccion())
	}
	if e.Equipo == nil || e.Equipo.Modelo != "Hasar SMH/PT-250F V: 02.00" || e.Equipo.CUIT != "30123456781" || e.Equipo.PuntoDeVenta != 99998 {
		t.Errorf("no trajo lo que el equipo dice de sí mismo: %+v", e.Equipo)
	}
	if e.Aviso != "" {
		t.Errorf("no había nada que avisar: %q", e.Aviso)
	}
	// Lo que se manda es lo que manda el buscador de Hasar: una «@» sola.
	pedidos := difusion.pedidos()
	if len(pedidos) == 0 {
		t.Fatal("no se preguntó por difusión")
	}
	for _, p := range pedidos {
		if string(p) != "@" {
			t.Errorf("por difusión se mandó %q; el buscador de Hasar manda «@»", p)
		}
	}
}

func TestBuscarNoEmiteNiCambiaNadaEnElEquipo(t *testing.T) {
	equipo := nuevaHasarFalsa(t)
	difusion := nuevaDifusionFalsa(t)
	buscarDePrueba(t, difusion.puerto(), aca, []string{"127.0.0.1"}, puertoDe(equipo.direccion()))

	Buscar(context.Background())

	for _, comando := range equipo.pedidos {
		if comando != "ConsultarVersion" && comando != "ConsultarDatosInicializacion" {
			t.Errorf("buscar le mandó «%s» al equipo: sólo puede consultar", comando)
		}
	}
}

func TestLaHasarConOtraClaveApareceConElAviso(t *testing.T) {
	equipo := nuevaHasarFalsa(t)
	equipo.clave = "4321"
	difusion := nuevaDifusionFalsa(t)
	buscarDePrueba(t, difusion.puerto(), aca, nil, puertoDe(equipo.direccion()))

	encontrados := Buscar(context.Background())

	if len(encontrados) != 1 {
		t.Fatalf("contestó la difusión: tiene que aparecer aunque no acepte la clave de fábrica. Encontró %+v", encontrados)
	}
	e := encontrados[0]
	if e.Conexion != equipo.direccion() || e.Equipo != nil {
		t.Errorf("tenía que venir la dirección y nada del equipo: %+v", e)
	}
	if !strings.Contains(e.Aviso, "clave") {
		t.Errorf("el aviso tiene que nombrar la clave: %q", e.Aviso)
	}
}

func TestQuienContestaOtraCosaALaDifusionNoEsUnaHasar(t *testing.T) {
	equipo := nuevaHasarFalsa(t)
	otro := nuevaDifusionFalsa(t)
	otro.contestar([]byte("hola"))
	buscarDePrueba(t, otro.puerto(), aca, nil, puertoDe(equipo.direccion()))

	if encontrados := Buscar(context.Background()); len(encontrados) != 0 {
		t.Errorf("en ese puerto contestó otra cosa que la «@» de una Hasar: %+v", encontrados)
	}
	if len(otro.pedidos()) == 0 {
		t.Error("el test no probó nada: no le llegó la pregunta")
	}
}

func TestElBarridoEncuentraLaQueNoContestaLaDifusion(t *testing.T) {
	equipo := nuevaHasarFalsa(t)
	// Nadie contesta por difusión (una red que la corta): queda el barrido.
	buscarDePrueba(t, 9, nil, []string{"127.0.0.1"}, puertoDe(equipo.direccion()))

	encontrados := Buscar(context.Background())

	if len(encontrados) != 1 || encontrados[0].Conexion != equipo.direccion() || encontrados[0].Equipo == nil {
		t.Fatalf("el barrido tenía que encontrarla con sus datos: %+v", encontrados)
	}
	if encontrados[0].Equipo.Modelo != "Hasar SMH/PT-250F V: 02.00" {
		t.Errorf("modelo = %q", encontrados[0].Equipo.Modelo)
	}
}

func TestElBarridoNoTomaPorHasarACualquierAparato(t *testing.T) {
	casos := map[string]http.HandlerFunc{
		"un router que pide clave":      func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnauthorized) },
		"una página cualquiera":         func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("<html>hola</html>")) },
		"algo que no conoce la ruta":    func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) },
		"un JSON que no es la consulta": func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"ok":true}`)) },
	}
	for nombre, atender := range casos {
		t.Run(nombre, func(t *testing.T) {
			aparato := httptest.NewServer(atender)
			defer aparato.Close()
			buscarDePrueba(t, 9, nil, []string{"127.0.0.1"}, puertoDe(strings.TrimPrefix(aparato.URL, "http://")))

			if encontrados := Buscar(context.Background()); len(encontrados) != 0 {
				t.Errorf("tomó por impresora fiscal a %s: %+v", nombre, encontrados)
			}
		})
	}
}

func TestLaQueApareceDeLasDosManerasSaleUnaSolaVez(t *testing.T) {
	equipo := nuevaHasarFalsa(t)
	difusion := nuevaDifusionFalsa(t)
	buscarDePrueba(t, difusion.puerto(), aca, []string{"127.0.0.1"}, puertoDe(equipo.direccion()))

	if encontrados := Buscar(context.Background()); len(encontrados) != 1 {
		t.Fatalf("es un solo equipo: %+v", encontrados)
	}
}

func TestSinRedDondeBuscarNoEncuentraNada(t *testing.T) {
	buscarDePrueba(t, 9, nil, nil, "")

	if encontrados := Buscar(context.Background()); len(encontrados) != 0 {
		t.Errorf("no había dónde buscar: %+v", encontrados)
	}
}

func TestSiNoHayNadieEnElBarridoNoSeQuedaEsperando(t *testing.T) {
	// Un puerto donde no atiende nadie: se abre y se cierra para saber que está libre.
	libre, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	puerto := ":" + strconv.Itoa(libre.Addr().(*net.TCPAddr).Port)
	libre.Close()
	buscarDePrueba(t, 9, nil, []string{"127.0.0.1"}, puerto)

	empezo := time.Now()
	if encontrados := Buscar(context.Background()); len(encontrados) != 0 {
		t.Errorf("no había nadie: %+v", encontrados)
	}
	if tardo := time.Since(empezo); tardo > 3*time.Second {
		t.Errorf("tardó %s en darse cuenta de que no había nadie", tardo)
	}
}

func TestLosVecinosSonLosDeLaRedDeEstaComputadora(t *testing.T) {
	vecinos := vecinosDe(net.IPv4(192, 168, 0, 23), net.CIDRMask(24, 32))

	if len(vecinos) != 253 {
		t.Fatalf("en una /24 hay 254 direcciones y una es la propia: salieron %d", len(vecinos))
	}
	if vecinos[0] != "192.168.0.1" || vecinos[len(vecinos)-1] != "192.168.0.254" {
		t.Errorf("van de la .1 a la .254: %s … %s", vecinos[0], vecinos[len(vecinos)-1])
	}
	for _, v := range vecinos {
		if v == "192.168.0.23" || v == "192.168.0.0" || v == "192.168.0.255" {
			t.Errorf("%s no es un vecino", v)
		}
	}
}

func TestDeUnaRedGrandeSeRecorreSoloElTramoDeEstaComputadora(t *testing.T) {
	vecinos := vecinosDe(net.IPv4(10, 0, 5, 7), net.CIDRMask(16, 32))

	if len(vecinos) != 253 || vecinos[0] != "10.0.5.1" || vecinos[len(vecinos)-1] != "10.0.5.254" {
		t.Errorf("de una /16 se recorre la /24 propia: %d, %v…", len(vecinos), vecinos[:1])
	}
}

func TestUnaRedChicaSeRecorreEntera(t *testing.T) {
	vecinos := vecinosDe(net.IPv4(192, 168, 1, 130), net.CIDRMask(26, 32))

	if !reflect.DeepEqual([]string{vecinos[0], vecinos[len(vecinos)-1]}, []string{"192.168.1.129", "192.168.1.190"}) || len(vecinos) != 61 {
		t.Errorf("una /26 tiene 62 direcciones y una es la propia: %d, de %s a %s", len(vecinos), vecinos[0], vecinos[len(vecinos)-1])
	}
}

func TestNoSeBarreUnaRedQueNoEsDeLocal(t *testing.T) {
	for _, ip := range []net.IP{net.IPv4(8, 8, 8, 8), net.IPv4(127, 0, 0, 1), net.IPv4(169, 254, 3, 4), net.ParseIP("fe80::1")} {
		if vecinos := vecinosDe(ip, net.CIDRMask(24, 32)); len(vecinos) != 0 {
			t.Errorf("%s no es una red de local y salió a barrer %d direcciones", ip, len(vecinos))
		}
	}
}

func TestLaDifusionDeUnaRed(t *testing.T) {
	casos := []struct {
		ip      net.IP
		unos    int
		difunde string
	}{
		{net.IPv4(192, 168, 0, 23), 24, "192.168.0.255"},
		{net.IPv4(10, 0, 5, 7), 16, "10.0.255.255"},
		{net.IPv4(192, 168, 1, 130), 26, "192.168.1.191"},
	}
	for _, c := range casos {
		if d := difusionDe(c.ip, net.CIDRMask(c.unos, 32)); d == nil || d.String() != c.difunde {
			t.Errorf("la difusión de %s/%d es %s, salió %v", c.ip, c.unos, c.difunde, d)
		}
	}
	if d := difusionDe(net.ParseIP("fe80::1"), net.CIDRMask(64, 128)); d != nil {
		t.Errorf("IPv6 no tiene difusión: %v", d)
	}
}
