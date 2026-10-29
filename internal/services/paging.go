package services

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/vitacodesas/dbacus/internal/apperr"
	"github.com/vitacodesas/dbacus/internal/driver"
	"github.com/vitacodesas/dbacus/internal/model"
	"github.com/vitacodesas/dbacus/internal/paging"
	"github.com/vitacodesas/dbacus/internal/results"
	"github.com/vitacodesas/dbacus/internal/session"
	"github.com/vitacodesas/dbacus/internal/sqlsplit"
)

// Paginación por cursor de clave (spec 18, ADR-005). Cada página es una lectura pequeña que sustituye el buffer de su
// sentencia dentro de la misma ejecución.

// PagingInfo describe cómo se pagina una sentencia y dónde está la página actual.
type PagingInfo struct {
	Mode       string   `json:"mode"`   // "keyset" | "approx" | "none"
	Reason     string   `json:"reason"` // solo "none": código del spec 18 §6
	PageSize   int      `json:"pageSize"`
	HasNext    bool     `json:"hasNext"`
	HasPrev    bool     `json:"hasPrev"`
	Page       int      `json:"page"`       // 1-basada si se conoce; 0 = desconocida (tras «Última»)
	KeyColumns []string `json:"keyColumns"` // columnas del cursor, en orden
	Warning    string   `json:"warning"`    // "" | "tie_at_edge" | "no_index"
	Estimated  int64    `json:"estimated"`  // ≈ filas de la tabla (solo keyset y sin WHERE); 0 = desconocido
	// Modo aproximado (spec 18 §5). Column es la columna por la que se pagina (solo approx); Candidates, las que se pueden
	// elegir con «Paginar por…» (en approx, y en none cuando el resultado llegó al límite y se podría paginar así);
	// TieNext/TiePrev, que avanzar en esa dirección se saltaría filas empatadas en el borde y por eso está bloqueado.
	Column     string   `json:"column"`
	Candidates []string `json:"candidates"`
	TieNext    bool     `json:"tieNext"`
	TiePrev    bool     `json:"tiePrev"`
}

const (
	warnNoIndex = "no_index"
	warnTie     = "tie_at_edge"
)

// PageRequest pide otra página de una sentencia ya paginada.
type PageRequest struct {
	QueryID   string `json:"queryId"`
	StmtIndex int    `json:"stmtIndex"`
	Direction string `json:"direction"` // first | next | prev | last
	Column    string `json:"column"`    // solo modo aproximado: empieza (o reinicia) la paginación por esa columna; ver Page
}

// pageState es lo que se cuelga de cada results.Buffer paginado (Buffer.Ext): el plan y los valores de borde de la
// primera y la última fila mostradas, con los que se construye la página siguiente o la anterior.
type pageState struct {
	plan             paging.Plan
	approx           *paging.Approx  // no nil: paginación aproximada (plan no se usa)
	candidates       []string        // approx: columnas elegibles
	tieNext, tiePrev bool            // approx: avanzar en esa dirección omitiría filas empatadas en el borde
	start            int64           // keyset con LIMIT propio: posición (0-basada) de la primera fila de la página en el resultado acotado
	base             StatementResult // índice, posición y SQL original de la sentencia
	hasNext, hasPrev bool
	page             int
	first, last      []string // valores de las columnas del orden en la primera y la última fila de la página
	warning          string
	estimated        int64
}

// order son las columnas del cursor de la sentencia (las del orden efectivo, o la única del modo aproximado).
func (p *pageState) order() []paging.Col {
	if p.approx != nil {
		return p.approx.Order()
	}
	return p.plan.Order
}

func (p *pageState) size() int {
	if p.approx != nil {
		return p.approx.PageSize
	}
	return p.plan.PageSize
}

func (p *pageState) info() *PagingInfo {
	pi := &PagingInfo{
		Mode: paging.ModeKeyset, PageSize: p.size(), HasNext: p.hasNext, HasPrev: p.hasPrev, Page: p.page,
		Warning: p.warning, Estimated: p.estimated,
	}
	if p.approx != nil {
		pi.Mode, pi.Column, pi.Candidates = paging.ModeApprox, p.approx.Column, p.candidates
		pi.TieNext, pi.TiePrev = p.tieNext, p.tiePrev
		pi.KeyColumns = []string{p.approx.Column}
	} else {
		pi.KeyColumns = p.plan.KeyColumns()
	}
	return pi
}

