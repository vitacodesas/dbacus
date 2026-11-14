package migrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/vitacodesas/dbacus/internal/apperr"
	"github.com/vitacodesas/dbacus/internal/driver"
	"github.com/vitacodesas/dbacus/internal/model"
)

// systemSchemas no pueden ser origen ni destino (spec 23, validación).
var systemSchemas = map[string]bool{"mysql": true, "sys": true, "information_schema": true, "performance_schema": true}

// PlanDeps son los dos extremos de la migración. SameSession elige la ruta en el servidor (⚑D2).
type PlanDeps struct {
	Source, Target driver.SchemaMigrator
	SameSession    bool
}

// Prepared es el plan completo con lo que el job necesita (el DTO que ve Vue es Prepared.Plan).
type Prepared struct {
	Plan          model.MigrationPlan
	Req           model.MigrationRequest
	Source        driver.SchemaMigrator
	Target        driver.SchemaMigrator
	Src           model.MigrationSource
	SourceDB      string
	TargetDB      string
	TargetNew     bool
	Tables        []model.MigrationTable // las que se copian
	SkippedTables []string               // ya existían en el destino y se pidió omitirlas
	Views         []model.MigrationCode  // con CreateSQL reescrito
	Routines      []model.MigrationCode
	Triggers      []model.MigrationCode
	Workers       int
	ChunkRows     map[string]int
	StructureOnly map[string]bool // tablas elegidas que se crean vacías (⚑D20)
	// SkippedObjects son vistas, rutinas y triggers elegidos que ya existían en el destino y se dejan como están (⚑D21).
	SkippedObjects []model.PlanObject
}

// autoIncOption es la opción de tabla `AUTO_INCREMENT=N` del SHOW CREATE (la de columna no lleva «=»).
var autoIncOption = regexp.MustCompile(`\s*\bAUTO_INCREMENT=\d+`)

// CompareCode compara el código que se crearía con el que hay en el destino: `same` si coinciden tras quitar el DEFINER y normalizar
// espacios, `different` si no, `unknown` si falta alguno de los dos (no se pudo leer). Un `different` puede ser un falso positivo.
func CompareCode(mine, theirs string) string {
	if strings.TrimSpace(mine) == "" || strings.TrimSpace(theirs) == "" {
		return "unknown"
	}
	norm := func(s string) string {
		s, _ = StripDefiner(s)
		return strings.Join(strings.Fields(strings.TrimSuffix(strings.TrimSpace(s), ";")), " ")
	}
	if norm(mine) == norm(theirs) {
		return "same"
	}
	return "different"
}

// Objects lista las vistas, rutinas y triggers elegidos, en el orden en que se crean.
func (p *Prepared) Objects() []model.PlanObject {
	var out []model.PlanObject
	for _, r := range p.Routines {
		out = append(out, model.PlanObject{Name: r.Name, Kind: r.Kind})
	}
	for _, v := range p.Views {
		out = append(out, model.PlanObject{Name: v.Name, Kind: "view"})
	}
	for _, t := range p.Triggers {
		out = append(out, model.PlanObject{Name: t.Name, Kind: "trigger", Table: t.Table})
	}
	return out
}

// ValidateName comprueba el nombre de un schema (spec 23, validación).
func ValidateName(name string) error {
	switch {
	case name == "" || utf8.RuneCountInString(name) > 64:
		return apperr.New(apperr.Validation, "El nombre de la base debe tener entre 1 y 64 caracteres").WithDetail("database")
	case strings.ContainsAny(name, "/\\.\x00") || strings.HasSuffix(name, " "):
		return apperr.New(apperr.Validation, "El nombre de la base no puede llevar «/», «\\», «.», caracteres nulos ni terminar en espacio").WithDetail("database")
	case systemSchemas[strings.ToLower(name)]:
		return apperr.New(apperr.Validation, "Los schemas del sistema no pueden ser origen ni destino").WithDetail("system_schema")
	}
	return nil
}

func autoChunkRows(t model.MigrationTable, req int) int {
	if req > 0 {
		return min(max(req, model.MigMinChunkRows), model.MigMaxChunkRows)
	}
	if t.AvgRowLen <= 0 {
		return 100_000
	}
	return int(min(max((64<<20)/t.AvgRowLen, 10_000), 500_000)) // ≈ 64 MiB por trozo, acotado
}

