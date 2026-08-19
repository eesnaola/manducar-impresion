package main

import "testing"

// El panel imprime «vincular 123456 --servidor URL»: el código va primero y
// las opciones después. flag.Parse solo no lo banca.
func TestParsePairArgsTakesTheCodeAndTheFlagsInAnyOrder(t *testing.T) {
	casos := []struct {
		nombre string
		args   []string
		name   string
	}{
		{"código y después la opción", []string{"123456", "--servidor", "https://pizzeria.manduc.ar"}, ""},
		{"la opción y después el código", []string{"--servidor", "https://pizzeria.manduc.ar", "123456"}, ""},
		{"con un solo guión", []string{"123456", "-servidor", "https://pizzeria.manduc.ar"}, ""},
		{"con nombre en el medio", []string{"--nombre", "Caja", "123456", "--servidor", "https://pizzeria.manduc.ar"}, "Caja"},
		{"con la opción pegada", []string{"123456", "--servidor=https://pizzeria.manduc.ar"}, ""},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			code, server, name, err := parsePairArgs(c.args)
			if err != nil {
				t.Fatalf("%v: %v", c.args, err)
			}
			if code != "123456" {
				t.Errorf("código: %q", code)
			}
			if server != "https://pizzeria.manduc.ar" {
				t.Errorf("servidor: %q", server)
			}
			if c.name != "" && name != c.name {
				t.Errorf("nombre: %q", name)
			}
			if name == "" {
				t.Error("el nombre no puede quedar vacío: tiene el de la computadora por default")
			}
		})
	}
}

func TestParsePairArgsComplainsAboutWhatFalta(t *testing.T) {
	casos := []struct {
		nombre string
		args   []string
		dice   string
	}{
		{"sin código", []string{"--servidor", "https://x"}, "falta el código"},
		{"sin servidor", []string{"123456"}, "falta --servidor"},
		{"sin nada", nil, "falta el código"},
		{"dos códigos", []string{"123456", "654321", "--servidor", "https://x"}, "sobra «654321»"},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			_, _, _, err := parsePairArgs(c.args)
			if err == nil {
				t.Fatal("tenía que quejarse")
			}
			if !contains(err.Error(), c.dice) {
				t.Fatalf("dijo %q y tenía que decir %q", err, c.dice)
			}
		})
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// El token se manda a lo que diga --servidor: si eso no es una URL con
// https, mejor plantarse antes que después.
func TestParsePairArgsChecksTheServer(t *testing.T) {
	casos := []struct {
		nombre string
		url    string
		dice   string
	}{
		{"sin esquema", "pizzeria.manduc.ar", "con https:// adelante"},
		{"esquema raro", "ftp://pizzeria.manduc.ar", "tiene que empezar con https://"},
		{"http a la calle", "http://pizzeria.manduc.ar", "viajan en claro"},
		{"sin dominio", "https://", "con https:// adelante"},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			_, _, _, err := parsePairArgs([]string{"123456", "--servidor", c.url})
			if err == nil {
				t.Fatalf("«%s» tenía que rechazarse", c.url)
			}
			if !contains(err.Error(), c.dice) {
				t.Fatalf("dijo %q y tenía que decir %q", err, c.dice)
			}
		})
	}
}

// En la máquina de uno, http alcanza: es donde se prueba.
func TestParsePairArgsAllowsHttpOnTheLocalMachine(t *testing.T) {
	for _, u := range []string{"http://localhost:8080", "http://pizzeria.manducar.localhost:8080", "http://127.0.0.1:8080"} {
		if _, server, _, err := parsePairArgs([]string{"123456", "--servidor", u}); err != nil || server != u {
			t.Fatalf("«%s»: %q, %v", u, server, err)
		}
	}
}

// La barra del final la agrega cualquiera al copiar del navegador; el cliente
// arma las rutas pegando «/agente/…», así que se la saca acá.
func TestParsePairArgsTrimsTheTrailingSlash(t *testing.T) {
	_, server, _, err := parsePairArgs([]string{"123456", "--servidor", "https://pizzeria.manduc.ar/"})
	if err != nil {
		t.Fatal(err)
	}
	if server != "https://pizzeria.manduc.ar" {
		t.Fatalf("quedó %q", server)
	}
}

// `--sistema` puede venir en cualquier lado de la línea —adelante, en el
// medio, atrás—: se lo saca antes de que cada comando parsee lo suyo, así el
// código de vinculación sigue pudiendo ir suelto.
func TestTakeFlagPullsTheOptionOutFromAnywhere(t *testing.T) {
	casos := []struct {
		nombre string
		args   []string
		quedan []string
	}{
		{"al final", []string{"123456", "--servidor", "https://x", "--sistema"}, []string{"123456", "--servidor", "https://x"}},
		{"adelante", []string{"--sistema", "123456", "--servidor", "https://x"}, []string{"123456", "--servidor", "https://x"}},
		{"en el medio", []string{"123456", "--sistema", "--servidor", "https://x"}, []string{"123456", "--servidor", "https://x"}},
		{"con un solo guión", []string{"-sistema", "123456"}, []string{"123456"}},
		{"repetida", []string{"--sistema", "--sistema", "123456"}, []string{"123456"}},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			quedan, estaba := takeFlag(c.args, "sistema")
			if !estaba {
				t.Fatal("tenía que encontrarla")
			}
			if len(quedan) != len(c.quedan) {
				t.Fatalf("quedó %v y tenía que quedar %v", quedan, c.quedan)
			}
			for i := range quedan {
				if quedan[i] != c.quedan[i] {
					t.Fatalf("quedó %v y tenía que quedar %v", quedan, c.quedan)
				}
			}
			// Y sin la opción, la línea queda igual que como vino.
			if _, estaba := takeFlag(c.quedan, "sistema"); estaba {
				t.Fatal("no estaba y dijo que sí")
			}
		})
	}
}

// Sacar `--sistema` no puede romper la forma en que el panel imprime el
// comando de vinculación.
func TestTakeFlagLeavesThePairingLineUsable(t *testing.T) {
	args, sistema := takeFlag([]string{"123456", "--servidor", "https://pizzeria.manduc.ar", "--sistema"}, "sistema")
	if !sistema {
		t.Fatal("tenía que verla")
	}
	code, server, _, err := parsePairArgs(args)
	if err != nil || code != "123456" || server != "https://pizzeria.manduc.ar" {
		t.Fatalf("%q %q %v", code, server, err)
	}
}

// En Windows sin administrador al agente lo lanza un .vbs: no hay ventana
// donde mirar el log, así que va al archivo. Se puede pedir por la opción o
// por la variable de entorno.
func TestWantsFileLog(t *testing.T) {
	casos := []struct {
		nombre string
		args   []string
		env    string
		quiero bool
	}{
		{"como lo lanza el .vbs", []string{"--log-archivo"}, "", true},
		{"con un solo guión", []string{"-log-archivo"}, "", true},
		{"a mano en una terminal", nil, "", false},
		{"por variable de entorno", nil, "1", true},
		{"apagada a propósito", nil, "0", false},
		{"apagada con palabras", nil, "no", false},
		{"con espacios de más", nil, " 1 ", true},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			if got := wantsFileLog(c.args, c.env); got != c.quiero {
				t.Fatalf("dio %v y tenía que dar %v", got, c.quiero)
			}
		})
	}
}
