package results

import (
	"errors"
	"sort"
)

// NewRow es una fila nueva sin aplicar (spec 14). Cells es lo que se muestra: las celdas sin valor llevan T = TDefault
// (el servidor usará su DEFAULT); Set dice cuáles tiene el usuario escritas; Data es opaco para este paquete.
type NewRow struct {
	Cells []Cell
	Set   []bool
	Data  []any
}

// TDefault marca una celda de fila nueva a la que no se le ha dado valor.
const TDefault = "default"

// NewRowView es una copia de una fila nueva con su índice absoluto en el resultado.
type NewRowView struct {
	Row int
	NewRow
}

// DeletedRow es una fila marcada para borrar.
type DeletedRow struct {
	Row  int
	Data any
}

// View es una ventana de filas tal como las ve el usuario: con los cambios pendientes superpuestos.
type View struct {
	Rows     [][]Cell
	Total    int // filas reales + filas nuevas
	Complete bool
	Deleted  []int // índices absolutos (dentro de la ventana) de las filas marcadas para borrar
	NewFrom  int   // índice de la primera fila nueva, o -1
}

func unsetCell() Cell { return Cell{T: TDefault, Edited: true} }

// WindowView devuelve las filas [offset, offset+limit) con las celdas editadas, las filas nuevas (tras la última real) y
// las marcas de borrado. No toca el buffer: las filas originales siguen siendo con las que se comprueba el conflicto.
func (b *Buffer) WindowView(offset, limit int) (View, error) {
	m := b.run.m
	m.mu.Lock()
	defer m.mu.Unlock()
	if b.run.evicted {
		return View{}, ErrExpired
	}
	m.lru.MoveToFront(b.run.elem)
	real := len(b.rows)
	total := real + len(b.inserts)
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
	v := View{Total: total, Complete: b.complete, NewFrom: -1, Rows: make([][]Cell, 0, end-offset)}
	if len(b.inserts) > 0 {
		v.NewFrom = real
	}
	for i := offset; i < end; i++ {
		var row []Cell
		if i < real {
			row = b.rows[i]
			// Copia solo si alguna celda de la fila tiene un cambio pendiente.
			if len(b.pending) > 0 {
				copied := false
				for c := range row {
					if p, ok := b.pending[[2]int{i, c}]; ok {
						if !copied {
							row, copied = append([]Cell(nil), row...), true
						}
						row[c] = p.Cell
					}
				}
			}
			if _, del := b.deletes[i]; del {
				v.Deleted = append(v.Deleted, i)
			}
		} else {
			row = append([]Cell(nil), b.inserts[i-real].Cells...)
		}
		v.Rows = append(v.Rows, row)
	}
	return v, nil
}

// ---- filas nuevas -------------------------------------------------------------------------------------------------

// AddNewRow añade una fila nueva vacía (todas las celdas «DEFAULT») y devuelve su índice absoluto. Solo con el resultado
// completo: si aún llegaban filas, los índices de las nuevas se desplazarían.
func (b *Buffer) AddNewRow(cols int) (int, error) {
	m := b.run.m
	m.mu.Lock()
	defer m.mu.Unlock()
	if b.run.evicted {
		return 0, ErrExpired
	}
	if !b.complete {
		return 0, errors.New("el resultado aún se está leyendo")
	}
	n := &NewRow{Cells: make([]Cell, cols), Set: make([]bool, cols), Data: make([]any, cols)}
	for i := range n.Cells {
		n.Cells[i] = unsetCell()
	}
	b.inserts = append(b.inserts, n)
	return len(b.rows) + len(b.inserts) - 1, nil
}

func (b *Buffer) newRowLocked(row int) (*NewRow, error) {
	i := row - len(b.rows)
	if i < 0 || i >= len(b.inserts) {
		return nil, errors.New("la fila nueva no existe")
	}
	return b.inserts[i], nil
}

// SetNewCell da valor a una celda de una fila nueva (shown es lo que se ve; data es opaco).
func (b *Buffer) SetNewCell(row, col int, shown Cell, data any) error {
	m := b.run.m
	m.mu.Lock()
	defer m.mu.Unlock()
	n, err := b.newRowLocked(row)
	if err != nil {
		return err
	}
	if col < 0 || col >= len(n.Cells) {
		return errors.New("celda fuera de rango")
	}
	shown.Edited = true
	n.Cells[col], n.Set[col], n.Data[col] = shown, true, data
	return nil
}

