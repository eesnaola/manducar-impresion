package main

import (
	"encoding/binary"
	"strings"
	"testing"

	"github.com/eesnaola/manducar-impresion/internal/config"
)

// Abrir el programa de un doble clic es lo que no se puede preguntar: se
// deduce de que no haya terminal del otro lado y de que no le hayan pasado
// ningún comando.
func TestAbiertoDeDobleClic(t *testing.T) {
	casos := []struct {
		nombre   string
		comando  string
		terminal bool
		sistema  string
		quiero   bool
	}{
		{"doble clic en Windows", "", false, "windows", true},
		{"doble clic en Mac", "", false, "darwin", true},
		{"el asistente sin terminal", "asistente", false, "windows", true},
		{"desde una terminal", "", true, "windows", false},
		{"el asistente desde una terminal", "asistente", true, "darwin", false},
		{"con un comando de verdad", "vincular", false, "windows", false},
		{"con un comando y una terminal", "estado", true, "darwin", false},
		// En Linux se abre desde la terminal: no hay cuadros ni hacen falta.
		{"en Linux", "", false, "linux", false},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			if got := abiertoDeDobleClic(c.comando, c.terminal, c.sistema); got != c.quiero {
				t.Fatalf("dio %v y tenía que dar %v", got, c.quiero)
			}
		})
	}
}

// Los renglones sueltos de la terminal, adentro de una ventana: el aire de
// arriba y el de abajo no son parte del mensaje.
func TestParrafo(t *testing.T) {
	casos := []struct {
		nombre string
		lineas []string
		quiero string
	}{
		{"uno solo", []string{"Listo."}, "Listo."},
		{"el vacío de adelante no va", []string{"", "Listo."}, "Listo."},
		{"ni el de atrás", []string{"Listo.", "", ""}, "Listo."},
		{"uno en el medio separa", []string{"Falló.", "", "¿Probamos de nuevo?"}, "Falló.\n\n¿Probamos de nuevo?"},
		{"dos seguidos son uno", []string{"Falló.", "", "", "¿Probamos?"}, "Falló.\n\n¿Probamos?"},
		{"nada es nada", []string{"", ""}, ""},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			if got := parrafo(c.lineas); got != c.quiero {
				t.Fatalf("dio %q y quería %q", got, c.quiero)
			}
		})
	}
}

// ————— Windows: el .vbs que muestra el cuadro —————

func TestVbsPreguntarDejaLaRespuestaEnElArchivoQueLePasan(t *testing.T) {
	got := vbsPreguntar("Manducar — impresión", "Pegá el código que muestra el panel:")

	for _, quiero := range []string{
		`salida = WScript.Arguments(0)`,
		`r = InputBox("Pegá el código que muestra el panel:", "Manducar — impresión")`,
		`Set f = fso.CreateTextFile(salida, True, True)`,
		"f.Write r",
		"f.Close",
	} {
		if !contains(got, quiero) {
			t.Errorf("el .vbs quedó así:\n%s\ny tenía que tener:\n%s", got, quiero)
		}
	}
	if !contains(got, "\r\n") {
		t.Error("es un archivo de Windows: los renglones van con CRLF")
	}
	if !strings.HasPrefix(got, "'") {
		t.Errorf("arranca con el comentario que dice quién lo escribió: %q", got)
	}
	for _, l := range strings.Split(strings.TrimRight(got, "\r\n"), "\r\n") {
		if l == "" {
			t.Fatal("no puede quedar un renglón vacío en el medio")
		}
	}
}

func TestVbsConfirmarPreguntaSiONo(t *testing.T) {
	got := vbsConfirmar("Manducar", "¿Probamos de nuevo?")
	// 36 son los dos botones (4) más el signo de pregunta (32).
	if !contains(got, `r = MsgBox("¿Probamos de nuevo?", 36, "Manducar")`) {
		t.Fatalf("quedó:\n%s", got)
	}
	// El número que devuelve MsgBox es lo que se lee después: 6 es «Sí».
	if !contains(got, "f.Write CStr(r)") {
		t.Fatalf("no deja la respuesta en el archivo:\n%s", got)
	}
}

