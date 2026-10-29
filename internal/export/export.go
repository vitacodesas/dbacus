// Package export serializa resultados a CSV y JSON en streaming (spec 07). No conoce drivers ni Wails:
// recibe celdas ya convertidas por results.Convert, de modo que la misma ruta sirve al buffer en memoria
// y a la re-ejecución directa a archivo.
package export

import (
	"bufio"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/vitacodesas/dbacus/internal/apperr"
	"github.com/vitacodesas/dbacus/internal/model"
	"github.com/vitacodesas/dbacus/internal/results"
)

const (
	FormatCSV  = "csv"
	FormatJSON = "json"
)

// Options reúne las opciones de ambos formatos; las que no aplican al formato elegido se ignoran.
type Options struct {
	Format string `json:"format"` // csv | json

	// CSV
	Delimiter      string `json:"delimiter"`      // "," (por defecto) | ";" | "\t"
	Header         bool   `json:"header"`         // fila de nombres de columna
	NullAs         string `json:"nullAs"`         // texto para NULL ("" por defecto)
	BOM            bool   `json:"bom"`            // UTF-8 BOM al inicio (Excel)
	CRLF           bool   `json:"crlf"`           // fin de línea \r\n
	QuoteAll       bool   `json:"quoteAll"`       // entrecomillar todos los campos
	EscapeFormulas bool   `json:"escapeFormulas"` // antepone ' a textos que empiezan por = + - @ (inyección de fórmulas)

	// JSON
	NDJSON bool `json:"ndjson"` // un objeto por línea en vez de un array
}

// Writer escribe un resultado: Begin una vez, Row por fila y End al final.
type Writer interface {
	Begin(cols []model.ColumnMeta) error
	Row(cells []results.Cell) error
	End() error
}

// Extension es la extensión de archivo sugerida para el formato.
func Extension(o Options) string {
	if o.Format == FormatJSON {
		if o.NDJSON {
			return "ndjson"
		}
		return "json"
	}
	return "csv"
}

// Validate comprueba las opciones (Go es la autoridad, spec 04 "Validación").
func (o Options) Validate() error {
	switch o.Format {
	case FormatCSV:
		switch o.Delimiter {
		case "", ",", ";", "\t":
		default:
			return apperr.New(apperr.Validation, "Separador no válido: usa coma, punto y coma o tabulador").WithDetail("delimiter")
		}
		if len(o.NullAs) > 64 || strings.ContainsAny(o.NullAs, "\r\n") {
			return apperr.New(apperr.Validation, "El texto para NULL es demasiado largo o tiene saltos de línea").WithDetail("nullAs")
		}
	case FormatJSON:
	default:
		return apperr.New(apperr.Validation, "Formato de exportación no soportado").WithDetail("format")
	}
	return nil
}

// New crea el escritor del formato. w debería ser un *bufio.Writer; si no lo es, se envuelve.
func New(w io.Writer, o Options) (Writer, error) {
	if err := o.Validate(); err != nil {
		return nil, err
	}
	bw, ok := w.(*bufio.Writer)
	if !ok {
		bw = bufio.NewWriterSize(w, 64*1024)
	}
	if o.Format == FormatJSON {
		return &jsonWriter{w: bw, o: o}, nil
	}
	if o.Delimiter == "" {
		o.Delimiter = ","
	}
	return &csvWriter{w: bw, o: o}, nil
}

// ---- CSV ----

type csvWriter struct {
	w *bufio.Writer
	o Options
}

func (c *csvWriter) eol() string {
	if c.o.CRLF {
		return "\r\n"
	}
	return "\n"
}

func (c *csvWriter) Begin(cols []model.ColumnMeta) error {
	if c.o.BOM {
		if _, err := c.w.WriteString("\xEF\xBB\xBF"); err != nil {
			return err
		}
	}
	if !c.o.Header {
		return nil
	}
	for i, col := range cols {
		if i > 0 {
			c.w.WriteString(c.o.Delimiter)
		}
		c.field(col.Name, true)
	}
	_, err := c.w.WriteString(c.eol())
	return err
}

func (c *csvWriter) Row(cells []results.Cell) error {
	for i, cell := range cells {
		if i > 0 {
			c.w.WriteString(c.o.Delimiter)
		}
		text, isNull, isText := cellText(cell)
		if isNull {
			c.field(c.o.NullAs, false)
			continue
		}
		c.field(text, isText)
	}
	_, err := c.w.WriteString(c.eol())
	return err
}

func (c *csvWriter) End() error { return c.w.Flush() }

