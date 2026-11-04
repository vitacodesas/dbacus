package model

import "time"

// DTOs de lectura del monitoreo (spec 22, T16.5): detalle de un digest y comparación de periodos.

// Umbrales de regresión (spec 22 ⚑D14): latencia media ↑ ≥ 25 % con ≥ 100 ejecuciones en ambos periodos.
const (
	RegressionMinPct   = 25.0
	RegressionMinExecs = 100
)

// MonitorMaxPoints es el tope de puntos por serie (spec 22 ⚑D11).
const MonitorMaxPoints = 300

// TimeRange es un intervalo [From, To).
type TimeRange struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

// DigestPoint es un punto de la serie propia de un digest.
type DigestPoint struct {
	T       time.Time `json:"t"`
	Execs   uint64    `json:"execs"`
	TotalMs float64   `json:"totalMs"`
	AvgMs   float64   `json:"avgMs"`
}

// DigestDetail es el detalle de un digest en un rango: totales, serie de ejecuciones/media y huecos del muestreo.
// Schema va vacío si el mismo digest aparece en varias bases (Schemas las lista todas).
type DigestDetail struct {
	Digest  string        `json:"digest"`
	Text    string        `json:"text"`
	Schemas []string      `json:"schemas"`
	Stat    DigestStat    `json:"stat"`
	Points  []DigestPoint `json:"points"`
	Gaps    []TimeGap     `json:"gaps"`
}

// Comparison es el resultado de comparar un periodo base (A) con uno actual (B): consultas que empeoraron,
// aparecidas en B y desaparecidas de B. Las de Regressions y New traen Prev (el valor en A), ChangePct y Regression.
type Comparison struct {
	Regressions []DigestStat `json:"regressions"`
	New         []DigestStat `json:"new"`
	Gone        []DigestStat `json:"gone"`
}