// approxOffer cuelga del buffer de una sentencia que no se pagina (modo none) pero admite el modo aproximado: guarda lo
// necesario para que `Page` con Column la empiece. Solo existe cuando el resultado llegó al límite de filas.
type approxOffer struct {
	base       StatementResult
	size       int
	candidates []string
}

// offerApprox decide si una sentencia sin paginación por cursor se puede ofrecer en modo aproximado (spec 18 §5) y, si es
// así, anota las columnas elegibles en `info`. Si no se puede, ajusta el motivo cuando hay uno más preciso.
func offerApprox(info *PagingInfo, text string, cols []model.ColumnMeta) []string {
	switch info.Reason {
	case sqlsplit.ReasonNotSelect, sqlsplit.ReasonUserLimit, sqlsplit.ReasonIntoLock:
		return nil
	}
	if _, reason := sqlsplit.Wrap(text); reason != "" {
		info.Reason = reason
		return nil
	}
	names, reason := paging.Candidates(cols)
	if reason != "" {
		if info.Reason == sqlsplit.ReasonMultiTable {
			info.Reason = reason // lo útil para quien pensaba usar «Paginación aproximada»
		}
		return nil
	}
	info.Candidates = names
	return names
}

// result es el StatementResult de la página que hay ahora en el buffer.
func (p *pageState) result(buf *results.Buffer) StatementResult {
	r := p.base
	r.Kind, r.Columns = "rows", buf.Columns()
	r.RowCount, r.Truncated = int64(buf.Len()), buf.Truncated()
	r.LimitApplied = 0
	r.Paging = p.info()
	return r
}

// planPaging decide cómo paginar una sentencia. known=false significa «no se puede saber» (sin servicio de esquema, tabla
// que no se puede describir…): quien llama sigue con el LIMIT automático de siempre y el servidor dará el error que toque.
func (s *QueryService) planPaging(ctx context.Context, sess *session.Session, text, db string, size int) (dec paging.Decision, detail model.TableDetail, known bool) {
	parts, reason := sqlsplit.SplitSelect(text)
	if reason != "" {
		return paging.Decision{Mode: paging.ModeNone, Reason: reason}, model.TableDetail{}, true
	}
	if s.d.Schema == nil {
		return paging.Decision{}, model.TableDetail{}, false
	}
	dbName := parts.Database
	if dbName == "" {
		dbName = db
	}
	if dbName == "" {
		return paging.Decision{}, model.TableDetail{}, false
	}
	detail, err := s.d.Schema.DescribeTable(ctx, sess.ID, dbName, parts.Table)
	if err != nil {
		return paging.Decision{}, model.TableDetail{}, false
	}
	dec = paging.Decide(parts, detail, size)
	if dec.Mode == paging.ModeKeyset {
		// La tabla va calificada con su base: la página no depende de la base vigente de la conexión en ese momento.
		q := sess.Driver.QuoteIdent
		dec.Plan.From = q(dbName) + "." + q(parts.Table)
	}
	return dec, detail, true
}

// newPageState prepara el estado de una sentencia paginable (todavía sin página).
func newPageState(plan paging.Plan, detail model.TableDetail, base StatementResult) *pageState {
	st := &pageState{plan: plan, base: base}
	if plan.Where == "" && plan.Cap == 0 && detail.RowEstimate != nil && *detail.RowEstimate > 0 {
		st.estimated = *detail.RowEstimate
	}
	return st
}

// readPage lee del cursor las filas de una página: PageSize más una de sondeo (`probe`, nil si no hubo), que solo dice si hay
// más y no se guarda. Para las páginas leídas hacia atrás las filas llegan en orden inverso y se invierten aquí.
func readPage(res driver.RunResult, cols []model.ColumnMeta, size int, dir paging.Dir) (rows [][]results.Cell, probe []results.Cell, err error) {
	all := make([][]results.Cell, 0, size+1)
	for res.Next() {
		row := res.Row()
		cells := make([]results.Cell, len(row))
		for j, v := range row {
			cells[j] = results.Convert(v, cols[j].DataType)
		}
		all = append(all, cells)
		if len(all) > size { // ya está la fila de sondeo: el servidor no manda más (LIMIT size+1)
			break
		}
	}
	if len(all) <= size {
		if err := res.Err(); err != nil {
			return nil, nil, err
		}
	}
	rows, more := paging.Page(all, size)
	if more {
		probe = all[size]
	}
	if dir.Backward() {
		paging.Reverse(rows)
	}
	return rows, probe, nil
}

