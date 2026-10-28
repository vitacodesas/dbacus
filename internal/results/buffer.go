package results

import (
	"container/list"
	"errors"
	"sync"

	"tabulariswails/internal/apperr"
	"tabulariswails/internal/model"
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

// Append añade una fila. Devuelve false (y marca el resultado como truncado) si el límite de memoria impide guardarla
// incluso tras expulsar ejecuciones antiguas.
func (b *Buffer) Append(row []Cell) bool {
	var size int64 = 24
	for _, c := range row {
		size += c.size()
	}
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

// Window devuelve las filas [offset, offset+limit) y el estado actual. Las filas devueltas son de solo lectura.
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