func TestVbsMensajeCambiaElIconoYNoEscribeNada(t *testing.T) {
	info := vbsMensaje("Manducar", "Listo.", vbInformation)
	if !contains(info, `MsgBox "Listo.", 64, "Manducar"`) {
		t.Fatalf("el aviso quedó:\n%s", info)
	}
	if contains(info, "WScript.Arguments") || contains(info, "CreateTextFile") {
		t.Fatalf("no hay nada que leer después de un aviso:\n%s", info)
	}
	if malo := vbsMensaje("Manducar", "No se pudo.", vbCritical); !contains(malo, `, 16, `) {
		t.Fatalf("el error va con el ícono de error:\n%s", malo)
	}
}

// La comilla se escapa duplicándola —eso lo sabe el lanzador, y es el mismo
// escapado— y el salto de línea no entra adentro de un literal: va como
// vbCrLf entre dos.
func TestVbsTexto(t *testing.T) {
	casos := map[string]string{
		"simple":         `"simple"`,
		`con "algo"`:     `"con ""algo"""`,
		"dos\nrenglones": `"dos" & vbCrLf & "renglones"`,
		"con\r\nCRLF":    `"con" & vbCrLf & "CRLF"`,
		"uno\n\nsaltado": `"uno" & vbCrLf & "" & vbCrLf & "saltado"`,
		`C:\ruta\a.x`:    `"C:\ruta\a.x"`,
		"":               `""`,
	}
	for in, quiero := range casos {
		if got := vbsTexto(in); got != quiero {
			t.Errorf("vbsTexto(%q) = %q, quería %q", in, got, quiero)
		}
	}
}

// El script va en UTF-16 con BOM: si fuera UTF-8, wscript lo leería con la
// codificación vieja de esa computadora y «Pegá el código» aparecería roto.
func TestUTF16LEVaConBOMYVuelveIgual(t *testing.T) {
	b := utf16LE("Pegá el código «acá»")
	if len(b) < 2 || b[0] != 0xFF || b[1] != 0xFE {
		t.Fatalf("le falta el BOM: %x", b)
	}
	if got := textoDelArchivo(b); got != "Pegá el código «acá»" {
		t.Fatalf("volvió %q", got)
	}
	// Y el orden es el de Windows: la «P» va con el byte bajo primero.
	if binary.LittleEndian.Uint16(b[2:]) != uint16('P') {
		t.Fatalf("no quedó en little endian: %x", b[2:6])
	}
}

func TestTextoDelArchivo(t *testing.T) {
	casos := []struct {
		nombre string
		bytes  []byte
		quiero string
	}{
		{"lo que escribe el .vbs", utf16LE("42862135"), "42862135"},
		{"con el fin de renglón pegado", utf16LE("42862135\r\n"), "42862135"},
		{"con acentos", utf16LE("Pizzería"), "Pizzería"},
		{"cancelar deja el archivo vacío", utf16LE(""), ""},
		{"un archivo que ni se escribió", nil, ""},
		{"en UTF-8 con BOM", append([]byte{0xEF, 0xBB, 0xBF}, []byte("42862135\r\n")...), "42862135"},
		{"pelado", []byte("42862135\r\n"), "42862135"},
		{"el número que devuelve MsgBox", utf16LE("6"), "6"},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			if got := textoDelArchivo(c.bytes); got != c.quiero {
				t.Fatalf("dio %q y quería %q", got, c.quiero)
			}
		})
	}
}

// ————— Mac: el AppleScript —————

func TestOsaTextoEscapaLoQueRompeElLiteral(t *testing.T) {
	casos := map[string]string{
		"simple":           `"simple"`,
		`con "algo"`:       `"con \"algo\""`,
		`una\contrabarra`:  `"una\\contrabarra"`,
		"dos\nrenglones":   `"dos\nrenglones"`,
		"con\r\nCRLF":      `"con\nCRLF"`,
		"con acentos á «»": `"con acentos á «»"`,
		"":                 `""`,
	}
	for in, quiero := range casos {
		if got := osaTexto(in); got != quiero {
			t.Errorf("osaTexto(%q) = %q, quería %q", in, got, quiero)
		}
	}
}