// cellLiteral devuelve el texto exacto de una celda numérica para usarlo como valor de borde.
func cellLiteral(c results.Cell) (string, bool) {
	switch v := c.V.(type) {
	case int64:
		return strconv.FormatInt(v, 10), true
	case string: // BIGINT fuera del rango seguro de JS y DECIMAL llegan como texto exacto
		return v, paging.ValidLiteral(v)
	}
	return "", false
}

// edgeValues lee, en una fila, el valor de cada columna del orden.
func edgeValues(cols []model.ColumnMeta, row []results.Cell, order []paging.Col) ([]string, error) {
	out := make([]string, len(order))
	for i, o := range order {
		idx := -1
		for j, c := range cols {
			if strings.EqualFold(c.Name, o.Name) {
				idx = j
				break
			}
		}
		if idx < 0 || idx >= len(row) {
			return nil, apperr.Newf(apperr.Internal, "La columna %q del cursor no está en el resultado", o.Name)
		}
		v, ok := cellLiteral(row[idx])
		if !ok {
			return nil, apperr.Newf(apperr.Internal, "El valor de la columna %q no se puede usar como cursor", o.Name)
		}
		out[i] = v
	}
	return out, nil
}

// fillPage guarda las filas en el buffer y anota en el estado los valores de borde. Devuelve false si el límite de memoria
// impidió guardarlas todas.
func (p *pageState) fillPage(buf *results.Buffer, rows [][]results.Cell) (bool, error) {
	cols := buf.Columns()
	p.first, p.last = nil, nil
	if len(rows) > 0 {
		var err error
		if p.first, err = edgeValues(cols, rows[0], p.order()); err != nil {
			return false, err
		}
		if p.last, err = edgeValues(cols, rows[len(rows)-1], p.order()); err != nil {
			return false, err
		}
	}
	ok := true
	for _, r := range rows {
		if !buf.Append(r) {
			ok = false
			break
		}
	}
	buf.Finish(!ok)
	return ok, nil
}

// explainUsesFilesort pregunta al servidor si la página necesita ordenar sin índice: en ese caso cada página cuesta como
// un OFFSET y se avisa (spec 18 §9). Es informativo: cualquier fallo se ignora.
func explainUsesFilesort(ctx context.Context, conn driver.Conn, sqlText string) bool {
	res, err := conn.Run(ctx, "EXPLAIN "+sqlText)
	if err != nil {
		return false
	}
	defer res.Close()
	extra := -1
	for i, c := range res.Columns() {
		if strings.EqualFold(c.Name, "Extra") {
			extra = i
		}
	}
	if extra < 0 {
		return false
	}
	for res.Next() {
		row := res.Row()
		if extra >= len(row) {
			continue
		}
		var text string
		switch v := row[extra].(type) {
		case string:
			text = v
		case []byte:
			text = string(v)
		}
		if strings.Contains(strings.ToLower(text), "filesort") {
			return true
		}
	}
	return false
}

