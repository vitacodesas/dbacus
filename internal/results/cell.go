// Package results guarda los resultados de consultas en memoria (ADR-002) y serializa los valores del driver a celdas JSON.
package results

import (
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// MaxCellBytes recorta textos muy largos; el valor completo se pide con Buffer.Cell (spec 02).
	MaxCellBytes = 64 * 1024
	// MaxBinaryBytes es lo que se muestra de un BLOB (en hexadecimal).
	MaxBinaryBytes = 256
	// MaxSafeInt es el mayor entero que un number de JavaScript representa sin pérdida.
	MaxSafeInt = 1<<53 - 1
)

// Tipos de celda (campo T). Vacío = valor JSON normal (texto, número, bool).
const (
	TBigint   = "bigint"   // entero fuera del rango seguro de JS: V es string
	TDecimal  = "decimal"  // DECIMAL/NUMERIC: V es string para no perder precisión
	TBytes    = "bytes"    // binario: V es "0x…" truncado
	TDatetime = "datetime" // fecha/hora tal cual la entrega el servidor
	TJSON     = "json"
)

// Cell es un valor listo para Vue. Los campos de JSON siguen el contrato de docs/specs/02.
type Cell struct {
	V     any    `json:"v"`
	T     string `json:"t,omitempty"`
	Trunc bool   `json:"trunc,omitempty"`
	// Edited marca una celda con un cambio pendiente de aplicar (spec 13): V es el valor nuevo, no el del servidor.
	Edited bool `json:"edited,omitempty"`

	full string // valor completo si Trunc; no viaja a Vue
}

// Full devuelve el valor completo de una celda truncada (o V si no lo está).
func (c Cell) Full() any {
	if c.Trunc && c.full != "" {
		return c.full
	}
	return c.V
}

// size estima los bytes de memoria de la celda para contabilizar el límite.
func (c Cell) size() int64 {
	n := int64(48)
	if s, ok := c.V.(string); ok {
		n += int64(len(s))
	}
	return n + int64(len(c.full))
}

type kind int

const (
	kText kind = iota
	kJSON
	kDecimal
	kTemporal
	kBinary
	kOther
)

func classify(dataType string) kind {
	dt := strings.ToUpper(dataType)
	dt = strings.TrimPrefix(dt, "UNSIGNED ")
	switch dt {
	case "CHAR", "VARCHAR", "TEXT", "TINYTEXT", "MEDIUMTEXT", "LONGTEXT", "ENUM", "SET", "NCHAR", "NVARCHAR":
		return kText
	case "JSON":
		return kJSON
	case "DECIMAL", "NUMERIC", "NEWDECIMAL":
		return kDecimal
	case "DATE", "DATETIME", "TIMESTAMP", "TIME", "YEAR":
		return kTemporal
	case "BINARY", "VARBINARY", "BLOB", "TINYBLOB", "MEDIUMBLOB", "LONGBLOB", "GEOMETRY", "BIT", "VECTOR":
		return kBinary
	}
	return kOther
}

// Convert traduce un valor crudo del driver a Cell según el tipo de columna declarado (no según el tipo Go:
// el driver devuelve int64, float64, []byte… según el protocolo).
func Convert(v any, dataType string) Cell {
	switch x := v.(type) {
	case nil:
		return Cell{}
	case bool:
		return Cell{V: x}
	case int64:
		return intCell(x)
	case int:
		return intCell(int64(x))
	case int32:
		return intCell(int64(x))
	case uint64:
		if x > MaxSafeInt {
			return Cell{V: strconv.FormatUint(x, 10), T: TBigint}
		}
		return Cell{V: int64(x)}
	case float64:
		return floatCell(x)
	case float32:
		return floatCell(float64(x))
	case time.Time:
		return Cell{V: x.Format("2006-01-02 15:04:05.999999"), T: TDatetime}
	case string:
		return textCell([]byte(x), dataType)
	case []byte:
		return textCell(x, dataType)
	}
	return Cell{V: fmt.Sprint(v)}
}

func intCell(n int64) Cell {
	if n > MaxSafeInt || n < -MaxSafeInt {
		return Cell{V: strconv.FormatInt(n, 10), T: TBigint}
	}
	return Cell{V: n}
}

func floatCell(f float64) Cell {
	// JSON no puede representar NaN ni infinitos.
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return Cell{V: strconv.FormatFloat(f, 'g', -1, 64)}
	}
	return Cell{V: f}
}

func textCell(b []byte, dataType string) Cell {
	k := classify(dataType)
	if k == kBinary || !utf8.Valid(b) {
		return binaryCell(b)
	}
	t := ""
	switch k {
	case kJSON:
		t = TJSON
	case kDecimal:
		t = TDecimal
	case kTemporal:
		t = TDatetime
	case kOther:
		// Enteros sin signo enormes que el driver entrega como texto.
		if isBigInteger(b) {
			t = TBigint
		}
	}
	s := string(b)
	if len(b) > MaxCellBytes {
		cut := MaxCellBytes
		for cut > 0 && !utf8.RuneStart(b[cut]) { // no partir un carácter UTF-8
			cut--
		}
		return Cell{V: string(b[:cut]), T: t, Trunc: true, full: s}
	}
	return Cell{V: s, T: t}
}

func binaryCell(b []byte) Cell {
	if len(b) > MaxBinaryBytes {
		return Cell{V: "0x" + hex.EncodeToString(b[:MaxBinaryBytes]) + "…", T: TBytes, Trunc: true, full: "0x" + hex.EncodeToString(b)}
	}
	return Cell{V: "0x" + hex.EncodeToString(b), T: TBytes}
}

// isBigInteger reconoce un entero decimal que no cabe en el rango seguro de JS.
func isBigInteger(b []byte) bool {
	s := string(b)
	if s == "" || len(s) < 16 {
		return false
	}
	if s[0] == '-' || s[0] == '+' {
		s = s[1:]
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	n, err := strconv.ParseUint(s, 10, 64)
	return err != nil || n > MaxSafeInt
}
