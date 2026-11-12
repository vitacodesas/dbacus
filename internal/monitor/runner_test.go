package monitor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/vitacodesas/dbacus/internal/model"
	"github.com/vitacodesas/dbacus/internal/store"
)

var allSources = model.MonitorConfig{IntervalSec: 30, TopK: 30, Processes: true, Status: true, Digest: true}

// cancelAfter cancela el contexto padre (la sesión) durante el n-ésimo Sleep: el Runner termina como «disconnected».
func cancelAfter(clk *fakeClock, cancel context.CancelFunc, n int) {
	clk.onSleep = func(i int) {
		if i >= n {
			cancel()
		}
	}
}

func TestRunnerFirstSampleIsBaselineAndTextsOnlyForMissing(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	clk, st, ev := newClock(), newStore(), &events{}
	rd := &fakeReader{
		statusFn: func(n int) (map[string]uint64, error) { return statusVals(n), nil },
		digestFn: func(n int) ([]model.DigestCounters, error) {
			return []model.DigestCounters{dc("db", "a", uint64(10*n), uint64(n)*1e9), dc("db", "b", uint64(5*n), uint64(n)*2e9)}, nil
		},
		procsFn: func(int) ([]model.Process, error) { return nil, nil },
	}
	cancelAfter(clk, cancel, 3) // 3 muestras
	r := startRunner(t, parent, rd, st, clk, ev, allSources)
	waitDone(t, r)

	s := st.snapshot()
	if len(s) != 3 {
		t.Fatalf("muestras: %d", len(s))
	}
	if len(s[0].Digests) != 0 || s[0].SpanMs != 0 {
		t.Fatalf("la primera muestra es línea base: %+v", s[0])
	}
	if len(s[1].Digests) != 2 {
		t.Fatalf("la segunda ya trae deltas: %+v", s[1].Digests)
	}
	for _, d := range s[1].Digests {
		if d.Text == "" {
			t.Fatalf("falta el texto de un digest nuevo: %+v", d)
		}
	}
	// el diccionario ya tiene los textos: en la tercera muestra no se piden de nuevo
	if n := rd.nText.Load(); n != 1 {
		t.Fatalf("DigestTexts se llamó %d veces, quiero 1", n)
	}
	if got := st.reason(1); got != store.RunDisconnected {
		t.Fatalf("razón de cierre = %q", got)
	}
	if len(ev.samples) != 3 || !ev.has(StateDisconnected, "") {
		t.Fatalf("eventos: %+v", ev)
	}
}

func TestRunnerNoOverlap(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	clk, st, ev := newClock(), newStore(), &events{}
	rd := &fakeReader{
		statusFn: func(n int) (map[string]uint64, error) { return statusVals(n), nil },
		digestFn: func(n int) ([]model.DigestCounters, error) {
			return []model.DigestCounters{dc("db", "a", uint64(n), uint64(n))}, nil
		},
		procsFn: func(int) ([]model.Process, error) { return nil, nil },
	}
	cancelAfter(clk, cancel, 6)
	waitDone(t, startRunner(t, parent, rd, st, clk, ev, allSources))
	if m := rd.maxInflight.Load(); m != 1 {
		t.Fatalf("lecturas simultáneas: %d, quiero 1", m)
	}
	if len(st.snapshot()) != 6 {
		t.Fatalf("muestras: %d", len(st.snapshot()))
	}
}

