package results

import (
	"container/list"
	"errors"
	"sort"
	"sync"

	"github.com/vitacodesas/dbacus/internal/apperr"
	"github.com/vitacodesas/dbacus/internal/model"
)

// ErrExpired indica que el resultado ya no está en memoria (liberado o expulsado por el límite global).
var ErrExpired = apperr.New(apperr.ResultExpired, "El resultado ya no está disponible: vuelve a ejecutar la consulta")

// Manager lleva la memoria total de los resultados y expulsa los más antiguos (LRU) cuando se supera el límite (spec 06).
type Manager struct {
	mu    sync.Mutex
	limit int64
	total int64
	runs  map[string]*Run
	lru   *list.List // frente = más reciente
}

func NewManager(limitBytes int64) *Manager {
	return &Manager{limit: limitBytes, runs: map[string]*Run{}, lru: list.New()}
}

func (m *Manager) SetLimit(limitBytes int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.limit = limitBytes
}

// Total devuelve los bytes estimados en uso.
func (m *Manager) Total() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.total
}

// Run es el conjunto de resultados de una ejecución (una sentencia = un Buffer).
type Run struct {
	ID        string
	TabID     string
	SessionID string // lo fija quien crea la ejecución; sirve para elegir el driver al generar SQL desde el resultado

	m       *Manager
	elem    *list.Element
	stmts   []*Buffer
	bytes   int64
	done    bool // solo las ejecuciones terminadas son expulsables
	evicted bool
}

func (m *Manager) NewRun(id, tabID string) *Run {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := &Run{ID: id, TabID: tabID, m: m}
	r.elem = m.lru.PushFront(r)
	m.runs[id] = r
	return r
}

// Get devuelve la ejecución y la marca como usada; ErrExpired si no existe o fue expulsada.
func (m *Manager) Get(id string) (*Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.runs[id]
	if !ok || r.evicted {
		return nil, ErrExpired
	}
	m.lru.MoveToFront(r.elem)
	return r, nil
}

func (m *Manager) Release(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.runs[id]; ok {
		m.dropLocked(r)
	}
}

// ReleaseTab libera todas las ejecuciones de una pestaña.
func (m *Manager) ReleaseTab(tabID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.runs {
		if r.TabID == tabID {
			m.dropLocked(r)
		}
	}
}

func (m *Manager) dropLocked(r *Run) {
	m.total -= r.bytes
	r.bytes = 0
	r.stmts = nil
	r.evicted = true
	m.lru.Remove(r.elem)
	delete(m.runs, r.ID)
}

// evictLocked libera ejecuciones terminadas, de la más antigua a la más reciente, hasta que quepan `need` bytes más.
// `keep` nunca se expulsa. Devuelve true si ya caben.
func (m *Manager) evictLocked(keep *Run, need int64) bool {
	for e := m.lru.Back(); e != nil && m.total+need > m.limit; {
		prev := e.Prev()
		if r := e.Value.(*Run); r != keep && r.done {
			m.dropLocked(r)
		}
		e = prev
	}
	return m.total+need <= m.limit
}

// MarkDone declara terminada la ejecución: a partir de aquí puede ser expulsada.
func (r *Run) MarkDone() {
	r.m.mu.Lock()
	defer r.m.mu.Unlock()
	r.done = true
}

// NewBuffer abre el resultado de la sentencia siguiente.
func (r *Run) NewBuffer(cols []model.ColumnMeta) *Buffer {
	r.m.mu.Lock()
	defer r.m.mu.Unlock()
	b := &Buffer{run: r, cols: cols}
	r.stmts = append(r.stmts, b)
	return b
}

// ReplaceBuffer sustituye el resultado de la sentencia i por uno vacío con las columnas dadas y descuenta la memoria del
// anterior (paginación por cursor: cada página reemplaza a la anterior dentro de la misma ejecución, spec 18). Quien aún
// tenga el buffer viejo lo sigue leyendo sin problema: deja de contar y de ser alcanzable desde la ejecución.
func (r *Run) ReplaceBuffer(i int, cols []model.ColumnMeta) (*Buffer, error) {
	r.m.mu.Lock()
	defer r.m.mu.Unlock()
	if r.evicted {
		return nil, ErrExpired
	}
	if i < 0 || i >= len(r.stmts) {
		return nil, apperr.Newf(apperr.NotFound, "La sentencia %d no existe en esta ejecución", i)
	}
	var freed int64
	for _, row := range r.stmts[i].rows {
		freed += rowSize(row)
	}
	r.bytes -= freed
	r.m.total -= freed
	b := &Buffer{run: r, cols: cols}
	r.stmts[i] = b
	r.m.lru.MoveToFront(r.elem)
	return b, nil
}