func TestOsaPreguntarPideElDatoYSeDejaCancelar(t *testing.T) {
	got := osaPreguntar("Manducar — impresión", "Pegá el código:", "note")
	for _, quiero := range []string{
		"activate\n",
		`display dialog "Pegá el código:"`,
		`default answer ""`,
		`with title "Manducar — impresión"`,
		`buttons {"Cancelar", "Aceptar"}`,
		// Sin esto, un botón que no se llame «Cancel» en inglés es un botón
		// más y la tecla Esc no hace nada.
		`cancel button "Cancelar"`,
		`default button "Aceptar"`,
	} {
		if !contains(got, quiero) {
			t.Errorf("el script quedó:\n%s\ny tenía que tener: %s", got, quiero)
		}
	}
}

func TestOsaConfirmarYMensaje(t *testing.T) {
	si := osaConfirmar("Manducar", "¿Probamos de nuevo?", "note")
	if !contains(si, `buttons {"No", "Sí"}`) || !contains(si, `default button "Sí"`) {
		t.Errorf("el sí o no quedó:\n%s", si)
	}
	if contains(si, "cancel button") {
		t.Errorf("cerrar la pregunta no es decir que no:\n%s", si)
	}
	aviso := osaMensaje("Manducar", "Listo.", "note")
	if !contains(aviso, `buttons {"Aceptar"} default button 1 with icon note`) {
		t.Errorf("el aviso quedó:\n%s", aviso)
	}
	if conPlaca := osaMensaje("Manducar", "Listo.", "/Applications/Manducar Impresión.app/Contents/Resources/manducar.icns"); !contains(conPlaca, `with icon POSIX file "/Applications/Manducar Impresi`) {
		t.Errorf("el .icns propio va como POSIX file: %q", conPlaca)
	}
	if sinIcono := osaMensaje("Manducar", "Listo.", ""); contains(sinIcono, "with icon") {
		t.Errorf("sin ícono no va la cláusula: %q", sinIcono)
	}
	if malo := osaMensaje("Manducar", "No se pudo.", "stop"); !contains(malo, "with icon stop") {
		t.Errorf("el error va con el ícono de error:\n%s", malo)
	}
}

// Lo que contesta osascript, tal cual sale.
func TestLoQueDevuelveOsascript(t *testing.T) {
	const conTexto = "button returned:Aceptar, text returned:4286 2135\n"
	if got, ok := textoDevuelto(conTexto); !ok || got != "4286 2135" {
		t.Errorf("el texto: %q, %v", got, ok)
	}
	if got := botonDevuelto(conTexto); got != "Aceptar" {
		t.Errorf("el botón: %q", got)
	}
	if got := botonDevuelto("button returned:Sí\n"); got != botonSi {
		t.Errorf("el sí: %q", got)
	}
	// El campo vacío no es lo mismo que no haber contestado: contestó, y
	// contestó nada.
	if got, ok := textoDevuelto("button returned:Aceptar, text returned:\n"); !ok || got != "" {
		t.Errorf("aceptar sin escribir: %q, %v", got, ok)
	}
	if _, ok := textoDevuelto("button returned:Aceptar\n"); ok {
		t.Error("sin «text returned» no hay texto")
	}
}

func TestEsCancelacion(t *testing.T) {
	if !esCancelacion("35:59: execution error: User canceled. (-128)") {
		t.Error("el -128 es la persona cancelando")
	}
	if !esCancelacion("execution error: Se canceló la acción. (-128)") {
		t.Error("el número no se traduce; el texto puede")
	}
	if esCancelacion("execution error: No se pudo mostrar el cuadro. (-1708)") {
		t.Error("otro error no es una cancelación")
	}
	if esCancelacion("") {
		t.Error("sin error no hay cancelación")
	}
}

func TestMensajeDeOsascriptQuedaEnCastellanoYSinElNumero(t *testing.T) {
	got := mensajeDeOsascript("35:59: execution error: No se pudo mostrar el cuadro. (-1708)\n")
	if got != "No se pudo mostrar el cuadro." {
		t.Fatalf("quedó %q", got)
	}
	if got := mensajeDeOsascript("  algo suelto  "); got != "algo suelto" {
		t.Fatalf("quedó %q", got)
	}
}

// ————— Los pasos son los mismos por donde sea que se hable —————

// uiDeMentira contesta lo que se le escribió de antemano y anota por dónde
// pasó el asistente. Es la ui que no depende de si hay una terminal, una
// ventana o nada.
type uiDeMentira struct {
	respuestas []string // lo que contesta cada vez que le preguntan
	reintentos []bool   // lo que contesta cada vez que algo falla
	opcion     string   // qué elige con la computadora ya vinculada

	pasos   []string
	avisos  []string
	fallas  []string
	elLocal string
}