func TestRunnerBackoff(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	clk, st, ev := newClock(), newStore(), &events{}
	rd := &fakeReader{
		statusFn: func(n int) (map[string]uint64, error) { return statusVals(n), nil },
		procsFn:  func(int) ([]model.Process, error) { return nil, nil },
		digestFn: func(int) ([]model.DigestCounters, error) { return nil, nil },
	}
	rd.onCall = func() {} // la lentitud la simula Digests
	slow := &slowReader{fakeReader: rd, clk: clk, d: 20 * time.Second}
	cancelAfter(clk, cancel, 3)
	r, err := StartRunner(parent, RunnerDeps{Reader: slow, Store: st, Clock: clk, ProfileID: "p1", Caps: okCaps(), Config: allSources, OnSample: ev.onSample, OnState: ev.onState})
	if err != nil {
		t.Fatal(err)
	}
	waitDone(t, r)
	// 20 s > 50 % de 30 s ⇒ el periodo pasa a 40 s ⇒ se duerme 40-20 = 20 s
	if len(clk.sleeps) == 0 || clk.sleeps[0] != 20*time.Second {
		t.Fatalf("esperas: %v", clk.sleeps)
	}
	if !ev.has(StateDegraded, SourceSampler) {
		t.Fatal("falta el aviso de muestra lenta")
	}
	if ev.count(StateDegraded, SourceSampler) != 1 {
		t.Fatalf("el aviso debe darse una vez: %d", ev.count(StateDegraded, SourceSampler))
	}
	if sp := st.snapshot()[1].SpanMs; sp != 40_000 {
		t.Fatalf("span entre muestras = %d ms, quiero 40000", sp)
	}
}

// slowReader hace que leer el digest consuma tiempo de reloj.
type slowReader struct {
	*fakeReader
	clk *fakeClock
	d   time.Duration
}

func (s *slowReader) Digests(ctx context.Context) ([]model.DigestCounters, error) {
	s.clk.Advance(s.d)
	return s.fakeReader.Digests(ctx)
}

func TestRunnerGap(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	clk, st, ev := newClock(), newStore(), &events{}
	rd := &fakeReader{statusFn: func(n int) (map[string]uint64, error) { return statusVals(n), nil }}
	cancelAfter(clk, cancel, 3)
	clk.extra = func(i int) time.Duration { // entre la 2.ª y la 3.ª muestra la app «estuvo dormida»
		if i == 2 {
			return 200 * time.Second
		}
		return 0
	}
	cfg := allSources
	cfg.Processes, cfg.Digest = false, false
	waitDone(t, startRunner(t, parent, rd, st, clk, ev, cfg))
	s := st.snapshot()
	if len(s) != 3 || s[0].AfterGap || s[1].AfterGap || !s[2].AfterGap {
		t.Fatalf("huecos: %+v", s)
	}
	if s[2].SpanMs != 230_000 {
		t.Fatalf("span real = %d", s[2].SpanMs)
	}
	// la tasa usa el tiempo real de la lectura previa de estado, no el intervalo nominal
	if qps := s[2].Metrics["qps"]; qps != 3000.0/230 {
		t.Fatalf("qps = %v", qps)
	}
}

func TestRunnerPartialFailure(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	clk, st, ev := newClock(), newStore(), &events{}
	rd := &fakeReader{
		statusFn: func(n int) (map[string]uint64, error) {
			if n <= 3 {
				return nil, errors.New("boom")
			}
			return statusVals(n), nil
		},
		procsFn: func(int) ([]model.Process, error) {
			return []model.Process{{ID: 1, User: "u", Command: "Query", TimeSec: 1}}, nil
		},
	}
	cfg := allSources
	cfg.Digest = false
	cancelAfter(clk, cancel, 5)
	waitDone(t, startRunner(t, parent, rd, st, clk, ev, cfg))
	s := st.snapshot()
	if len(s) != 5 {
		t.Fatalf("una fuente caída no tumba la muestra: %d muestras", len(s))
	}
	if _, ok := s[0].Metrics["threads_connected"]; ok {
		t.Fatal("sin estado no hay métricas de estado")
	}
	if s[0].Metrics["proc_total"] != 1 {
		t.Fatalf("los procesos sí se guardan: %v", s[0].Metrics)
	}
	if _, ok := s[3].Metrics["threads_connected"]; !ok {
		t.Fatal("al volver el estado se guarda de nuevo")
	}
	if ev.count(StateDegraded, SourceStatus) != 1 {
		t.Fatalf("tres fallos seguidos ⇒ un solo aviso: %d", ev.count(StateDegraded, SourceStatus))
	}
	if !ev.has(StateRunning, SourceStatus) {
		t.Fatal("falta el aviso de recuperación")
	}
}

