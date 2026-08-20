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
