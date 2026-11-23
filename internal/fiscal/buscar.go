package fiscal

import (
	"bytes"
	"context"
	"errors"
	"net"
	"sort"
	"sync"
	"time"
)

// Encontrado es un controlador fiscal que apareció al buscar en la red del
// local: dónde está y, si contestó, quién es.
type Encontrado struct {
	Marca string `json:"marca"`
	// Conexion: lo que hay que cargar en el panel para llegar a él.
	Conexion string `json:"conexion"`
	// Equipo: lo que dice de sí mismo. Falta si no aceptó la clave de fábrica.
	Equipo *Equipo `json:"equipo,omitempty"`
	// Aviso: por qué no se sabe más de él, en palabras para el panel.
	Aviso string `json:"aviso,omitempty"`
}

// Lo que se sabe de cómo busca Hasar a sus impresoras fiscales 2G sale de su
// buscador (discover.exe, en el paquete de distribución): manda una «@» por
// difusión al puerto UDP 10028 y cada equipo le contesta otra «@» desde su
// dirección. No está en los manuales; se leyó del ejecutable.
var (
	puertoDescubrirHasar = 10028
	pedidoDescubrirHasar = []byte{'@'}
)

// Las perillas de la búsqueda, en variables para que los tests busquen en un
// equipo de mentira y no en la red de la máquina que los corre.
var (
	// destinosDeDifusion: a qué direcciones se manda la pregunta por difusión.
	destinosDeDifusion = difusionesLocales
	// vecinosDeRed: las direcciones que se recorren una por una.
	vecinosDeRed = vecinosLocales
	// puertoHTTPHasar: «:puerto» donde atiende el equipo; vacío es el 80.
	puertoHTTPHasar = ""
	// esperaDeDifusion: cuánto se esperan las respuestas de cada ronda. El
	// buscador de Hasar espera dos segundos; en una red de local contestan
	// en milésimas.
	esperaDeDifusion = 1200 * time.Millisecond
	// plazoDeBarrido: cuánto se espera a que un vecino abra la conexión.
	plazoDeBarrido = 400 * time.Millisecond
)

const (
	// rondasDeDifusion: la pregunta va por UDP y se puede perder.
	rondasDeDifusion = 2
	// obrerosDeBarrido: cuántos vecinos se prueban a la vez.
	obrerosDeBarrido = 64
	// plazoDeIdentificar: lo que se le espera a un equipo que diga quién es.
	plazoDeIdentificar = 3 * time.Second
	// topeDeVecinos: de ahí para arriba no es la red de un local.
	topeDeVecinos = 1024
)

// Buscar busca los controladores fiscales que hay en la red de esta
// computadora. No emite ni cambia nada: sólo pregunta quién anda ahí.
//
// Encuentra las Hasar de dos maneras a la vez: como su buscador (por
// difusión) y recorriendo los vecinos de la red con la consulta de versión de
// su interfaz HTTP. La segunda es por si la red corta la difusión, y sólo ve a
// los que tienen la clave de fábrica: sin la clave no se distingue una Hasar
// de cualquier aparato que pida contraseña.
//
// También la Hasar que esté enchufada por USB a esta computadora, a la que
// reconoce por sus identificadores y le pregunta quién es (buscarHasarUSB).
//
// De Epson encuentra la que está enchufada por USB, mirando la lista de lo
// enchufado (ver buscarEpson); las que van por puerto serie o por red no
// tienen cómo aparecer solas, y se cargan a mano.
func Buscar(ctx context.Context) []Encontrado {
	encontrados := buscarHasar(ctx)
	encontrados = append(encontrados, buscarHasarUSB(ctx)...)
	return append(encontrados, buscarEpson()...)
}