// Page lee otra página de una sentencia paginada y la deja en lugar de la actual, dentro de la misma ejecución (spec 18 §7).
// Es síncrona, se puede cancelar con Cancel(queryID) y no se anota en el historial. Si la dirección no tiene más filas,
// deja la página actual como estaba. Las demás sentencias del script no se tocan.
func (s *QueryService) Page(ctx context.Context, req PageRequest) (StatementResult, error) {
	dir := paging.Dir(req.Direction)
	switch dir {
	case paging.First, paging.Next, paging.Prev, paging.Last:
	default:
		return StatementResult{}, invalid("direction", "La dirección debe ser first, next, prev o last")
	}
	run, err := s.d.Results.Get(req.QueryID)
	if err != nil {
		return StatementResult{}, toAppErr(err)
	}
	buf, err := run.Buffer(req.StmtIndex)
	if err != nil {
		return StatementResult{}, toAppErr(err)
	}
	cur, _ := buf.Ext().(*pageState)
	offer, _ := buf.Ext().(*approxOffer)
	if cur == nil && offer == nil {
		return StatementResult{}, invalid("statement", "Esta sentencia no se pagina por cursor")
	}
	if req.Column != "" {
		// «Paginar por…»: empieza (o reinicia con otra columna) la paginación aproximada desde la primera página.
		next, err := startApprox(buf, cur, offer, req.Column)
		if err != nil {
			return StatementResult{}, err
		}
		cur, dir = next, paging.First
	} else if cur == nil {
		return StatementResult{}, invalid("column", "Elige la columna por la que paginar")
	}
	sess, err := s.d.Sessions.Get(run.SessionID)
	if err != nil {
		return StatementResult{}, toAppErr(err)
	}
	// Las ediciones pendientes se indexan por posición de fila del buffer: cambiar de página las perdería. La interfaz
	// pide confirmación y las descarta antes; aquí se impide por si no lo hiciera.
	if buf.PendingCount() > 0 || buf.InsertCount() > 0 || buf.DeleteCount() > 0 {
		return StatementResult{}, invalid("pending", "Hay cambios sin aplicar en esta página")
	}
	// No hay página en esa dirección: se queda la actual.
	if (dir == paging.Next && !cur.hasNext) || (dir == paging.Prev && !cur.hasPrev) {
		return cur.result(buf), nil
	}
	// Avanzar con valores repetidos en el borde omitiría filas en silencio: la interfaz lo bloquea y aquí se impide también.
	if (dir == paging.Next && cur.tieNext) || (dir == paging.Prev && cur.tiePrev) {
		return StatementResult{}, invalid(warnTie, "Hay valores repetidos en el borde de la página: avanzar omitiría filas. Cambia la columna o usa Sin límite")
	}

	var edge []string
	switch dir {
	case paging.Next:
		edge = cur.last
	case paging.Prev:
		edge = cur.first
	}
	// Con un LIMIT propio (spec 18 §4) las páginas se colocan por posición dentro del tope.
	capped := cur.approx == nil && cur.plan.Cap > 0
	n, probe := cur.size(), true // filas que se muestran y si hace falta la fila de sondeo
	var nextStart int64          // posición de la primera fila de la página pedida (solo con tope)
	if capped {
		switch dir {
		case paging.Next:
			nextStart = cur.start + int64(buf.Len())
		case paging.Prev:
			nextStart = max(cur.start-int64(cur.size()), 0)
		}
		if dir != paging.Last {
			n, probe = cur.plan.Window(nextStart)
		}
		if n == 0 { // se pidió pasar del tope: no hay más
			cur.hasNext = false
			return cur.result(buf), nil
		}
	}

	runCtx, cancel := context.WithCancel(sess.Context())
	stopAfter := context.AfterFunc(s.baseCtx, cancel)
	ar := &activeRun{queryID: req.QueryID, tabID: run.TabID, sessionID: run.SessionID, cancel: cancel, done: make(chan struct{})}
	s.mu.Lock()
	if s.byTab[run.TabID] != nil {
		s.mu.Unlock()
		stopAfter()
		cancel()
		return StatementResult{}, apperr.New(apperr.QueryFailed, "Ya hay una ejecución en curso en esta pestaña").WithDetail("busy")
	}
	s.byTab[run.TabID], s.byQuery[req.QueryID] = ar, ar
	s.mu.Unlock()
	defer func() {
		stopAfter()
		cancel()
		s.mu.Lock()
		if s.byTab[run.TabID] == ar {
			delete(s.byTab, run.TabID)
		}
		if s.byQuery[req.QueryID] == ar {
			delete(s.byQuery, req.QueryID)
		}
		s.mu.Unlock()
		close(ar.done)
	}()

	started := time.Now()
	readDir := dir
	var sqlText string
	if capped && dir == paging.Last {
		// «Última» con tope: se cuentan las filas del resultado acotado y se lee por posición (no se puede leer desde el final
		// de la tabla: el tope manda).
		total, cerr := s.countCapped(runCtx, ar, sess, run.TabID, cur.plan)
		if cerr != nil {
			if ar.canceled.Load() {
				return StatementResult{}, apperr.Wrap(apperr.QueryCanceled, "Consulta cancelada", cerr)
			}
			return StatementResult{}, apperr.From(cerr)
		}
		nextStart = cur.plan.LastStart(total)
		if total == 0 {
			dir, readDir = paging.First, paging.First
			n, probe = cur.plan.Window(0)
		} else {
			readDir = paging.First
			n, probe = cur.plan.Window(nextStart)
			probe = false
		}
		sqlText, err = cur.plan.BuildN(paging.First, nil, sess.Driver.QuoteIdent, n, nextStart)
	} else {
		limit := n
		if probe {
			limit++
		}
		if capped {
			sqlText, err = cur.plan.BuildN(dir, edge, sess.Driver.QuoteIdent, limit, 0)
		} else {
			sqlText, err = cur.build(dir, edge, sess.Driver.QuoteIdent)
		}
	}
	if err != nil {
		return StatementResult{}, apperr.Wrap(apperr.Internal, "No se pudo preparar la página", err)
	}
	rows, probeRow, cols, err := s.fetchPage(runCtx, ar, sess, run.TabID, sqlText, n, readDir)
	if err != nil {
		if ar.canceled.Load() {
			return StatementResult{}, apperr.Wrap(apperr.QueryCanceled, "Consulta cancelada", err)
		}
		return StatementResult{}, apperr.From(err)
	}
	more := probeRow != nil
	// Se pidió «siguiente»/«anterior» y ya no queda nada (se borraron filas): se queda la página actual.
	if len(rows) == 0 && (dir == paging.Next || dir == paging.Prev) {
		if dir == paging.Next {
			cur.hasNext = false
		} else {
			cur.hasPrev = false
		}
		return cur.result(buf), nil
	}

	srcSQL, srcDB := buf.Source()
	nb, err := run.ReplaceBuffer(req.StmtIndex, cols)
	if err != nil {
		return StatementResult{}, toAppErr(err)
	}
	nb.SetSource(srcSQL, srcDB)

	next := &pageState{plan: cur.plan, approx: cur.approx, candidates: cur.candidates, base: cur.base, warning: cur.warning, estimated: cur.estimated}
	switch dir {
	case paging.First:
		next.hasNext, next.hasPrev, next.page = more, false, 1
	case paging.Next:
		next.hasNext, next.hasPrev = more, true
		if cur.page > 0 {
			next.page = cur.page + 1
		}
	case paging.Prev:
		next.hasNext, next.hasPrev = true, more
		switch {
		case !more:
			next.page = 1 // no hay nada antes: es la primera
		case cur.page > 1:
			next.page = cur.page - 1
		}
	case paging.Last:
		next.hasNext, next.hasPrev = false, more
		if !more {
			next.page = 1 // cabe todo en una página
		}
	}
	if capped {
		// La posición manda: el número de página siempre se conoce y hacia atrás se llega al principio justo en la posición 0.
		next.start = nextStart
		next.page = int(nextStart/int64(cur.size())) + 1
		switch dir {
		case paging.Prev:
			next.hasPrev = nextStart > 0
		case paging.Last:
			next.hasNext, next.hasPrev = false, nextStart > 0
		}
	}
	if _, err := next.fillPage(nb, rows); err != nil {
		return StatementResult{}, toAppErr(err)
	}
	if next.approx != nil {
		if err := next.markTies(cols, dir, probeRow); err != nil {
			return StatementResult{}, toAppErr(err)
		}
	}
	nb.SetExt(next)
	next.base.ElapsedMs = time.Since(started).Milliseconds()
	return next.result(nb), nil
}