func (u *uiDeMentira) decir(...string) {}

func (u *uiDeMentira) avisar(lineas ...string) {
	u.pasos = append(u.pasos, "avisar")
	u.avisos = append(u.avisos, parrafo(lineas))
}

func (u *uiDeMentira) fallar(lineas ...string) {
	u.pasos = append(u.pasos, "fallar")
	u.fallas = append(u.fallas, parrafo(lineas))
}

func (u *uiDeMentira) preguntar(string) (string, bool) {
	u.pasos = append(u.pasos, "preguntar")
	if len(u.respuestas) == 0 {
		return "", false
	}
	r := u.respuestas[0]
	u.respuestas = u.respuestas[1:]
	return r, true
}

func (u *uiDeMentira) reintentar([]string) bool {
	u.pasos = append(u.pasos, "reintentar")
	if len(u.reintentos) == 0 {
		return false
	}
	r := u.reintentos[0]
	u.reintentos = u.reintentos[1:]
	return r
}

func (u *uiDeMentira) elegir(estadoInfo) string {
	u.pasos = append(u.pasos, "elegir")
	return u.opcion
}

func (u *uiDeMentira) listo(local, _ string) {
	u.pasos = append(u.pasos, "listo")
	u.elLocal = local
}

func (u *uiDeMentira) cerrar() { u.pasos = append(u.pasos, "cerrar") }

// El camino de todos los días, sin importar por dónde se hable: se pregunta el
// código una vez, se vincula, se instala y se cuenta cómo quedó.
func TestLosPasosSonLosMismosSeaPorDondeSeaQueSeHable(t *testing.T) {
	preparar(t)
	var pedido string
	srv := servidorDeMentira(t, func(code, _ string) { pedido = code })
	instalado := false
	installAgent = func() error { instalado = true; return nil }

	u := &uiDeMentira{respuestas: []string{"4286 2135"}}
	if code := asistirCon(u, srv.URL); code != 0 {
		t.Fatalf("salió con %d: %v", code, u.fallas)
	}
	if pedido != "42862135" {
		t.Errorf("al servidor le mandó %q", pedido)
	}
	if !instalado {
		t.Error("no lo dejó instalado")
	}
	if u.elLocal != "Pizzería Demo" {
		t.Errorf("no contó a qué local quedó vinculada: %q", u.elLocal)
	}
	if got := strings.Join(u.pasos, ","); got != "preguntar,listo,cerrar" {
		t.Errorf("los pasos fueron: %s", got)
	}
}

// El código mal escrito se cuenta y se vuelve a preguntar; a la tercera se
// deja de preguntar diciendo dónde está.
func TestElCodigoMalEscritoSeVuelveAPedirHastaTresVeces(t *testing.T) {
	preparar(t)
	llamaron := false
	srv := servidorDeMentira(t, func(string, string) { llamaron = true })

	u := &uiDeMentira{respuestas: []string{"uno", "dos", "tres"}}
	if code := asistirCon(u, srv.URL); code != 1 {
		t.Fatalf("salió con %d y tenía que salir con 1", code)
	}
	if llamaron {
		t.Error("no tenía que hablar con el servidor")
	}
	if got := strings.Join(u.pasos, ","); got != "preguntar,avisar,preguntar,avisar,preguntar,avisar,cerrar" {
		t.Errorf("los pasos fueron: %s", got)
	}
	if ultimo := u.avisos[len(u.avisos)-1]; !contains(ultimo, "Vincular una computadora") {
		t.Errorf("no dijo dónde está el código: %q", ultimo)
	}
}

// Con el servidor caído se cuenta por qué y se ofrece otra vuelta; el que dice
// que no se va con código de error.
func TestElErrorDeRedSeCuentaYSeOfreceOtraVuelta(t *testing.T) {
	preparar(t)
	srv := servidorDeMentira(t, nil)
	dir := srv.URL
	srv.Close() // el puerto queda cerrado: es lo que se ve sin internet

	u := &uiDeMentira{respuestas: []string{"42862135"}}
	if code := asistirCon(u, dir); code != 1 {
		t.Fatalf("salió con %d y tenía que salir con 1", code)
	}
	if got := strings.Join(u.pasos, ","); got != "preguntar,reintentar,cerrar" {
		t.Errorf("los pasos fueron: %s", got)
	}
}

