package edit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/vitacodesas/dbacus/internal/apperr"
	"github.com/vitacodesas/dbacus/internal/model"
	"github.com/vitacodesas/dbacus/internal/results"
)

// Value es lo que el usuario escribe en una celda: NULL explícito o texto (la cadena vacía NO es NULL).
type Value struct {
	Null bool   `json:"null"`
	Text string `json:"text"`
}

func invalid(format string, a ...any) error {
	return apperr.Newf(apperr.Validation, format, a...).WithDetail("cell")
}

var (
	decimalRe = regexp.MustCompile(`^[+-]?(\d+(\.\d*)?|\.\d+)$`)
	timeRe    = regexp.MustCompile(`^-?\d{1,3}:[0-5]\d:[0-5]\d(\.\d{1,6})?$`)
	sizeRe    = regexp.MustCompile(`\((\d+)(?:,\s*(\d+))?\)`)
)

type intRange struct {
	min, max int64
	umax     uint64
}

var intRanges = map[string]intRange{
	"tinyint":   {math.MinInt8, math.MaxInt8, math.MaxUint8},
	"smallint":  {math.MinInt16, math.MaxInt16, math.MaxUint16},
	"mediumint": {-1 << 23, 1<<23 - 1, 1<<24 - 1},
	"int":       {math.MinInt32, math.MaxInt32, math.MaxUint32},
	"integer":   {math.MinInt32, math.MaxInt32, math.MaxUint32},
	"bigint":    {math.MinInt64, math.MaxInt64, math.MaxUint64},
}

func isUnsigned(columnType string) bool {
	return strings.Contains(strings.ToLower(columnType), "unsigned")
}

// Verifiable dice si el valor original de una columna del tipo dado se puede comparar con exactitud en el WHERE del
// control optimista. Se excluyen los tipos cuya igualdad depende de la sesión o de la representación: FLOAT/DOUBLE (un
// FLOAT no coincide con el texto que lo representa), JSON (se compara como valor JSON, no como texto) y TIMESTAMP (depende
// de la zona horaria). En esas columnas la fila se localiza por la clave y no se detecta un cambio ajeno del valor.
func Verifiable(dataType string) bool {
	dt := strings.ToLower(dataType)
	if _, ok := intRanges[dt]; ok {
		return true
	}
	switch dt {
	case "decimal", "numeric", "char", "varchar", "tinytext", "text", "mediumtext", "longtext", "enum", "set", "date", "datetime", "time", "year":
		return true
	}
	return false
}

// Convert valida el texto escrito para la columna y lo convierte al valor que se envía como parámetro.
// Los enteros salen como int64/uint64 (no como texto) para que los BIGINT UNSIGNED no pasen por coma flotante.
func Convert(col model.Column, v Value) (any, error) {
	if v.Null {
		if !col.Nullable {
			return nil, invalid("La columna «%s» no admite NULL", col.Name)
		}
		return nil, nil
	}
	dt, text := strings.ToLower(col.DataType), strings.TrimSpace(v.Text)
	if r, ok := intRanges[dt]; ok {
		return convertInt(col, dt, r, text)
	}
	switch dt {
	case "decimal", "numeric":
		return convertDecimal(col, text)
	case "float", "double", "real":
		f, err := strconv.ParseFloat(text, 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, invalid("«%s» no es un número válido", v.Text)
		}
		return f, nil
	case "year":
		n, err := strconv.Atoi(text)
		if err != nil || (n != 0 && (n < 1901 || n > 2155)) {
			return nil, invalid("El año debe estar entre 1901 y 2155 (o 0)")
		}
		return int64(n), nil
	case "date":
		t, err := time.Parse("2006-01-02", text)
		if err != nil {
			return nil, invalid("La fecha debe tener el formato AAAA-MM-DD")
		}
		return t.Format("2006-01-02"), nil
	case "datetime", "timestamp":
		return convertDatetime(text)
	case "time":
		if !timeRe.MatchString(text) {
			return nil, invalid("La hora debe tener el formato HH:MM:SS (hasta 838:59:59)")
		}
		return text, nil
	case "json":
		if err := CheckJSON(v.Text); err != nil {
			return nil, err
		}
		return v.Text, nil
	case "char", "varchar", "tinytext", "text", "mediumtext", "longtext", "enum", "set":
		return convertText(col, dt, v.Text)
	}
	return nil, apperr.Newf(apperr.Unsupported, "El tipo %s no se puede editar", col.DataType).WithDetail("cell")
}

func convertInt(col model.Column, dt string, r intRange, text string) (any, error) {
	if lower := strings.ToLower(text); (lower == "true" || lower == "false") && strings.Contains(strings.ToLower(col.ColumnType), "tinyint(1)") {
		if lower == "true" {
			return int64(1), nil
		}
		return int64(0), nil
	}
	if isUnsigned(col.ColumnType) {
		n, err := strconv.ParseUint(strings.TrimPrefix(text, "+"), 10, 64)
		if err != nil || n > r.umax {
			return nil, invalid("«%s» no es un entero válido para %s unsigned (0 a %d)", text, dt, r.umax)
		}
		if n > math.MaxInt64 {
			return n, nil
		}
		return int64(n), nil
	}
	n, err := strconv.ParseInt(strings.TrimPrefix(text, "+"), 10, 64)
	if err != nil || n < r.min || n > r.max {
		return nil, invalid("«%s» no es un entero válido para %s (%d a %d)", text, dt, r.min, r.max)
	}
	return n, nil
}

