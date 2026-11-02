package mysql

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"

	gomysql "github.com/go-sql-driver/mysql"

	"github.com/vitacodesas/dbacus/internal/apperr"
	"github.com/vitacodesas/dbacus/internal/model"
)

// Routines lista procedimientos y funciones de una base con una sola consulta y sin ROUTINE_DEFINITION: el cuerpo se
// pide aparte al abrir una rutina (spec 21, D1).
func (r *schemaReader) Routines(ctx context.Context, db string) ([]model.Routine, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT ROUTINE_NAME, ROUTINE_TYPE, DEFINER, SECURITY_TYPE, ROUTINE_COMMENT, LAST_ALTERED
		FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA = ?`, db)
	if err != nil {
		return nil, mapError(err)
	}
	defer rows.Close()
	out := []model.Routine{}
	for rows.Next() {
		var (
			x                                   model.Routine
			typ                                 string
			definer, security, comment, altered sql.NullString
		)
		if err := rows.Scan(&x.Name, &typ, &definer, &security, &comment, &altered); err != nil {
			return nil, mapError(err)
		}
		x.Kind = kindOf(typ)
		x.Definer, x.SecurityType, x.Comment, x.LastAltered = definer.String, security.String, comment.String, altered.String
		out = append(out, x)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError(err)
	}
	// Mismo orden en MySQL y MariaDB; procedimientos y funciones pueden repetir nombre.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return nameLess(out[i].Name, out[j].Name)
		}
		return out[i].Kind < out[j].Kind
	})
	return out, nil
}

func kindOf(routineType string) string {
	if strings.EqualFold(routineType, "FUNCTION") {
		return model.KindFunction
	}
	return model.KindProcedure
}

// DescribeRoutine lee el CREATE (SHOW CREATE), los parámetros y los metadatos de una rutina.
func (r *schemaReader) DescribeRoutine(ctx context.Context, db, kind, name string) (model.RoutineDetail, error) {
	if kind != model.KindProcedure && kind != model.KindFunction {
		return model.RoutineDetail{}, apperr.New(apperr.Validation, "Tipo de rutina desconocido").WithDetail("kind")
	}
	upper := strings.ToUpper(kind)
	d := model.RoutineDetail{Database: db, Params: []model.RoutineParam{}}
	d.Name, d.Kind = name, kind

	var definer, security, comment, altered sql.NullString
	err := r.db.QueryRowContext(ctx, `
		SELECT DEFINER, SECURITY_TYPE, ROUTINE_COMMENT, LAST_ALTERED
		FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA = ? AND ROUTINE_NAME = ? AND ROUTINE_TYPE = ?`, db, name, upper).
		Scan(&definer, &security, &comment, &altered)
	if errors.Is(err, sql.ErrNoRows) {
		return d, apperr.New(apperr.NotFound, "La rutina ya no existe")
	}
	if err != nil {
		return d, mapError(err)
	}
	d.Definer, d.SecurityType, d.Comment, d.LastAltered = definer.String, security.String, comment.String, altered.String

	if err := r.showCreateRoutine(ctx, &d, upper); err != nil {
		return d, err
	}

	prows, err := r.db.QueryContext(ctx, `
		SELECT PARAMETER_MODE, PARAMETER_NAME, DTD_IDENTIFIER, ORDINAL_POSITION
		FROM information_schema.PARAMETERS
		WHERE SPECIFIC_SCHEMA = ? AND SPECIFIC_NAME = ? AND ROUTINE_TYPE = ? ORDER BY ORDINAL_POSITION`, db, name, upper)
	if err != nil {
		return d, mapError(err)
	}
	defer prows.Close()
	for prows.Next() {
		var mode, pname, typ sql.NullString
		var ord int
		if err := prows.Scan(&mode, &pname, &typ, &ord); err != nil {
			return d, mapError(err)
		}
		if ord == 0 { // la posición 0 es el valor de retorno de una función
			d.Returns = typ.String
			continue
		}
		d.Params = append(d.Params, model.RoutineParam{Name: pname.String, Mode: mode.String, Type: typ.String, Ordinal: ord})
	}
	return d, mapError(prows.Err())
}

// showCreateRoutine rellena CreateSQL y las variables de sesión con las que se creó. Sin privilegio, el servidor
// devuelve la fila con el CREATE en NULL: se marca BodyHidden en vez de fallar.
func (r *schemaReader) showCreateRoutine(ctx context.Context, d *model.RoutineDetail, upper string) error {
	rows, err := r.db.QueryContext(ctx, "SHOW CREATE "+upper+" "+QuoteIdent(d.Database)+"."+QuoteIdent(d.Name))
	if err != nil {
		var me *gomysql.MySQLError
		if errors.As(err, &me) && me.Number == 1305 {
			return apperr.New(apperr.NotFound, "La rutina ya no existe")
		}
		return mapError(err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return mapError(err)
	}
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return mapError(err)
		}
		return apperr.New(apperr.NotFound, "La rutina ya no existe")
	}
	vals := make([]sql.NullString, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return mapError(err)
	}
	for i, c := range cols {
		switch strings.ToLower(c) {
		case "sql_mode":
			d.SQLMode = vals[i].String
		case "create procedure", "create function":
			d.CreateSQL = vals[i].String
			d.BodyHidden = !vals[i].Valid
		case "character_set_client":
			d.CharsetClient = vals[i].String
		case "collation_connection":
			d.CollationConnection = vals[i].String
		case "database collation":
			d.DatabaseCollation = vals[i].String
		}
	}
	return nil
}