// fetchPage ejecuta la consulta de una página y lee sus filas, reintentando una vez si la conexión de la pestaña había
// muerto (igual que Execute).
func (s *QueryService) fetchPage(ctx context.Context, ar *activeRun, sess *session.Session, tabID, sqlText string, size int, dir paging.Dir) ([][]results.Cell, []results.Cell, []model.ColumnMeta, error) {
	for attempt := 0; ; attempt++ {
		conn, err := sess.AcquireConn(ctx, tabID)
		if err == nil {
			ar.setConn(conn)
			var res driver.RunResult
			if res, err = conn.Run(ctx, sqlText); err == nil {
				cols := res.Columns()
				if len(cols) == 0 {
					_ = res.Close()
					return nil, nil, nil, apperr.New(apperr.Internal, "La página no devolvió columnas")
				}
				rows, probe, rerr := readPage(res, cols, size, dir)
				_ = res.Close()
				return rows, probe, cols, rerr
			}
		}
		if attempt == 0 && connLost(err) && ctx.Err() == nil {
			_ = sess.CloseTab(tabID)
			continue
		}
		return nil, nil, nil, err
	}
}

// pageSizeFor limita el tamaño de página al tope de filas del usuario.
func pageSizeFor(autoLimit, maxRows int) int {
	size := autoLimit
	if maxRows > 0 && size > maxRows {
		size = maxRows
	}
	return size
}

