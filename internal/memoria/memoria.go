// Package memoria mide la memoria de la computadora y lo más que llegó a usar
// un proceso. La usa el vigía cuando el agente termina mal: con eso la caída
// dice si a la computadora se le acabó la memoria o si el agente la estaba
// gastando. En ELCARACOL (15/10/2026) el agente se cayó con «out of memory»
// usando pocos MB, y saberlo pidió leer el volcado entero.
//
// Mide sólo en Windows, que es donde hay vigía; en el resto la medida queda
// vacía.
package memoria

// Medida es cómo estaba la memoria cuando el agente terminó, en MB. Cero es
// que no se pudo medir.
type Medida struct {
	// Lo que la computadora tenía prometido a los programas (RAM más archivo
	// de paginación) y su tope. En el tope Windows ya no le da memoria a
	// nadie: es el «errno=1455» del volcado.
	ComprometidaMB, LimiteMB uint64
	RAMMB, RAMLibreMB        uint64
	// Lo más que llegó a comprometer el agente en toda su vida.
	AgenteMB uint64
}

// Vacia: no se pudo medir nada.
func (m Medida) Vacia() bool { return m == Medida{} }

// enMB pasa bytes a MB redondeando para arriba: lo poco que se midió no queda
// en cero, que es «no se supo».
func enMB(bytes uint64) uint64 { return (bytes + 1<<20 - 1) >> 20 }
