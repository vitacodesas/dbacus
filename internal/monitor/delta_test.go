package monitor

import (
	"math"
	"math/rand"
	"testing"
	"time"

	"github.com/vitacodesas/dbacus/internal/model"
	"github.com/vitacodesas/dbacus/internal/store"
)

func dc(schema, digest string, execs, timerPs uint64) model.DigestCounters {
	return model.DigestCounters{Schema: schema, Digest: digest, Execs: execs, TimerPs: timerPs, RowsExamined: execs * 10, RowsSent: execs}
}

func byDigest(ds []store.DigestDelta) map[string]store.DigestDelta {
	m := map[string]store.DigestDelta{}
	for _, d := range ds {
		m[d.Digest] = d
	}
	return m
}

func TestDigestDeltaFirstSampleIsBaseline(t *testing.T) {
	e := NewEngine()
	ds, baseline := e.Digest([]model.DigestCounters{dc("db", "a", 10, 5_000_000)}, 30)
	if !baseline || len(ds) != 0 {
		t.Fatalf("la primera lectura solo fija la línea base: baseline=%v deltas=%v", baseline, ds)
	}
	ds, baseline = e.Digest([]model.DigestCounters{dc("db", "a", 15, 9_000_000)}, 30)
	if baseline || len(ds) != 1 || ds[0].Execs != 5 || ds[0].TotalUs != 4 {
		t.Fatalf("delta incorrecto: %+v", ds)
	}
}

func TestDigestDeltaCounterDropsUsesCurrent(t *testing.T) {
	e := NewEngine()
	e.Digest([]model.DigestCounters{dc("db", "a", 1000, 9_000_000_000)}, 30)
	ds, _ := e.Digest([]model.DigestCounters{dc("db", "a", 7, 3_000_000)}, 30) // TRUNCATE o desalojo
	if len(ds) != 1 || ds[0].Execs != 7 || ds[0].TotalUs != 3 {
		t.Fatalf("si el contador baja, delta = actual: %+v", ds)
	}
}

func TestDigestDeltaNewKey(t *testing.T) {
	e := NewEngine()
	e.Digest([]model.DigestCounters{dc("db", "a", 10, 1_000_000)}, 30)
	ds, _ := e.Digest([]model.DigestCounters{dc("db", "a", 10, 1_000_000), dc("db", "b", 4, 8_000_000)}, 30)
	m := byDigest(ds)
	if len(ds) != 1 || m["b"].Execs != 4 || m["b"].TotalUs != 8 {
		t.Fatalf("un digest nuevo aporta lo acumulado y uno sin cambios no aparece: %+v", ds)
	}
}

func TestDigestDeltaUptimeDropsResetsBaseline(t *testing.T) {
	e := NewEngine()
	at := t0
	e.Status(at, map[string]uint64{"Uptime": 5000, "Questions": 1000})
	e.Digest([]model.DigestCounters{dc("db", "a", 500, 1_000_000)}, 30)
	at = at.Add(30 * time.Second)
	metrics, restarted := e.Status(at, map[string]uint64{"Uptime": 12, "Questions": 40})
	if !restarted {
		t.Fatal("Uptime bajó: debe detectarse el reinicio")
	}
	if _, ok := metrics["qps"]; ok {
		t.Fatal("tras un reinicio no hay tasa en esa lectura")
	}
	if ds, baseline := e.Digest([]model.DigestCounters{dc("db", "a", 3, 1_000_000)}, 30); !baseline || len(ds) != 0 {
		t.Fatalf("tras un reinicio el digest solo fija línea base: %v %v", baseline, ds)
	}
	// y la siguiente lectura de estado vuelve a dar tasas
	m, _ := e.Status(at.Add(30*time.Second), map[string]uint64{"Uptime": 42, "Questions": 340})
	if got := m["qps"]; math.Abs(got-10) > 1e-9 {
		t.Fatalf("qps = %v, quiero 10", got)
	}
}