// UnsetNewCell devuelve una celda de fila nueva a «DEFAULT».
func (b *Buffer) UnsetNewCell(row, col int) {
	m := b.run.m
	m.mu.Lock()
	defer m.mu.Unlock()
	if n, err := b.newRowLocked(row); err == nil && col >= 0 && col < len(n.Cells) {
		n.Cells[col], n.Set[col], n.Data[col] = unsetCell(), false, nil
	}
}

// RemoveNewRow descarta una fila nueva; las siguientes suben un índice.
func (b *Buffer) RemoveNewRow(row int) {
	m := b.run.m
	m.mu.Lock()
	defer m.mu.Unlock()
	if i := row - len(b.rows); i >= 0 && i < len(b.inserts) {
		b.inserts = append(b.inserts[:i], b.inserts[i+1:]...)
	}
}

// NewRows devuelve una copia de las filas nuevas, en orden.
func (b *Buffer) NewRows() []NewRowView {
	m := b.run.m
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]NewRowView, len(b.inserts))
	for i, n := range b.inserts {
		out[i] = NewRowView{Row: len(b.rows) + i, NewRow: NewRow{
			Cells: append([]Cell(nil), n.Cells...), Set: append([]bool(nil), n.Set...), Data: append([]any(nil), n.Data...),
		}}
	}
	return out
}

// InsertCount es el número de filas nuevas sin aplicar.
func (b *Buffer) InsertCount() int {
	m := b.run.m
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(b.inserts)
}

// ---- borrados -----------------------------------------------------------------------------------------------------

// MarkDelete marca una fila real para borrar. Quita las ediciones pendientes de sus celdas (no tienen sentido en una fila
// que se va a borrar).
func (b *Buffer) MarkDelete(row int, data any) error {
	m := b.run.m
	m.mu.Lock()
	defer m.mu.Unlock()
	if b.run.evicted {
		return ErrExpired
	}
	if row < 0 || row >= len(b.rows) {
		return errors.New("fila fuera de rango")
	}
	if b.deletes == nil {
		b.deletes = map[int]any{}
	}
	b.deletes[row] = data
	for k := range b.pending {
		if k[0] == row {
			delete(b.pending, k)
		}
	}
	return nil
}

// UnmarkDelete quita la marca de borrado.
func (b *Buffer) UnmarkDelete(row int) {
	m := b.run.m
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(b.deletes, row)
}

// IsDeleted dice si la fila está marcada para borrar.
func (b *Buffer) IsDeleted(row int) bool {
	m := b.run.m
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := b.deletes[row]
	return ok
}

// DeletedRows devuelve las filas marcadas para borrar, ordenadas.
func (b *Buffer) DeletedRows() []DeletedRow {
	m := b.run.m
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]DeletedRow, 0, len(b.deletes))
	for r, d := range b.deletes {
		out = append(out, DeletedRow{Row: r, Data: d})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Row < out[j].Row })
	return out
}

// DeleteCount es el número de filas marcadas para borrar.
func (b *Buffer) DeleteCount() int {
	m := b.run.m
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(b.deletes)
}

// ---- tras aplicar -------------------------------------------------------------------------------------------------

// RemoveRows quita filas reales del buffer (las borradas en el servidor) y ajusta la memoria contabilizada.
func (b *Buffer) RemoveRows(rows []int) {
	m := b.run.m
	m.mu.Lock()
	defer m.mu.Unlock()
	drop := map[int]bool{}
	for _, r := range rows {
		drop[r] = true
	}
	kept := make([][]Cell, 0, len(b.rows))
	var freed int64
	for i, row := range b.rows {
		if !drop[i] {
			kept = append(kept, row)
			continue
		}
		freed += 24
		for _, c := range row {
			freed += c.size()
		}
	}
	b.rows = kept
	b.run.bytes -= freed
	m.total -= freed
}

// AppendRow añade una fila real al final (tras aplicar una inserción), sin respetar el límite de memoria: la fila ya existe
// en el servidor y el usuario la acaba de crear.
func (b *Buffer) AppendRow(cells []Cell) {
	var size int64 = 24
	for _, c := range cells {
		size += c.size()
	}
	m := b.run.m
	m.mu.Lock()
	defer m.mu.Unlock()
	b.rows = append(b.rows, cells)
	b.run.bytes += size
	m.total += size
}
