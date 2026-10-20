package api

import "encoding/base64"

type Mercure struct {
	URL   string `json:"url"`
	JWT   string `json:"jwt"`
	Topic string `json:"topic"`
}

type PairResponse struct {
	Token   string `json:"token"`
	AgentID int    `json:"agentId"`
	Store   struct {
		Slug string `json:"slug"`
		Name string `json:"name"`
	} `json:"store"`
	Mercure Mercure `json:"mercure"`
}

type Release struct {
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
	URL     string `json:"url"`
}

type HeartbeatResponse struct {
	Mercure          Mercure  `json:"mercure"`
	Agent            *Release `json:"agent"`
	JobsPending      int      `json:"jobsPending"`
	HeartbeatSeconds int      `json:"heartbeatSeconds"`
	// Printers: las impresoras a cargo de esta computadora, para pulsarlas
	// y contarle al servidor cómo están (el semáforo del panel).
	Printers []PrinterRef `json:"printers"`
}

// PrinterRef es una impresora del panel: su id y su target.
type PrinterRef struct {
	ID int `json:"id"`
	Target
}

// PrinterHealth es el resultado del pulso a una impresora.
type PrinterHealth struct {
	ID     int    `json:"id"`
	Health string `json:"health"`
}

type Target struct {
	Kind       string `json:"kind"`       // network | system
	Address    string `json:"address"`    // host:puerto
	SystemName string `json:"systemName"` // nombre en el SO
}

type Job struct {
	ID      int    `json:"id"`
	Kind    string `json:"kind"`
	Printer Target `json:"printer"`
	Payload []byte `json:"-"`
}

// El payload viaja en base64; acá se decodifica una sola vez.
func (j *Job) UnmarshalJSON(b []byte) error {
	type alias Job
	var raw struct {
		alias
		Payload string `json:"payload"`
	}
	if err := jsonUnmarshal(b, &raw); err != nil {
		return err
	}
	data, err := base64.StdEncoding.DecodeString(raw.Payload)
	if err != nil {
		return err
	}
	*j = Job(raw.alias)
	j.Payload = data
	return nil
}

type Result struct {
	OK             bool   `json:"ok"`
	Error          string `json:"error,omitempty"`
	WroteSomething bool   `json:"wroteSomething"`
	// Canceled: el trabajo se fue de la cola del sistema porque alguien lo
	// canceló ahí (no salió ni va a salir).
	Canceled bool `json:"canceled,omitempty"`
}

// CrashReport es la caída que el agente cuenta al volver a arrancar
// (POST /agente/caida). Los topes los hace el que arma el informe
// (internal/caida) y los vuelve a hacer el servidor.
type CrashReport struct {
	Version string `json:"version"`
	Sistema string `json:"sistema"`
	Ocurrio string `json:"ocurrio"` // RFC 3339, UTC
	Codigo  *int   `json:"codigo"`  // nil si el vigía no lo vio
	Resumen string `json:"resumen"`
	Detalle string `json:"detalle"`
	Log     string `json:"log"`
	// Cuántas caídas junta el informe: la última, la del detalle, y las que se
	// anotaron antes sin que un agente llegara a contarlas.
	Caidas int `json:"caidas"`
	// Cómo estaba la memoria cuando terminó; nil si el vigía no la midió (sin
	// vigía, o un vigía de antes de que midiera).
	Memoria *Memoria `json:"memoria,omitempty"`
}

// Memoria es la medida que tomó el vigía al terminar el agente, en MB. Lo que
// no se pudo medir va en cero y no viaja.
type Memoria struct {
	ComprometidaMB uint64 `json:"comprometida_mb,omitempty"`
	LimiteMB       uint64 `json:"limite_mb,omitempty"`
	RAMMB          uint64 `json:"ram_mb,omitempty"`
	RAMLibreMB     uint64 `json:"ram_libre_mb,omitempty"`
	AgenteMB       uint64 `json:"agente_mb,omitempty"`
}