func TestDigestDeltaAbove2Pow63(t *testing.T) {
	const base = uint64(1) << 63
	e := NewEngine()
	e.Digest([]model.DigestCounters{{Schema: "db", Digest: "a", Execs: base + 10, TimerPs: base + 5_000_000_000_000, MaxTimerPs: base}}, 30)
	ds, _ := e.Digest([]model.DigestCounters{{Schema: "db", Digest: "a", Execs: base + 14, TimerPs: base + 8_000_000_000_000, MaxTimerPs: base + 2_000_000}}, 30)
	if len(ds) != 1 || ds[0].Execs != 4 || ds[0].TotalUs != 3_000_000 {
		t.Fatalf("delta en uint64 incorrecto: %+v", ds)
	}
	if ds[0].MaxObservedUs <= 0 {
		t.Fatalf("el máximo observado se desbordó: %d", ds[0].MaxObservedUs)
	}
	// un acumulado enorme sin lectura previa se satura, no se vuelve negativo
	e2 := NewEngine()
	e2.Digest(nil, 30)
	ds, _ = e2.Digest([]model.DigestCounters{{Schema: "db", Digest: "z", Execs: math.MaxUint64, TimerPs: math.MaxUint64}}, 30)
	if ds[0].Execs != math.MaxInt64 || ds[0].TotalUs <= 0 {
		t.Fatalf("saturación incorrecta: %+v", ds[0])
	}
}

func TestDigestTopKPlusOtherAddsUp(t *testing.T) {
	rnd := rand.New(rand.NewSource(7))
	var first, second []model.DigestCounters
	var wantExecs, wantTimer, wantRows int64
	for i := 0; i < 100; i++ {
		d := string(rune('A'+i/26)) + string(rune('a'+i%26))
		a := dc("db", d, uint64(rnd.Intn(1000)+1), uint64(rnd.Intn(1_000_000_000)+1)*1_000_000)
		b := a
		de, dt := uint64(rnd.Intn(500)+1), uint64(rnd.Intn(1000)+1)*1_000_000
		b.Execs += de
		b.TimerPs += dt
		b.RowsExamined += de * 3
		first, second = append(first, a), append(second, b)
		wantExecs += int64(de)
		wantTimer += int64(dt / 1_000_000)
		wantRows += int64(de * 3)
	}
	e := NewEngine()
	e.Digest(first, 5)
	ds, _ := e.Digest(second, 5)
	var gotExecs, gotTimer, gotRows int64
	var others int
	for _, d := range ds {
		gotExecs += d.Execs
		gotTimer += d.TotalUs
		gotRows += d.RowsExamined
		if d.Digest == store.OtherDigest {
			others++
		}
	}
	if gotExecs != wantExecs || gotTimer != wantTimer || gotRows != wantRows {
		t.Fatalf("Σ top-K + __other__ no cuadra: execs %d/%d tiempo %d/%d filas %d/%d", gotExecs, wantExecs, gotTimer, wantTimer, gotRows, wantRows)
	}
	if others != 1 || len(ds) > 11 { // a lo sumo 5+5 distintos y la fila __other__
		t.Fatalf("filas inesperadas: %d (otros=%d)", len(ds), others)
	}
}

func TestDigestOverflowRow(t *testing.T) {
	e := NewEngine()
	e.Digest([]model.DigestCounters{{Digest: "", Execs: 100, TimerPs: 5_000_000}}, 30)
	ds, _ := e.Digest([]model.DigestCounters{{Digest: "", Execs: 160, TimerPs: 9_000_000}}, 30)
	if len(ds) != 1 || ds[0].Digest != OverflowDigest || ds[0].Text != OverflowText || ds[0].Execs != 60 {
		t.Fatalf("fila de desbordamiento: %+v", ds)
	}
}

func TestStatusRatesAndInstant(t *testing.T) {
	e := NewEngine()
	m, _ := e.Status(t0, map[string]uint64{"Uptime": 10, "Questions": 1000, "Threads_running": 3, "Slow_queries": 5})
	if m["threads_running"] != 3 {
		t.Fatalf("las instantáneas salen siempre: %v", m)
	}
	if _, ok := m["qps"]; ok {
		t.Fatal("la primera lectura no tiene tasa")
	}
	m, _ = e.Status(t0.Add(20*time.Second), map[string]uint64{"Uptime": 30, "Questions": 1600, "Threads_running": 4, "Slow_queries": 3})
	if m["qps"] != 30 {
		t.Fatalf("qps = %v, quiero 30", m["qps"])
	}
	if m["slow_queries"] != 0.15 { // bajó (FLUSH STATUS): delta = actual = 3, en 20 s
		t.Fatalf("contador que baja: %v", m["slow_queries"])
	}
}