// field escribe un campo; isText indica que es texto libre (candidato a escape de fórmulas).
func (c *csvWriter) field(s string, isText bool) {
	if isText && c.o.EscapeFormulas && s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		s = "'" + s
	}
	needs := c.o.QuoteAll || s != "" && (strings.Contains(s, c.o.Delimiter) || strings.ContainsAny(s, "\"\r\n") || s[0] == ' ' || s[len(s)-1] == ' ')
	if !needs {
		c.w.WriteString(s)
		return
	}
	c.w.WriteByte('"')
	c.w.WriteString(strings.ReplaceAll(s, `"`, `""`))
	c.w.WriteByte('"')
}

// cellText devuelve la representación textual de la celda con su valor completo (no el recortado para Vue).
func cellText(c results.Cell) (text string, isNull, isText bool) {
	if c.V == nil {
		return "", true, false
	}
	switch v := c.Full().(type) {
	case bool:
		return strconv.FormatBool(v), false, false
	case int64:
		return strconv.FormatInt(v, 10), false, false
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64), false, false
	case string:
		return v, false, c.T == "" || c.T == results.TJSON
	default:
		return fmt.Sprint(v), false, false
	}
}

// ---- JSON ----

type jsonWriter struct {
	w     *bufio.Writer
	o     Options
	keys  [][]byte // claves ya serializadas, únicas
	types []string // DataType por columna
	n     int
}

func (j *jsonWriter) Begin(cols []model.ColumnMeta) error {
	seen := map[string]int{}
	j.keys, j.types = make([][]byte, len(cols)), make([]string, len(cols))
	for i, col := range cols {
		name := col.Name
		// Dos columnas con el mismo nombre (JOIN) perderían datos en un objeto: la segunda recibe sufijo.
		if k := seen[name]; k > 0 {
			name = name + "_" + strconv.Itoa(k+1)
		}
		seen[col.Name]++
		b, _ := json.Marshal(name)
		j.keys[i], j.types[i] = b, strings.ToUpper(col.DataType)
	}
	if j.o.NDJSON {
		return nil
	}
	_, err := j.w.WriteString("[")
	return err
}

func (j *jsonWriter) Row(cells []results.Cell) error {
	if j.o.NDJSON {
		if j.n > 0 {
			j.w.WriteByte('\n')
		}
	} else if j.n > 0 {
		j.w.WriteString(",\n")
	} else {
		j.w.WriteByte('\n')
	}
	j.n++
	j.w.WriteByte('{')
	for i, cell := range cells {
		if i > 0 {
			j.w.WriteByte(',')
		}
		j.w.Write(j.keys[i])
		j.w.WriteByte(':')
		j.value(cell, j.types[i])
	}
	return j.w.WriteByte('}')
}

func (j *jsonWriter) End() error {
	if j.o.NDJSON {
		if j.n > 0 {
			j.w.WriteByte('\n')
		}
		return j.w.Flush()
	}
	if j.n == 0 {
		j.w.WriteString("]")
	} else {
		j.w.WriteString("\n]")
	}
	return j.w.Flush()
}

func (j *jsonWriter) value(c results.Cell, dataType string) {
	if c.V == nil {
		j.w.WriteString("null")
		return
	}
	switch c.T {
	case results.TBytes: // base64 del contenido completo (spec 07)
		s, _ := c.Full().(string)
		raw, err := hex.DecodeString(strings.TrimPrefix(s, "0x"))
		if err != nil {
			j.str(s)
			return
		}
		j.str(base64.StdEncoding.EncodeToString(raw))
		return
	case results.TBigint, results.TDecimal: // como string: JS no los representa sin pérdida
		j.str(fmt.Sprint(c.Full()))
		return
	case results.TJSON:
		if s, ok := c.Full().(string); ok && json.Valid([]byte(s)) {
			j.w.WriteString(s)
			return
		}
	case results.TDatetime:
		if s, ok := c.Full().(string); ok {
			j.str(iso8601(s, dataType))
			return
		}
	}
	switch v := c.Full().(type) {
	case bool:
		j.w.WriteString(strconv.FormatBool(v))
	case int64:
		j.w.WriteString(strconv.FormatInt(v, 10))
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			j.str(strconv.FormatFloat(v, 'g', -1, 64))
		} else {
			j.w.WriteString(strconv.FormatFloat(v, 'f', -1, 64))
		}
	case string:
		j.str(v)
	default:
		j.str(fmt.Sprint(v))
	}
}

func (j *jsonWriter) str(s string) {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "�")
	}
	b, _ := json.Marshal(s)
	j.w.Write(b)
}

// iso8601 convierte "2006-01-02 15:04:05.999" en "2006-01-02T15:04:05.999" para DATETIME/TIMESTAMP;
// DATE, TIME y YEAR se dejan tal cual (no tienen una forma ISO sin inventar datos).
func iso8601(s, dataType string) string {
	if dataType != "DATETIME" && dataType != "TIMESTAMP" {
		return s
	}
	if len(s) > 10 && s[10] == ' ' {
		return s[:10] + "T" + s[11:]
	}
	return s
}