// firstPage lee la primera página de una sentencia paginable en el buffer que ya abrió runStatement y deja el estado para
// pedir las siguientes. El historial y la invalidación de esquema los hace el llamador con el resultado devuelto.
func (s *QueryService) firstPage(res driver.RunResult, cols []model.ColumnMeta, buf *results.Buffer, st *pageState, out StatementResult,
	started time.Time) (StatementResult, error) {
	n, _ := st.plan.Window(0)
	rows, probe, err := readPage(res, cols, n, paging.First)
	if err != nil {
		buf.Finish(false)
		return out, err
	}
	st.hasNext, st.page = probe != nil, 1
	st.base = out
	if _, err := st.fillPage(buf, rows); err != nil {
		return out, err
	}
	buf.SetExt(st)
	st.base.ElapsedMs = time.Since(started).Milliseconds()
	return st.result(buf), nil
}

// build es la consulta de una página según el modo de la sentencia.
func (p *pageState) build(d paging.Dir, edge []string, quote func(string) string) (string, error) {
	if p.approx == nil {
		return p.plan.Build(d, edge, quote)
	}
	e := ""
	if len(edge) > 0 {
		e = edge[0]
	}
	return p.approx.Build(d, e, quote)
}

// markTies aplica la regla de empate del modo aproximado (spec 18 §5) a la página recién leída. Hacia delante, la fila de
// sondeo viene justo detrás de la última; hacia atrás, justo delante de la primera. Si vale lo mismo que ese borde, la
// consulta de la página siguiente (`columna > borde`) se saltaría las filas que empatan, así que ese avance se bloquea.
// En la otra dirección no hay empate posible: la página se pidió con comparación estricta contra la que acabamos de dejar.
func (p *pageState) markTies(cols []model.ColumnMeta, dir paging.Dir, probe []results.Cell) error {
	p.tieNext, p.tiePrev = false, false
	if probe != nil && len(p.first) > 0 {
		pv, err := edgeValues(cols, probe, p.order())
		if err != nil {
			return err
		}
		if dir.Backward() {
			p.tiePrev = paging.TieAtEdge(p.first[0], pv[0])
		} else {
			p.tieNext = paging.TieAtEdge(p.last[0], pv[0])
		}
	}
	p.warning = ""
	if p.tieNext || p.tiePrev {
		p.warning = warnTie
	}
	return nil
}

// startApprox prepara el estado para paginar en modo aproximado por `column`, a partir de una sentencia sin paginar
// (con oferta) o ya aproximada (cambio de columna). Las columnas elegibles son las del resultado; un nombre que no lo sea
// se rechaza (nunca llega a SQL sin pasar por esta lista).
func startApprox(buf *results.Buffer, cur *pageState, offer *approxOffer, column string) (*pageState, error) {
	var (
		base       StatementResult
		size       int
		candidates []string
	)
	switch {
	case cur != nil && cur.approx != nil:
		base, size, candidates = cur.base, cur.approx.PageSize, cur.candidates
	case offer != nil:
		base, size, candidates = offer.base, offer.size, offer.candidates
	default:
		return nil, invalid("column", "Esta sentencia se pagina por clave: no admite elegir columna")
	}
	var name string
	for _, c := range candidates {
		if strings.EqualFold(c, column) {
			name = c
		}
	}
	if name == "" {
		return nil, invalid("column", "La columna no sirve para paginar: debe ser numérica y no admitir NULL")
	}
	src, _ := buf.Source()
	inner, reason := sqlsplit.Wrap(src)
	if reason != "" {
		return nil, invalid("statement", "Esta sentencia no se puede paginar: "+reason)
	}
	return &pageState{approx: &paging.Approx{Inner: inner, Column: name, PageSize: size}, candidates: candidates, base: base}, nil
}

// countCapped cuenta las filas de un resultado con LIMIT propio (como mucho el tope), para saber dónde empieza su última página.
func (s *QueryService) countCapped(ctx context.Context, ar *activeRun, sess *session.Session, tabID string, plan paging.Plan) (int64, error) {
	rows, _, _, err := s.fetchPage(ctx, ar, sess, tabID, plan.CountSQL(), 1, paging.First)
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 || len(rows[0]) == 0 {
		return 0, apperr.New(apperr.Internal, "No se pudo contar las filas del resultado")
	}
	lit, ok := cellLiteral(rows[0][0])
	n, perr := strconv.ParseInt(lit, 10, 64)
	if !ok || perr != nil {
		return 0, apperr.New(apperr.Internal, "El recuento de filas no es un número")
	}
	return n, nil
}