// ————— Y por los cuadros de diálogo, en particular —————

// dialogosDeMentira es el sistema: contesta lo que se le escribió y anota cada
// ventana que se abrió, que es lo que hay que contar —una de más es una que
// alguien tiene que cerrar—.
type dialogosDeMentira struct {
	respuestas []string
	siONo      []bool
	ventanas   []string
}

func (d *dialogosDeMentira) Ask(_, pregunta string) (string, bool, error) {
	d.ventanas = append(d.ventanas, "preguntar: "+pregunta)
	if len(d.respuestas) == 0 {
		return "", false, nil // canceló
	}
	r := d.respuestas[0]
	d.respuestas = d.respuestas[1:]
	return r, r != "", nil
}

func (d *dialogosDeMentira) Info(_, texto string) error {
	d.ventanas = append(d.ventanas, "aviso: "+texto)
	return nil
}

func (d *dialogosDeMentira) Error(_, texto string) error {
	d.ventanas = append(d.ventanas, "error: "+texto)
	return nil
}

func (d *dialogosDeMentira) Confirm(_, texto string) (bool, error) {
	d.ventanas = append(d.ventanas, "pregunta: "+texto)
	if len(d.siONo) == 0 {
		return false, nil
	}
	r := d.siONo[0]
	d.siONo = d.siONo[1:]
	return r, nil
}

func (d *dialogosDeMentira) todo() string { return strings.Join(d.ventanas, "\n———\n") }

// De un doble clic: una ventana pregunta el código y otra cuenta que quedó.
// Ninguna más: la narración que en la terminal son renglones, acá no existe.
func TestEnCuadrosSeVinculaConDosVentanasYNadaMas(t *testing.T) {
	preparar(t)
	var pedido string
	srv := servidorDeMentira(t, func(code, _ string) { pedido = code })
	instalado := false
	installAgent = func() error { instalado = true; return nil }

	d := &dialogosDeMentira{respuestas: []string{"4286 2135"}}
	if code := asistirCon(&cuadros{d: d}, srv.URL); code != 0 {
		t.Fatalf("salió con %d:\n%s", code, d.todo())
	}
	if pedido != "42862135" || !instalado {
		t.Fatalf("mandó %q, instalado=%v", pedido, instalado)
	}
	if len(d.ventanas) != 2 {
		t.Fatalf("abrió %d ventanas y tenían que ser dos:\n%s", len(d.ventanas), d.todo())
	}
	if !contains(d.ventanas[0], "Pegá el código") {
		t.Errorf("la primera ventana pide el código: %q", d.ventanas[0])
	}
	for _, dice := range []string{
		"Listo: «Pizzería Demo» quedó vinculada",
		"va a arrancar solo cada vez que entrés a esta computadora",
		"Ya podés cerrar esto.",
	} {
		if !contains(d.ventanas[1], dice) {
			t.Errorf("la última ventana no dice %q:\n%s", dice, d.ventanas[1])
		}
	}
}

// Cancelar el cuadro es irse: no se vincula nada y se sale con error.
func TestEnCuadrosCancelarEsIrse(t *testing.T) {
	preparar(t)
	llamaron := false
	srv := servidorDeMentira(t, func(string, string) { llamaron = true })

	d := &dialogosDeMentira{} // sin respuestas: cancela en la primera
	if code := asistirCon(&cuadros{d: d}, srv.URL); code != 1 {
		t.Fatalf("salió con %d y tenía que salir con 1:\n%s", code, d.todo())
	}
	if llamaron {
		t.Error("no tenía que hablar con el servidor")
	}
	if len(d.ventanas) != 1 {
		t.Fatalf("no se le insiste al que cerró el cuadro:\n%s", d.todo())
	}
}

// El código mal escrito abre un aviso y vuelve a preguntar.
func TestEnCuadrosElCodigoMalEscritoSeAvisaYSeVuelveAPreguntar(t *testing.T) {
	preparar(t)
	srv := servidorDeMentira(t, nil)

	d := &dialogosDeMentira{respuestas: []string{"pizzeria", "4286 2135"}}
	if code := asistirCon(&cuadros{d: d}, srv.URL); code != 0 {
		t.Fatalf("salió con %d:\n%s", code, d.todo())
	}
	if len(d.ventanas) != 4 {
		t.Fatalf("tenían que ser cuatro ventanas —pregunta, aviso, pregunta, listo—:\n%s", d.todo())
	}
	if !contains(d.ventanas[1], "no es un código de ocho números") {
		t.Errorf("el aviso quedó: %q", d.ventanas[1])
	}
}