// rowSize es el tamaño estimado de una fila en el buffer (el mismo cálculo que usa Append).
func rowSize(row []Cell) int64 {
	var size int64 = 24
	for _, c := range row {
		size += c.size()
	}
	return size
}

func (r *Run) Buffer(i int) (*Buffer, error) {
	r.m.mu.Lock()
	defer r.m.mu.Unlock()
	if r.evicted {
		return nil, ErrExpired
	}
	if i < 0 || i >= len(r.stmts) {
		return nil, apperr.Newf(apperr.NotFound, "La sentencia %d no existe en esta ejecución", i)
	}
	r.m.lru.MoveToFront(r.elem)
	return r.stmts[i], nil
}

// Buffer guarda las filas ya serializadas de una sentencia.
type Buffer struct {
	run       *Run
	cols      []model.ColumnMeta
	rows      [][]Cell
	truncated bool
	complete  bool
	srcSQL    string
	srcDB     string
	ext       any                // estado que adjunta quien crea el buffer (la paginación por cursor, spec 18); opaco aquí
	pending   map[[2]int]Pending // cambios de celda sin aplicar (spec 13); mueren con el buffer
	inserts   []*NewRow          // filas nuevas sin aplicar (spec 14): ocupan los índices tras la última fila real
	deletes   map[int]any        // filas marcadas para borrar (spec 14) → datos opacos de quien las marcó
}

// Pending es un cambio de celda sin aplicar. Cell es lo que se muestra (valor nuevo, con Edited) y Data es opaco para
// este paquete: lo usa la edición para guardar lo que necesita al aplicar.
type Pending struct {
	Row, Col int
	Cell     Cell
	Data     any
}

// Stage deja un cambio pendiente en (row, col); sustituye el anterior de esa celda.
func (b *Buffer) Stage(row, col int, shown Cell, data any) error {
	m := b.run.m
	m.mu.Lock()
	defer m.mu.Unlock()
	if b.run.evicted {
		return ErrExpired
	}
	if row < 0 || row >= len(b.rows) || col < 0 || col >= len(b.rows[row]) {
		return errors.New("celda fuera de rango")
	}
	if b.pending == nil {
		b.pending = map[[2]int]Pending{}
	}
	shown.Edited = true
	b.pending[[2]int{row, col}] = Pending{Row: row, Col: col, Cell: shown, Data: data}
	return nil
}

// Unstage retira el cambio pendiente de una celda (no hace nada si no lo había).
func (b *Buffer) Unstage(row, col int) {
	m := b.run.m
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(b.pending, [2]int{row, col})
}

// ClearPending descarta todos los cambios pendientes.
func (b *Buffer) ClearPending() {
	m := b.run.m
	m.mu.Lock()
	defer m.mu.Unlock()
	b.pending, b.inserts, b.deletes = nil, nil, nil
}

// PendingCount es el número de celdas con cambios pendientes.
func (b *Buffer) PendingCount() int {
	m := b.run.m
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(b.pending)
}

// PendingList devuelve los cambios pendientes ordenados por fila y columna.
func (b *Buffer) PendingList() []Pending {
	m := b.run.m
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Pending, 0, len(b.pending))
	for _, p := range b.pending {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Row != out[j].Row {
			return out[i].Row < out[j].Row
		}
		return out[i].Col < out[j].Col
	})
	return out
}

// SetSource anota la sentencia y la base que produjeron el resultado, para poder re-ejecutarla al exportar (spec 07).
func (b *Buffer) SetSource(sql, database string) {
	b.run.m.mu.Lock()
	b.srcSQL, b.srcDB = sql, database
	b.run.m.mu.Unlock()
}

// Source devuelve la sentencia original (sin LIMIT automático) y la base en la que se ejecutó.
func (b *Buffer) Source() (sql, database string) {
	b.run.m.mu.Lock()
	defer b.run.m.mu.Unlock()
	return b.srcSQL, b.srcDB
}

func (b *Buffer) Columns() []model.ColumnMeta { return b.cols }