// Invariante: sea cual sea la secuencia de lecturas, ningún delta es negativo y ninguno supera lo acumulado actual.
func FuzzDigestDeltas(f *testing.F) {
	f.Add([]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16})
	f.Add([]byte{255, 255, 255, 255, 255, 255, 255, 255, 0, 0, 0, 0, 0, 0, 0, 1})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) < 8 {
			return
		}
		next := func() uint64 {
			var v uint64
			for i := 0; i < 8; i++ {
				v <<= 8
				if len(data) > 0 {
					v |= uint64(data[0])
					data = data[1:]
				}
			}
			return v
		}
		e := NewEngine()
		for round := 0; round < 4 && len(data) > 0; round++ {
			var list []model.DigestCounters
			cur := map[string]model.DigestCounters{}
			for i := 0; i < 1+int(next()%6); i++ {
				c := model.DigestCounters{Schema: "s", Digest: string(rune('a' + next()%4)), Execs: next(), TimerPs: next(), RowsExamined: next(), RowsSent: next(), NoIndex: next(), TmpDiskTables: next(), Errors: next(), MaxTimerPs: next()}
				cur[c.Digest] = c
				list = append(list, c)
			}
			ds, _ := e.Digest(list, 1+int(next()%3))
			for _, d := range ds {
				for _, v := range []int64{d.Execs, d.TotalUs, d.MaxObservedUs, d.RowsExamined, d.RowsSent, d.NoIndex, d.TmpDisk, d.Errors} {
					if v < 0 {
						t.Fatalf("delta negativo: %+v", d)
					}
				}
			}
		}
	})
}

// TestDigestExcludingOwnKeepsSumCoherent: los digests excluidos no salen ni entran en __other__; la suma es total − excluido.
func TestDigestExcludingOwnKeepsSumCoherent(t *testing.T) {
	var first, second []model.DigestCounters
	var wantExecs int64
	own := map[string]bool{}
	for i := 0; i < 60; i++ {
		d := string(rune('A'+i/26)) + string(rune('a'+i%26))
		a := dc("db", d, 100, 1_000_000_000)
		b := a
		b.Execs += uint64(i + 1)
		b.TimerPs += uint64(i+1) * 1_000_000
		first, second = append(first, a), append(second, b)
		if i%6 == 0 {
			own[d] = true
			continue
		}
		wantExecs += int64(i + 1)
	}
	e := NewEngine()
	e.DigestExcluding(first, 5, func(d string) bool { return own[d] })
	ds, _ := e.DigestExcluding(second, 5, func(d string) bool { return own[d] })
	var got int64
	for _, d := range ds {
		if own[d.Digest] {
			t.Fatalf("salió un digest excluido: %+v", d)
		}
		got += d.Execs
	}
	if got != wantExecs {
		t.Fatalf("Σ top-K + __other__ = %d, quiero total − excluidos = %d", got, wantExecs)
	}
}

func TestChangedDigests(t *testing.T) {
	e := NewEngine()
	if got := e.ChangedDigests([]model.DigestCounters{dc("db", "a", 1, 1)}); got != nil {
		t.Fatalf("sin línea base no hay candidatos: %v", got)
	}
	e.Digest([]model.DigestCounters{dc("db", "a", 5, 1), dc("db", "b", 5, 1), dc("db", "c", 5, 1)}, 30)
	got := e.ChangedDigests([]model.DigestCounters{dc("db", "a", 5, 1), dc("db", "b", 9, 1), dc("db", "c", 2, 1), dc("db", "d", 1, 1), {Digest: "", Execs: 3}})
	want := map[string]bool{"b": true, "c": true, "d": true} // a no cambió; c bajó (reinicio); d es nuevo; el desbordamiento no se clasifica
	if len(got) != len(want) {
		t.Fatalf("candidatos: %v", got)
	}
	for _, d := range got {
		if !want[d] {
			t.Fatalf("candidato inesperado %q en %v", d, got)
		}
	}
}