// Con la computadora ya vinculada, un cuadro no puede hacer un menú de cuatro
// opciones: pregunta la única que importa.
func TestEnCuadrosYaVinculadaPreguntaSiVolverAVincular(t *testing.T) {
	p := preparar(t)
	if err := config.SaveTo(p, config.Config{
		Server: "https://pizzeria.manduc.ar", Token: "tok", AgentID: 7,
		Store: "Pizzería Demo", Mode: config.ModeUser,
	}); err != nil {
		t.Fatal(err)
	}

	// El que dice que no se va sabiendo cómo quedó todo.
	d := &dialogosDeMentira{siONo: []bool{false}}
	if code := asistirCon(&cuadros{d: d}, defaultServer); code != 0 {
		t.Fatalf("salió con %d:\n%s", code, d.todo())
	}
	if len(d.ventanas) != 2 {
		t.Fatalf("una pregunta y el estado:\n%s", d.todo())
	}
	for _, dice := range []string{
		"Esta computadora ya está vinculada a «Pizzería Demo»",
		"¿Querés volver a vincularla con otro código?",
	} {
		if !contains(d.ventanas[0], dice) {
			t.Errorf("la pregunta no dice %q:\n%s", dice, d.ventanas[0])
		}
	}
	for _, dice := range []string{"Pizzería Demo", "https://pizzeria.manduc.ar", p, "Arranca solo:"} {
		if !contains(d.ventanas[1], dice) {
			t.Errorf("el estado no dice %q:\n%s", dice, d.ventanas[1])
		}
	}
}

// Y el que dice que sí vuelve a pegar un código, sin pasar por ningún menú.
func TestEnCuadrosYaVinculadaElQueDiceQueSiVuelveAVincular(t *testing.T) {
	p := preparar(t)
	if err := config.SaveTo(p, config.Config{
		Server: "https://vieja.manduc.ar", Token: "tok", AgentID: 3,
		Store: "La de antes", Mode: config.ModeUser,
	}); err != nil {
		t.Fatal(err)
	}
	var pedido string
	srv := servidorDeMentira(t, func(code, _ string) { pedido = code })

	d := &dialogosDeMentira{siONo: []bool{true}, respuestas: []string{"4286-2135"}}
	if code := asistirCon(&cuadros{d: d}, srv.URL); code != 0 {
		t.Fatalf("salió con %d:\n%s", code, d.todo())
	}
	if pedido != "42862135" {
		t.Fatalf("al servidor le mandó %q", pedido)
	}
	if !contains(d.ventanas[len(d.ventanas)-1], "«Pizzería Demo» quedó vinculada") {
		t.Errorf("la última ventana quedó:\n%s", d.ventanas[len(d.ventanas)-1])
	}
	cfg, err := config.LoadFrom(p)
	if err != nil || cfg.Store != "Pizzería Demo" {
		t.Fatalf("quedó guardado %+v (%v)", cfg, err)
	}
}

// Lo que sale mal se muestra con el ícono de error y no con el de «listo»; y
// la narración —la que en la terminal son renglones al pasar— no abre nada.
func TestEnCuadrosCadaCosaVaConSuIcono(t *testing.T) {
	d := &dialogosDeMentira{}
	c := &cuadros{d: d}
	c.decir("Vinculando…", "esto en una ventana no va")
	c.avisar("", "Listo.", "")
	c.fallar("No se pudo vincular.")
	// Un aviso sin nada adentro tampoco es una ventana.
	c.avisar("", "")

	quiero := []string{"aviso: Listo.", "error: No se pudo vincular."}
	if len(d.ventanas) != len(quiero) {
		t.Fatalf("abrió %d ventanas y tenían que ser %d:\n%s", len(d.ventanas), len(quiero), d.todo())
	}
	for i, v := range quiero {
		if d.ventanas[i] != v {
			t.Errorf("la ventana %d quedó %q y tenía que ser %q", i+1, d.ventanas[i], v)
		}
	}
}