// buscarHasar busca las Hasar de la red, ordenadas por dirección.
func buscarHasar(ctx context.Context) []Encontrado {
	var contestaron []string
	var barridos []Encontrado
	var espera sync.WaitGroup
	espera.Add(2)
	go func() { defer espera.Done(); contestaron = difundir(ctx) }()
	go func() { defer espera.Done(); barridos = barrer(ctx) }()
	espera.Wait()

	porDireccion := map[string]Encontrado{}
	for _, e := range barridos {
		porDireccion[e.Conexion] = e
	}
	for _, ip := range contestaron {
		if _, ya := porDireccion[ip+puertoHTTPHasar]; ya {
			continue
		}
		// Contestó la difusión: es una Hasar aunque después no diga quién es.
		e, _ := identificarHasar(ctx, ip)
		porDireccion[e.Conexion] = e
	}

	encontrados := make([]Encontrado, 0, len(porDireccion))
	for _, e := range porDireccion {
		encontrados = append(encontrados, e)
	}
	sort.Slice(encontrados, func(i, j int) bool {
		a, b := net.ParseIP(hostDe(encontrados[i].Conexion)), net.ParseIP(hostDe(encontrados[j].Conexion))
		if a != nil && b != nil {
			return bytes.Compare(a.To16(), b.To16()) < 0
		}
		return encontrados[i].Conexion < encontrados[j].Conexion
	})
	return encontrados
}

func hostDe(conexion string) string {
	if host, _, err := net.SplitHostPort(conexion); err == nil {
		return host
	}
	return conexion
}

// difundir pregunta por difusión, como el buscador de Hasar, y devuelve las
// direcciones de los que contestaron.
func difundir(ctx context.Context) []string {
	destinos := destinosDeDifusion()
	if len(destinos) == 0 {
		return nil
	}
	conexion, err := net.ListenUDP("udp4", nil)
	if err != nil {
		return nil
	}
	defer conexion.Close()

	vistos := map[string]bool{}
	buf := make([]byte, 64)
	for ronda := 0; ronda < rondasDeDifusion && ctx.Err() == nil; ronda++ {
		for _, ip := range destinos {
			// Lo que no sale por una interfaz (sin ruta, sin permiso) sale por otra.
			_, _ = conexion.WriteToUDP(pedidoDescubrirHasar, &net.UDPAddr{IP: ip, Port: puertoDescubrirHasar})
		}
		_ = conexion.SetReadDeadline(time.Now().Add(esperaDeDifusion))
		for {
			n, quien, err := conexion.ReadFromUDP(buf)
			if err != nil {
				break // se acabó la espera de esta ronda
			}
			if n >= 1 && buf[0] == pedidoDescubrirHasar[0] && quien != nil && quien.IP.To4() != nil {
				vistos[quien.IP.To4().String()] = true
			}
		}
	}
	direcciones := make([]string, 0, len(vistos))
	for ip := range vistos {
		direcciones = append(direcciones, ip)
	}
	sort.Strings(direcciones)
	return direcciones
}

// barrer recorre los vecinos de la red y se queda con los que contestan la
// consulta de versión de una Hasar con la clave de fábrica.
func barrer(ctx context.Context) []Encontrado {
	vecinos := vecinosDeRed()
	if len(vecinos) == 0 {
		return nil
	}
	puerto := puertoHTTPHasar
	if puerto == "" {
		puerto = ":80"
	}
	var (
		candado     sync.Mutex
		encontrados []Encontrado
		espera      sync.WaitGroup
	)
	trabajos := make(chan string)
	for i := 0; i < obrerosDeBarrido; i++ {
		espera.Add(1)
		go func() {
			defer espera.Done()
			marcador := net.Dialer{Timeout: plazoDeBarrido}
			for ip := range trabajos {
				// Primero si hay alguien atendiendo: casi ningún vecino lo hace,
				// y así no se le espera una respuesta HTTP a cada uno.
				conexion, err := marcador.DialContext(ctx, "tcp4", ip+puerto)
				if err != nil {
					continue
				}
				conexion.Close()
				if e, es := identificarHasar(ctx, ip); es {
					candado.Lock()
					encontrados = append(encontrados, e)
					candado.Unlock()
				}
			}
		}()
	}
reparto:
	for _, ip := range vecinos {
		select {
		case trabajos <- ip:
		case <-ctx.Done():
			break reparto
		}
	}
	close(trabajos)
	espera.Wait()
	return encontrados
}