func TestRunnerSkipsUnavailableSources(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	clk, st, ev := newClock(), newStore(), &events{}
	rd := &fakeReader{statusFn: func(n int) (map[string]uint64, error) { return statusVals(n), nil }}
	caps := okCaps()
	caps.Digest = model.SourceState{State: model.SourceUnavailable, Reason: model.ReasonPerfSchemaOff}
	caps.Processlist = model.SourceState{State: model.SourceUnavailable, Reason: model.ReasonNoPrivilege}
	cancelAfter(clk, cancel, 2)
	r, err := StartRunner(parent, RunnerDeps{Reader: rd, Store: st, Clock: clk, ProfileID: "p1", Caps: caps, Config: allSources, OnState: ev.onState})
	if err != nil {
		t.Fatal(err)
	}
	waitDone(t, r)
	if rd.nDigest.Load() != 0 || rd.nProcs.Load() != 0 {
		t.Fatalf("no se leen fuentes no disponibles: digest=%d procs=%d", rd.nDigest.Load(), rd.nProcs.Load())
	}
}

func TestRunnerStopIsIdempotentAndStopped(t *testing.T) {
	clk := newClock()
	clk.block = true
	st, ev := newStore(), &events{}
	rd := &fakeReader{statusFn: func(n int) (map[string]uint64, error) { return statusVals(n), nil }}
	r := startRunner(t, context.Background(), rd, st, clk, ev, allSources)
	deadline := time.Now().Add(5 * time.Second)
	for len(st.snapshot()) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	r.Stop()
	r.Stop()
	if got := st.reason(r.Run().ID); got != store.RunStopped {
		t.Fatalf("razón = %q", got)
	}
	if ev.count(StateStopped, "") != 1 {
		t.Fatalf("un solo evento stopped: %d", ev.count(StateStopped, ""))
	}
}

func TestRunnerServerChangedEndsRun(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	clk, st, ev := newClock(), newStore(), &events{}
	rd := &fakeReader{
		statusFn: func(n int) (map[string]uint64, error) { return statusVals(n), nil },
		probeFn: func(int) (model.MonitorCaps, error) {
			c := okCaps()
			c.ServerID = "uuid-OTHER"
			return c, nil
		},
	}
	r, err := StartRunner(parent, RunnerDeps{Reader: rd, Store: st, Clock: clk, ProfileID: "p1", Caps: okCaps(), Config: allSources, ServerIDEvery: 2, OnState: ev.onState})
	if err != nil {
		t.Fatal(err)
	}
	waitDone(t, r)
	if !ev.has(StateStopped, SourceServer) || st.reason(1) != store.RunStopped {
		t.Fatalf("un servidor distinto corta la ejecución: %+v / %q", ev.states, st.reason(1))
	}
}

func TestRunnerValidatesConfig(t *testing.T) {
	for _, cfg := range []model.MonitorConfig{{IntervalSec: 1}, {IntervalSec: 5000}, {IntervalSec: 30, TopK: 999}} {
		_, err := StartRunner(context.Background(), RunnerDeps{Reader: &fakeReader{}, Store: newStore(), Clock: newClock(), Caps: okCaps(), Config: cfg})
		if err == nil {
			t.Fatalf("debió rechazar %+v", cfg)
		}
	}
}

// Ninguna parte de lo que llega al store, a los eventos ni a proc_json contiene INFO.
func TestMonitorNeverPersistsInfo(t *testing.T) {
	const secret = "SELECT * FROM users WHERE password = 'hunter2'"
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	clk, st, ev := newClock(), newStore(), &events{}
	rd := &fakeReader{
		statusFn: func(n int) (map[string]uint64, error) { return statusVals(n), nil },
		procsFn: func(int) ([]model.Process, error) {
			return []model.Process{{ID: 7, User: "app", Host: "h", DB: "d", Command: "Query", TimeSec: 99, State: "executing", Info: secret}}, nil
		},
		digestFn: func(n int) ([]model.DigestCounters, error) {
			return []model.DigestCounters{dc("d", "x", uint64(n), uint64(n))}, nil
		},
		textsFn: func(want []string) (map[string]string, error) {
			return map[string]string{"x": "SELECT * FROM users WHERE password = ?"}, nil
		},
	}
	cancelAfter(clk, cancel, 3)
	waitDone(t, startRunner(t, parent, rd, st, clk, ev, allSources))
	dump := fmt.Sprintf("%+v %+v %+v", st.snapshot(), ev.samples, ev.states)
	if strings.Contains(dump, "hunter2") || strings.Contains(dump, secret) {
		t.Fatalf("INFO llegó a las muestras o eventos:\n%s", dump)
	}
	if !strings.Contains(dump, "password = ?") {
		t.Fatal("el texto normalizado del digest sí debe guardarse")
	}
}