func convertDecimal(col model.Column, text string) (any, error) {
	if !decimalRe.MatchString(text) {
		return nil, invalid("«%s» no es un número decimal válido", text)
	}
	if m := sizeRe.FindStringSubmatch(col.ColumnType); m != nil && m[2] != "" {
		p, _ := strconv.Atoi(m[1])
		s, _ := strconv.Atoi(m[2])
		intDigits := strings.TrimLeft(strings.SplitN(strings.TrimLeft(text, "+-"), ".", 2)[0], "0")
		if len(intDigits) > p-s {
			return nil, invalid("«%s» tiene demasiadas cifras enteras para DECIMAL(%d,%d)", text, p, s)
		}
	}
	return text, nil
}

var datetimeLayouts = []string{
	"2006-01-02 15:04:05.999999", "2006-01-02T15:04:05.999999", "2006-01-02 15:04", "2006-01-02", "2006-01-02T15:04",
}

func convertDatetime(text string) (any, error) {
	for _, l := range datetimeLayouts {
		if t, err := time.Parse(l, text); err == nil {
			return t.Format("2006-01-02 15:04:05.999999"), nil
		}
	}
	return nil, invalid("La fecha y hora deben tener el formato AAAA-MM-DD HH:MM:SS[.ffffff]")
}

func convertText(col model.Column, dt, text string) (any, error) {
	if !utf8.ValidString(text) {
		return nil, invalid("El texto no es UTF-8 válido")
	}
	if dt == "char" || dt == "varchar" {
		if m := sizeRe.FindStringSubmatch(col.ColumnType); m != nil {
			if n, _ := strconv.Atoi(m[1]); utf8.RuneCountInString(text) > n {
				return nil, invalid("El texto tiene %d caracteres y «%s» admite %d", utf8.RuneCountInString(text), col.Name, n)
			}
		}
	}
	return text, nil
}

// OriginalArg convierte el valor original de una celda (tal como llegó del servidor) en el parámetro del WHERE.
// null=true significa que el original era NULL.
func OriginalArg(col model.Column, c results.Cell) (arg any, null bool, err error) {
	if c.V == nil {
		return nil, true, nil
	}
	s := fmt.Sprint(c.V)
	dt := strings.ToLower(col.DataType)
	if _, ok := intRanges[dt]; ok {
		if isUnsigned(col.ColumnType) {
			if n, err := strconv.ParseUint(s, 10, 64); err == nil {
				if n > math.MaxInt64 {
					return n, false, nil
				}
				return int64(n), false, nil
			}
		} else if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return n, false, nil
		}
		return nil, false, invalid("El valor original de «%s» no es un entero", col.Name)
	}
	return s, false, nil
}

// Same dice si el valor nuevo equivale al original (entonces no hay cambio que enviar). Compara en forma canónica:
// 1.50 = 1.5 en DECIMAL, 007 = 7 en enteros; el texto se compara tal cual.
func Same(col model.Column, newV Value, orig results.Cell) bool {
	if newV.Null || orig.V == nil {
		return newV.Null && orig.V == nil
	}
	dt, o := strings.ToLower(col.DataType), fmt.Sprint(orig.V)
	switch dt {
	case "decimal", "numeric":
		a, ok1 := new(big.Rat).SetString(strings.TrimSpace(newV.Text))
		b, ok2 := new(big.Rat).SetString(o)
		return ok1 && ok2 && a.Cmp(b) == 0
	case "tinyint", "smallint", "mediumint", "int", "integer", "bigint":
		a, err1 := Convert(col, newV)
		b, _, err2 := OriginalArg(col, orig)
		return err1 == nil && err2 == nil && fmt.Sprint(a) == fmt.Sprint(b)
	case "datetime", "timestamp", "date", "time":
		a, err := Convert(col, newV)
		return err == nil && fmt.Sprint(a) == o
	case "json":
		// El JSON de MySQL se guarda normalizado (espacios, orden de claves): dar formato no es un cambio.
		return sameJSON(newV.Text, o)
	}
	return newV.Text == o
}

// CheckJSON valida un texto JSON y, si no lo es, dice dónde falla y qué carácter encontró (p. ej. unas comillas
// tipográficas «“» que el sistema sustituyó al escribir, que a simple vista parecen normales).
func CheckJSON(text string) error {
	var v json.RawMessage
	err := json.Unmarshal([]byte(text), &v)
	if err == nil {
		return nil
	}
	var se *json.SyntaxError
	if errors.As(err, &se) {
		return invalid("El valor no es JSON válido: %s (posición %d)", se.Error(), se.Offset)
	}
	return invalid("El valor no es JSON válido")
}

// FormatJSON devuelve el JSON con sangría de dos espacios (error si no es válido).
func FormatJSON(text string) (string, error) {
	if err := CheckJSON(text); err != nil {
		return "", err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, []byte(text), "", "  "); err != nil {
		return "", invalid("No se pudo dar formato al JSON")
	}
	return out.String(), nil
}

// sameJSON compara dos textos JSON por su valor (no por su formato). Los números se comparan por su texto: 1 y 1.0 son
// valores distintos en MySQL, así que ante la duda se considera un cambio.
func sameJSON(a, b string) bool {
	parse := func(s string) (any, bool) {
		dec := json.NewDecoder(strings.NewReader(s))
		dec.UseNumber()
		var v any
		if err := dec.Decode(&v); err != nil || dec.More() {
			return nil, false
		}
		return v, true
	}
	va, ok1 := parse(a)
	vb, ok2 := parse(b)
	return ok1 && ok2 && reflect.DeepEqual(va, vb)
}