// identificarHasar le pregunta a esa dirección quién es, con la clave de
// fábrica. esHasar dice si contestó como una Hasar; lo encontrado viene
// siempre armado, con el aviso de por qué no se sabe más cuando no contestó.
func identificarHasar(ctx context.Context, ip string) (encontrado Encontrado, esHasar bool) {
	encontrado = Encontrado{Marca: MarcaHasar, Conexion: ip + puertoHTTPHasar}
	h, err := nuevoHasar(Controlador{Marca: MarcaHasar, Conexion: encontrado.Conexion})
	if err != nil {
		encontrado.Aviso = err.Error()
		return encontrado, false
	}
	defer h.hablar.soltar()
	ctx, cancelar := context.WithTimeout(ctx, plazoDeIdentificar)
	defer cancelar()

	equipo, err := h.modelo(ctx)
	if err != nil {
		var clave *claveRechazada
		if errors.As(err, &clave) {
			encontrado.Aviso = "Tiene una clave de acceso distinta de la de fábrica: escribila para poder probar la conexión."
		} else {
			encontrado.Aviso = "Contestó a la búsqueda pero no dijo quién es: " + err.Error()
		}
		return encontrado, false
	}
	// Un equipo sin inicializar no tiene contribuyente que informar: con el
	// modelo alcanza para saber que es una Hasar y dónde está.
	_ = h.inicializacion(ctx, &equipo)
	encontrado.Equipo = &equipo
	return encontrado, true
}

// difusionesLocales: la difusión general y la de cada red de local a la que
// está conectada esta computadora. La general sale por una sola interfaz; con
// cable y Wi-Fi a la vez, la de cada red llega a las dos.
func difusionesLocales() []net.IP {
	destinos := []net.IP{net.IPv4bcast}
	for _, red := range redesLocales() {
		if d := difusionDe(red.IP, red.Mask); d != nil {
			destinos = append(destinos, d)
		}
	}
	return destinos
}

// vecinosLocales: las direcciones de las redes de local a las que está
// conectada esta computadora, sin repetir.
func vecinosLocales() []string {
	var vecinos []string
	vistos := map[string]bool{}
	for _, red := range redesLocales() {
		for _, v := range vecinosDe(red.IP, red.Mask) {
			if !vistos[v] && len(vecinos) < topeDeVecinos {
				vistos[v] = true
				vecinos = append(vecinos, v)
			}
		}
	}
	return vecinos
}

// redesLocales: las redes IPv4 privadas de las interfaces que están andando.
func redesLocales() []*net.IPNet {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var redes []*net.IPNet
	for _, i := range interfaces {
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 || i.Flags&net.FlagPointToPoint != 0 {
			continue
		}
		direcciones, err := i.Addrs()
		if err != nil {
			continue
		}
		for _, d := range direcciones {
			if red, ok := d.(*net.IPNet); ok && esDeLocal(red.IP) {
				redes = append(redes, red)
			}
		}
	}
	return redes
}

// esDeLocal: una dirección IPv4 de red privada (192.168.x, 10.x, 172.16-31.x).
func esDeLocal(ip net.IP) bool {
	ip4 := ip.To4()
	return ip4 != nil && ip4.IsPrivate()
}

// difusionDe es la dirección de difusión de esa red; nil si no es IPv4.
func difusionDe(ip net.IP, mascara net.IPMask) net.IP {
	ip4 := ip.To4()
	if unos, bits := mascara.Size(); ip4 == nil || bits != 32 || unos == 0 {
		return nil
	}
	difusion := make(net.IP, net.IPv4len)
	for i := range difusion {
		difusion[i] = ip4[i] | ^mascara[len(mascara)-net.IPv4len+i]
	}
	return difusion
}

// vecinosDe lista las otras direcciones de la red de ip. De una red más
// grande que una /24 se recorre sólo el tramo de esta computadora: es donde
// está lo que se enchufó al lado, y recorrer miles de direcciones no es para
// la red de un local.
func vecinosDe(ip net.IP, mascara net.IPMask) []string {
	ip4 := ip.To4()
	if !esDeLocal(ip) {
		return nil
	}
	unos, bits := mascara.Size()
	if bits != 32 || unos < 24 {
		unos = 24
	}
	if unos > 30 {
		return nil
	}
	base := ip4.Mask(net.CIDRMask(unos, 32))
	inicio := uint32(base[0])<<24 | uint32(base[1])<<16 | uint32(base[2])<<8 | uint32(base[3])
	cuantas := uint32(1) << (32 - unos)
	vecinos := make([]string, 0, cuantas)
	for i := uint32(1); i < cuantas-1; i++ {
		n := inicio + i
		vecino := net.IPv4(byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
		if !vecino.Equal(ip4) {
			vecinos = append(vecinos, vecino.String())
		}
	}
	return vecinos
}