func selected(all []string, want []string, nilMeansAll bool) (map[string]bool, error) {
	set := map[string]bool{}
	if want == nil && nilMeansAll {
		for _, n := range all {
			set[n] = true
		}
		return set, nil
	}
	have := map[string]bool{}
	for _, n := range all {
		have[n] = true
	}
	for _, n := range want {
		if !have[n] {
			return nil, apperr.Newf(apperr.Validation, "«%s» no existe en el origen", n).WithDetail("objects")
		}
		set[n] = true
	}
	return set, nil
}

// Plan inspecciona origen y destino sin crear nada y arma el plan (solo lectura). Todas las comprobaciones que no necesitan
// mirar el destino ocurren antes que las que sí.
func Plan(ctx context.Context, d PlanDeps, req model.MigrationRequest) (*Prepared, error) {
	if d.Source == nil || d.Target == nil {
		return nil, apperr.New(apperr.Unsupported, "Este motor no soporta migrar schemas")
	}
	src, dst := req.Source.Database, req.Target.Database
	if err := ValidateName(src); err != nil {
		return nil, apperr.New(apperr.Validation, "Origen: "+err.(*apperr.AppError).Message).WithDetail(detailOf(err, "source"))
	}
	if err := ValidateName(dst); err != nil {
		return nil, err
	}
	if d.SameSession && src == dst {
		return nil, apperr.New(apperr.Validation, "El origen y el destino son el mismo schema").WithDetail("same_schema")
	}
	o := req.Options
	if o.Workers < 0 || o.Workers > model.MigMaxWorkers {
		return nil, apperr.Newf(apperr.Validation, "Los workers deben estar entre 1 y %d", model.MigMaxWorkers).WithDetail("workers")
	}
	if o.ChunkRows != 0 && (o.ChunkRows < model.MigMinChunkRows || o.ChunkRows > model.MigMaxChunkRows) {
		return nil, apperr.Newf(apperr.Validation, "El tamaño de trozo debe ser 0 o estar entre %d y %d filas", model.MigMinChunkRows, model.MigMaxChunkRows).WithDetail("chunkRows")
	}

	inspected, err := d.Source.MigrationInspect(ctx, src)
	if err != nil {
		return nil, err
	}
	p := &Prepared{Req: req, Source: d.Source, Target: d.Target, Src: inspected, SourceDB: src, TargetDB: dst, ChunkRows: map[string]int{}}
	route := model.RouteGo
	if d.SameSession {
		route = model.RouteServer
	}

	// Selección de objetos.
	names := func(c []model.MigrationCode) []string {
		out := make([]string, len(c))
		for i, x := range c {
			out[i] = x.Name
		}
		return out
	}
	tnames := make([]string, len(inspected.Tables))
	for i, t := range inspected.Tables {
		tnames[i] = t.Name
	}
	selT, err := selected(tnames, req.Objects.Tables, true)
	if err != nil {
		return nil, err
	}
	structOnly := map[string]bool{}
	for _, n := range req.Objects.StructureOnly {
		if !selT[n] {
			return nil, apperr.Newf(apperr.Validation, "La tabla «%s» está marcada como solo estructura pero no está entre las que se migran", n).WithDetail("objects")
		}
		structOnly[n] = true
	}
	selV, err := selected(names(inspected.Views), req.Objects.Views, true)
	if err != nil {
		return nil, err
	}
	selTr, err := selected(names(inspected.Triggers), req.Objects.Triggers, true)
	if err != nil {
		return nil, err
	}
	routineKey := func(kind, name string) string { return kind + ":" + name }
	selR := map[string]bool{}
	if req.Objects.Routines == nil {
		for _, r := range inspected.Routines {
			selR[routineKey(r.Kind, r.Name)] = true
		}
	} else {
		have := map[string]bool{}
		for _, r := range inspected.Routines {
			have[routineKey(r.Kind, r.Name)] = true
		}
		for _, r := range req.Objects.Routines {
			if !have[routineKey(r.Kind, r.Name)] {
				return nil, apperr.Newf(apperr.Validation, "La rutina «%s» no existe en el origen", r.Name).WithDetail("objects")
			}
			selR[routineKey(r.Kind, r.Name)] = true
		}
	}

	var chosen []model.MigrationTable
	for _, t := range inspected.Tables {
		if selT[t.Name] {
			if structOnly[t.Name] { // una tabla vacía empieza en 1, no en el contador del origen
				t.CreateSQL = autoIncOption.ReplaceAllString(t.CreateSQL, "")
				t.AutoIncrement = 0
			}
			chosen = append(chosen, t)
		}
	}

	// Tipos espaciales: la ruta por Go no los copia (spec 23).
	if route == model.RouteGo {
		for _, t := range chosen {
			if len(t.Spatial) > 0 && !structOnly[t.Name] {
				return nil, apperr.Newf(apperr.Unsupported, "La tabla «%s» tiene columnas espaciales (%s): solo se pueden copiar dentro del mismo servidor", t.Name, strings.Join(t.Spatial, ", ")).WithDetail("spatial")
			}
		}
	}

	// Comprobaciones del destino.
	in := model.MigrationPreflightInput{Database: dst, SourceName: src, SameServer: d.SameSession, Workers: o.Workers}
	collations := map[string]bool{inspected.Collation: true}
	for _, t := range chosen {
		for _, c := range t.Charsets {
			collations[c] = true
		}
		if !t.HasPrimaryKey {
			in.NoPKTables = append(in.NoPKTables, t.Name)
		}
	}
	for c := range collations {
		if c != "" {
			in.Collations = append(in.Collations, c)
		}
	}
	sort.Strings(in.Collations)
	in.NeedsRoutine, in.NeedsTrigger, in.NeedsView = len(selR) > 0, len(selTr) > 0, len(selV) > 0
	for _, r := range inspected.Routines {
		if selR[routineKey(r.Kind, r.Name)] {
			in.Objects = append(in.Objects, model.ObjectRef{Kind: r.Kind, Name: r.Name})
		}
	}
	for _, c := range inspected.Views {
		if selV[c.Name] {
			in.Objects = append(in.Objects, model.ObjectRef{Kind: "view", Name: c.Name})
		}
	}
	for _, c := range inspected.Triggers {
		if selTr[c.Name] {
			in.Objects = append(in.Objects, model.ObjectRef{Kind: "trigger", Name: c.Name})
		}
	}
	pre, err := d.Target.MigrationPreflight(ctx, in)
	if err != nil {
		return nil, err
	}
	if d.SameSession && pre.SameAsSource {
		return nil, apperr.New(apperr.Validation, "El origen y el destino son la misma base de datos para el servidor (los nombres solo difieren en mayúsculas o acentos)").WithDetail("same_schema")
	}
	if len(pre.Missing) > 0 {
		return nil, apperr.New(apperr.QueryFailed, "Faltan privilegios en el destino: "+strings.Join(pre.Missing, " · ")).WithDetail("privilege")
	}
	if len(pre.MissingCollation) > 0 {
		return nil, apperr.New(apperr.QueryFailed, "El destino no conoce estos collations del origen: "+strings.Join(pre.MissingCollation, ", ")).WithDetail("incompatible")
	}
	p.TargetNew = !pre.Exists

	existing := map[string]bool{}
	for _, n := range pre.ExistingTables {
		existing[n] = true
	}
	existCode := map[string]model.MigrationCode{}
	var objClash []string
	for _, c := range pre.ExistingCode {
		existCode[c.Kind+":"+strings.ToLower(c.Name)] = c
		objClash = append(objClash, c.Name)
	}
	var clash []string
	for _, t := range chosen {
		if existing[t.Name] {
			clash = append(clash, t.Name)
		}
	}
	if pre.Exists && pre.ExistingObjects > 0 && !o.SkipExisting {
		list := clash
		if len(list) > 10 {
			list = append(list[:10:10], "…")
		}
		msg := "El schema destino ya tiene objetos"
		if len(list) > 0 {
			msg += " (tablas que chocan: " + strings.Join(list, ", ") + ")"
		}
		if len(objClash) > 0 {
			objs := objClash
			if len(objs) > 10 {
				objs = append(objs[:10:10], "…")
			}
			msg += " (vistas, rutinas o triggers que chocan: " + strings.Join(objs, ", ") + ")"
		}
		return nil, apperr.New(apperr.QueryFailed, msg+". Elige un schema vacío o marca «omitir lo que ya existe»").WithDetail("exists")
	}
	var kept []model.MigrationTable
	for _, t := range chosen {
		if existing[t.Name] {
			p.SkippedTables = append(p.SkippedTables, t.Name)
			continue
		}
		kept = append(kept, t)
	}
	chosen = kept

	if pre.RequirePK {
		var bad []string
		for _, t := range chosen {
			if !t.HasPrimaryKey {
				bad = append(bad, t.Name)
			}
		}
		if len(bad) > 0 {
			return nil, apperr.New(apperr.QueryFailed, "El destino exige clave primaria (sql_require_primary_key) y estas tablas no la tienen: "+strings.Join(bad, ", ")+". Desmárcalas para seguir").WithDetail("incompatible")
		}
	}

	// Workers (⚑D7): nunca más de la mitad de las conexiones libres del destino.
	workers := o.Workers
	if workers == 0 {
		workers = model.MigDefaultWorkers
	}
	var warns []model.MigrationWarning
	if pre.MaxConnections > 0 {
		capW := pre.FreeConnections/2 - planners
		if capW < 1 {
			return nil, apperr.Newf(apperr.QueryFailed, "El servidor tiene muy pocas conexiones libres (%d de %d)", pre.FreeConnections, pre.MaxConnections).WithDetail("connections")
		}
		if workers > capW {
			warns = append(warns, model.MigrationWarning{Code: "workers_capped", Level: "info", Message: fmt.Sprintf("Se usarán %d workers en vez de %d para no pasar de la mitad de las conexiones libres del servidor", capW, workers)})
			workers = capW
		}
	}
	if workers > 8 {
		warns = append(warns, model.MigrationWarning{Code: "many_workers", Level: "warning", Message: "Más de 8 workers no suma velocidad y en las mediciones provocó paradas de varios segundos en la ruta por la app; el valor por defecto es 4"})
	}
	p.Workers = workers
	p.Tables = chosen
	p.StructureOnly = structOnly
	for _, t := range chosen {
		p.ChunkRows[t.Name] = autoChunkRows(t, o.ChunkRows)
	}

	// Objetos con código: DEFINER fuera y schema reescrito.
	known := map[string]bool{}
	for _, t := range inspected.Tables {
		known[t.Name] = true
	}
	for _, c := range inspected.Views {
		known[c.Name] = true
	}
	for _, c := range inspected.Routines {
		known[c.Name] = true
	}
	for _, c := range inspected.Triggers {
		known[c.Name] = true
	}
	prep := func(c model.MigrationCode) model.MigrationCode {
		sql, definer := StripDefiner(c.CreateSQL)
		rr := RewriteSchema(sql, src, dst, known)
		c.CreateSQL = rr.SQL
		if definer != "" && !sameUser(definer, pre.CurrentUser) {
			warns = append(warns, model.MigrationWarning{Code: "definer_foreign", Level: "warning", Object: c.Name,
				Message: fmt.Sprintf("Se creará a nombre del usuario que migra; el original era %s", definer)})
		}
		for _, w := range rr.Warnings {
			switch w {
			case "dynamic_sql":
				warns = append(warns, model.MigrationWarning{Code: w, Level: "warning", Object: c.Name, Message: "Usa PREPARE/EXECUTE: el SQL dinámico no se reescribe y puede seguir apuntando al schema origen"})
			case "string_mentions_source":
				warns = append(warns, model.MigrationWarning{Code: w, Level: "warning", Object: c.Name, Message: "Una cadena cita el schema origen: no se reescribe"})
			}
		}
		return c
	}
	// SHOW CREATE devuelve el cuerpo de una rutina como NULL si el usuario solo tiene EXECUTE (sin SHOW_ROUTINE ni ser su definer):
	// no hay nada que copiar. Se omite aquí, con el motivo, y no a mitad de la migración con un error del guardia.
	readable := func(c model.MigrationCode, kind string) bool {
		if strings.TrimSpace(c.CreateSQL) != "" {
			return true
		}
		// Sigue en el plan, marcado, para que el usuario lo vea en el paso de contenido y sepa por qué no se puede elegir.
		p.SkippedObjects = append(p.SkippedObjects, model.PlanObject{Name: c.Name, Kind: kind, Table: c.Table, Skipped: true, Compare: "unreadable"})
		warns = append(warns, model.MigrationWarning{Code: "unreadable_code", Level: "warning", Object: c.Name,
			Message: "No se puede leer su código: EXECUTE no basta. Hace falta ser su definer, o tener SHOW_ROUTINE global (MySQL 8.0.20+) o SELECT sobre mysql.proc (MariaDB); para vistas, SHOW VIEW. Se omite"})
		return false
	}
	// Lo que ya existe en el destino se deja como está (⚑D21): se compara su código con el que se crearía y se avisa, sin reemplazar nada.
	skipExisting := func(c model.MigrationCode, kind string) bool {
		ex, ok := existCode[kind+":"+strings.ToLower(c.Name)]
		if !ok {
			return false
		}
		mine, _ := StripDefiner(c.CreateSQL)
		cmp := CompareCode(RewriteSchema(mine, src, dst, known).SQL, ex.CreateSQL)
		p.SkippedObjects = append(p.SkippedObjects, model.PlanObject{Name: c.Name, Kind: kind, Table: c.Table, Skipped: true, Compare: cmp})
		w := model.MigrationWarning{Code: "object_exists_" + cmp, Level: "warning", Object: c.Name}
		switch cmp {
		case "same":
			w.Level, w.Message = "info", "Ya existe en el destino con el mismo código: se omite"
		case "different":
			w.Message = "Ya existe en el destino y su código es distinto: se omite y NO se reemplaza (el del destino queda como está)"
		default:
			w.Message = "Ya existe en el destino y no se pudo comparar su código: se omite y NO se reemplaza"
		}
		warns = append(warns, w)
		return true
	}
	for _, c := range inspected.Routines {
		if selR[routineKey(c.Kind, c.Name)] && !skipExisting(c, c.Kind) && readable(c, c.Kind) {
			p.Routines = append(p.Routines, prep(c))
		}
	}
	for _, c := range inspected.Views {
		if selV[c.Name] && !skipExisting(c, "view") && readable(c, "view") {
			p.Views = append(p.Views, prep(c))
		}
	}
	usable := map[string]bool{}
	for _, t := range chosen {
		usable[t.Name] = true
	}
	for _, c := range inspected.Triggers {
		if !selTr[c.Name] {
			continue
		}
		if !usable[c.Table] {
			warns = append(warns, model.MigrationWarning{Code: "trigger_table_skipped", Level: "info", Object: c.Name, Message: "Su tabla no se migra: el trigger se omite"})
			continue
		}
		if skipExisting(c, "trigger") || !readable(c, "trigger") {
			continue
		}
		p.Triggers = append(p.Triggers, prep(c))
	}

	// Plan para Vue.
	plan := model.MigrationPlan{Route: route, Workers: workers, TargetNew: p.TargetNew, Warnings: warns, ServerCaps: model.ServerCaps{MaxConnections: pre.MaxConnections, FreeConnections: pre.FreeConnections}}
	if route == model.RouteServer {
		plan.Warnings = append(plan.Warnings, model.MigrationWarning{Code: "route_server", Level: "info", Message: "La copia se hace dentro del servidor (INSERT … SELECT): los datos no pasan por la app y el origen solo se lee"})
	} else {
		plan.Warnings = append(plan.Warnings, model.MigrationWarning{Code: "route_go", Level: "info", Message: "Los datos pasan por la app: las conexiones que leen el origen son de solo lectura"})
	}
	for _, t := range chosen {
		pt := model.PlanTable{Name: t.Name, EstRows: t.EstRows, EstBytes: t.EstBytes, ChunkKey: t.Key.Kind, Columns: len(t.Columns), SkippedColumns: t.SkippedColumns, Warnings: []string{}}
		pt.Chunks = max(1, int((t.EstRows+int64(p.ChunkRows[t.Name])-1)/int64(p.ChunkRows[t.Name])))
		if structOnly[t.Name] {
			pt.StructureOnly, pt.EstRows, pt.EstBytes, pt.Chunks = true, 0, 0, 0
		} else {
			switch t.Key.Kind {
			case model.ChunkNone:
				pt.Chunks = 1
				pt.Warnings = append(pt.Warnings, "no_index")
				plan.Warnings = append(plan.Warnings, model.MigrationWarning{Code: "no_index", Level: "warning", Object: t.Name, Message: "No tiene ningún índice: se copia en un solo trozo, sin paralelismo y sin progreso intermedio"})
			case model.ChunkPartition:
				pt.Chunks = max(1, len(t.Key.Partitions))
			case model.ChunkIndex:
				pt.Warnings = append(pt.Warnings, "index_key")
				plan.Warnings = append(plan.Warnings, model.MigrationWarning{Code: "index_key", Level: "info", Object: t.Name, Message: "Sin clave primaria ni única: se trocea por un índice no único; los trozos pueden salir desiguales"})
			}
		}
		for _, fk := range t.ForeignKeys {
			if !structOnly[t.Name] && structOnly[fk.RefTable] && (fk.RefSchema == "" || fk.RefSchema == src) {
				plan.Warnings = append(plan.Warnings, model.MigrationWarning{Code: "fk_structure_only", Level: "warning", Object: t.Name + "." + fk.Name, Message: "Apunta a «" + fk.RefTable + "», que se crea vacía: las filas copiadas quedarán sin fila padre"})
			}
			if fk.RefSchema != "" && fk.RefSchema != src {
				plan.Warnings = append(plan.Warnings, model.MigrationWarning{Code: "fk_external_schema", Level: "warning", Object: t.Name + "." + fk.Name, Message: "Apunta a otro schema (" + fk.RefSchema + "): se conserva tal cual"})
			}
		}
		plan.Tables = append(plan.Tables, pt)
	}
	for _, n := range p.SkippedTables {
		plan.Tables = append(plan.Tables, model.PlanTable{Name: n, Skipped: true, Warnings: []string{"exists"}})
	}
	for _, o := range p.Objects() {
		switch o.Kind {
		case "view":
			plan.Views = append(plan.Views, o)
		case "trigger":
			plan.Triggers = append(plan.Triggers, o)
		default:
			plan.Routines = append(plan.Routines, o)
		}
	}
	for _, o := range p.SkippedObjects {
		switch o.Kind {
		case "view":
			plan.Views = append(plan.Views, o)
		case "trigger":
			plan.Triggers = append(plan.Triggers, o)
		default:
			plan.Routines = append(plan.Routines, o)
		}
	}
	if p.TargetNew {
		plan.Statements = append(plan.Statements, fmt.Sprintf("CREATE DATABASE %s CHARACTER SET %s COLLATE %s;", quote(dst), inspected.Charset, inspected.Collation))
	}
	for _, t := range chosen {
		plan.Statements = append(plan.Statements, strings.TrimSuffix(strings.TrimSpace(t.CreateSQL), ";")+";")
	}
	for _, c := range append(append(append([]model.MigrationCode{}, p.Routines...), p.Views...), p.Triggers...) {
		plan.Statements = append(plan.Statements, strings.TrimSuffix(strings.TrimSpace(c.CreateSQL), ";")+";")
	}
	plan.PlanHash = hashPrepared(p, route)
	p.Plan = plan
	return p, nil
}