// SetExt adjunta al buffer un estado opaco de quien lo creó; Ext lo devuelve. Muere con el buffer.
func (b *Buffer) SetExt(v any) {
	b.run.m.mu.Lock()
	b.ext = v
	b.run.m.mu.Unlock()
}

func (b *Buffer) Ext() any {
	b.run.m.mu.Lock()
	defer b.run.m.mu.Unlock()
	return b.ext
}

// Append añade una fila. Devuelve false (y marca el resultado como truncado) si el límite de memoria impide guardarla
// incluso tras expulsar ejecuciones antiguas.
func (b *Buffer) Append(row []Cell) bool {
	size := rowSize(row)
	m := b.run.m
	m.mu.Lock()
	defer m.mu.Unlock()
	if b.run.evicted {
		return false
	}
	if m.total+size > m.limit && !m.evictLocked(b.run, size) {
		b.truncated = true
		return false
	}
	b.rows = append(b.rows, row)
	b.run.bytes += size
	m.total += size
	m.lru.MoveToFront(b.run.elem)
	return true
}

// Finish marca el resultado como completo; truncated indica que se cortó (límite de filas o de memoria).
func (b *Buffer) Finish(truncated bool) {
	m := b.run.m
	m.mu.Lock()
	defer m.mu.Unlock()
	b.complete = true
	b.truncated = b.truncated || truncated
}

func (b *Buffer) Len() int {
	m := b.run.m
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(b.rows)
}

func (b *Buffer) Truncated() bool {
	m := b.run.m
	m.mu.Lock()
	defer m.mu.Unlock()
	return b.truncated
}

// Window devuelve las filas [offset, offset+limit) tal como están en el buffer (sin cambios pendientes) y el estado actual.
// Las filas devueltas son de solo lectura. Exportar y generar INSERT usan esta vista; el grid usa WindowView.
func (b *Buffer) Window(offset, limit int) (rows [][]Cell, total int, complete bool, err error) {
	m := b.run.m
	m.mu.Lock()
	defer m.mu.Unlock()
	if b.run.evicted {
		return nil, 0, false, ErrExpired
	}
	m.lru.MoveToFront(b.run.elem)
	total = len(b.rows)
	if offset < 0 {
		offset = 0
	}
	if offset > total {
		offset = total
	}
	end := offset + limit
	if limit < 0 || end > total {
		end = total
	}
	return b.rows[offset:end:end], total, b.complete, nil
}

// Cell devuelve una celda (con su valor completo accesible vía Cell.Full).
func (b *Buffer) Cell(row, col int) (Cell, error) {
	m := b.run.m
	m.mu.Lock()
	defer m.mu.Unlock()
	if b.run.evicted {
		return Cell{}, ErrExpired
	}
	if row < 0 || row >= len(b.rows) || col < 0 || col >= len(b.rows[row]) {
		return Cell{}, errors.New("celda fuera de rango")
	}
	return b.rows[row][col], nil
}

// CommitPending convierte los cambios pendientes en valores reales del buffer (tras aplicarlos con éxito) y los limpia.
func (b *Buffer) CommitPending() {
	m := b.run.m
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range b.pending {
		if p.Row < len(b.rows) && p.Col < len(b.rows[p.Row]) {
			row := append([]Cell(nil), b.rows[p.Row]...) // copia: las ventanas ya entregadas no deben cambiar bajo los pies de nadie
			c := p.Cell
			c.Edited = false
			row[p.Col] = c
			b.rows[p.Row] = row
		}
	}
	b.pending = nil
}

// ReplaceRow sustituye una fila entera (p. ej. releída del servidor tras una edición) y ajusta la memoria contabilizada.
func (b *Buffer) ReplaceRow(row int, cells []Cell) error {
	m := b.run.m
	m.mu.Lock()
	defer m.mu.Unlock()
	if b.run.evicted {
		return ErrExpired
	}
	if row < 0 || row >= len(b.rows) || len(cells) != len(b.rows[row]) {
		return errors.New("fila fuera de rango o con otro número de columnas")
	}
	var oldSize, newSize int64
	for _, c := range b.rows[row] {
		oldSize += c.size()
	}
	for _, c := range cells {
		newSize += c.size()
	}
	b.rows[row] = cells
	b.run.bytes += newSize - oldSize
	m.total += newSize - oldSize
	return nil
}