// Cada muestra anota quién ejecutaba cada digest (⚑D17): sin repetidos, sin hilos internos ni procesos sin digest, y sin INFO.
func TestRunnerRecordsActors(t *testing.T) {
	const secret = "SELECT * FROM users WHERE token = 'abc123'"
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	clk, st, ev := newClock(), newStore(), &events{}
	rd := &fakeReader{
		statusFn: func(n int) (map[string]uint64, error) { return statusVals(n), nil },
		procsFn: func(int) ([]model.Process, error) {
			return []model.Process{
				{ID: 1, User: "app", Host: "10.0.0.5:5001", Command: "Query", TimeSec: 1, Info: secret, Digest: "d1"},
				{ID: 2, User: "app", Host: "10.0.0.5:5002", Command: "Query", TimeSec: 2, Info: secret, Digest: "d1"},
				{ID: 3, User: "ana", Host: "10.0.0.7:5003", Command: "Query", TimeSec: 3, Digest: "d2"},
				{ID: 4, User: "app", Host: "10.0.0.5:5004", Command: "Sleep"},
				{ID: 5, User: "system user", Host: "", Command: "Daemon", Digest: "d3"},
			}, nil
		},
		digestFn: func(n int) ([]model.DigestCounters, error) {
			return []model.DigestCounters{dc("d", "d1", uint64(n), uint64(n))}, nil
		},
		textsFn: func([]string) (map[string]string, error) { return map[string]string{"d1": "SELECT ?"}, nil },
	}
	cancelAfter(clk, cancel, 2)
	waitDone(t, startRunner(t, parent, rd, st, clk, ev, allSources))

	s := st.snapshot()
	if len(s) == 0 {
		t.Fatal("sin muestras")
	}
	want := []store.ActorSeen{{Digest: "d1", User: "app", Host: "10.0.0.5"}, {Digest: "d2", User: "ana", Host: "10.0.0.7"}}
	for i, in := range s {
		if fmt.Sprint(in.Actors) != fmt.Sprint(want) {
			t.Fatalf("muestra %d: actores = %+v, quiero %+v", i, in.Actors, want)
		}
	}
	if dump := fmt.Sprintf("%+v", s); strings.Contains(dump, "abc123") {
		t.Fatalf("INFO llegó a las muestras:\n%s", dump)
	}
}

// Sin Digest en el processlist (MariaDB, 5.7) no hay actores y el resto de la muestra sigue igual.
func TestRunnerWithoutProcessDigestRecordsNoActors(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	clk, st, ev := newClock(), newStore(), &events{}
	rd := &fakeReader{
		statusFn: func(n int) (map[string]uint64, error) { return statusVals(n), nil },
		procsFn: func(int) ([]model.Process, error) {
			return []model.Process{{ID: 1, User: "app", Host: "10.0.0.5:5001", Command: "Query", TimeSec: 1, Info: "SELECT 1"}}, nil
		},
		digestFn: func(n int) ([]model.DigestCounters, error) { return nil, nil },
	}
	cancelAfter(clk, cancel, 2)
	waitDone(t, startRunner(t, parent, rd, st, clk, ev, allSources))
	for _, in := range st.snapshot() {
		if len(in.Actors) != 0 {
			t.Fatalf("no debería haber actores: %+v", in.Actors)
		}
	}
}