func detailOf(err error, fallback string) string {
	if ae, ok := err.(*apperr.AppError); ok && ae.Detail == "system_schema" {
		return "system_schema"
	}
	return fallback
}

// sameUser compara `usuario`@`host` ignorando comillas y espacios.
func sameUser(a, b string) bool {
	norm := func(s string) string {
		return strings.ToLower(strings.NewReplacer("`", "", "'", "", "\"", "", " ", "").Replace(s))
	}
	return b != "" && norm(a) == norm(b)
}

// hashPrepared resume lo que Start debe encontrar igual que Plan: si el origen cambió entre ambos, stale_plan.
// Solo entra lo que el usuario vio y puede decidir. Quedan fuera los valores que cambian solos entre «Revisar» y «Migrar»: el contador
// `AUTO_INCREMENT=N` del CREATE (sube con cada insert del origen) y los workers ya recortados por las conexiones libres del servidor
// (se hashea lo que se pidió, no lo que salió).
func hashPrepared(p *Prepared, route string) string {
	h := sha256.New()
	w := func(parts ...string) {
		for _, s := range parts {
			h.Write([]byte(s))
			h.Write([]byte{0})
		}
	}
	w(p.SourceDB, p.TargetDB, route, fmt.Sprint(p.Req.Options.Workers), fmt.Sprint(p.TargetNew))
	for _, t := range p.Tables {
		w("t", t.Name, autoIncOption.ReplaceAllString(t.CreateSQL, ""), strings.Join(t.Columns, ","), t.Key.Kind, fmt.Sprint(p.StructureOnly[t.Name]))
		for _, fk := range t.ForeignKeys {
			w("fk", fk.Name, fk.RefSchema, fk.RefTable, strings.Join(fk.Columns, ","), strings.Join(fk.RefColumns, ","))
		}
	}
	w("skipped", strings.Join(p.SkippedTables, ","))
	for _, o := range p.SkippedObjects {
		w("so", o.Kind, o.Name, o.Compare)
	}
	for _, c := range append(append(append([]model.MigrationCode{}, p.Routines...), p.Views...), p.Triggers...) {
		w("o", c.Kind, c.Name, c.CreateSQL)
	}
	return hex.EncodeToString(h.Sum(nil))
}
