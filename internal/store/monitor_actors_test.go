package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/vitacodesas/dbacus/internal/model"
)

// Quién ejecutó cada digest (spec 22 ⚑D17): se escribe con la muestra, se lee con Top y Digest, caduca con la hora y se borra con los datos.

func putActors(t *testing.T, s *Store, pid string, run int64, at time.Time, actors []ActorSeen, ds ...DigestDelta) {
	t.Helper()
	_, err := s.InsertSample(context.Background(), SampleInput{RunID: run, ProfileID: pid, At: at, SpanMs: 30000, Metrics: map[string]float64{"qps": 1}, Digests: ds, Actors: actors})
	require.NoError(t, err)
}

func TestActorsUpsertSameTransaction(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	pid := seedProfile(t, s, "p")
	run := openRun(t, s, pid, t0, 30)
	a := ActorSeen{Digest: "d1", User: "app", Host: "10.0.0.5"}
	putActors(t, s, pid, run, t0.Add(time.Minute), []ActorSeen{a}, DigestDelta{Schema: "shop", Digest: "d1", Text: "SELECT ?", Execs: 1, TotalUs: 10})
	putActors(t, s, pid, run, t0.Add(2*time.Minute), []ActorSeen{a, {Digest: "d1", User: "ana", Host: "10.0.0.7"}})

	var seen, last int64
	require.NoError(t, s.db.QueryRow(`SELECT seen, last_ts FROM monitor_digest_actor WHERE user = 'app'`).Scan(&seen, &last))
	require.Equal(t, int64(2), seen, "dos muestras de la misma hora suman en la misma fila")
	require.Equal(t, t0.Add(2*time.Minute).UnixMilli(), last)
	require.Equal(t, 2, count(t, s, "monitor_digest_actor"))

	// Si la muestra falla, los actores tampoco quedan: una sola transacción.
	before := count(t, s, "monitor_digest_actor")
	_, err := s.InsertSample(ctx, SampleInput{RunID: 9999, ProfileID: pid, At: t0, Actors: []ActorSeen{{Digest: "dx", User: "u", Host: "h"}}})
	require.Error(t, err)
	require.Equal(t, before, count(t, s, "monitor_digest_actor"))
}

func TestTopIncludesActors(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	pid := seedProfile(t, s, "p")
	run := openRun(t, s, pid, t0, 30)
	d1, d2 := DigestDelta{Schema: "shop", Digest: "d1", Text: "SELECT 1", Execs: 5, TotalUs: 900}, DigestDelta{Schema: "shop", Digest: "d2", Text: "SELECT 2", Execs: 5, TotalUs: 100}
	// d1: app 3 veces, ana 2, bob 1, eva 1 (4 actores); d2: sin datos.
	for i, who := range []string{"app", "app", "app", "ana", "ana", "bob", "eva"} {
		putActors(t, s, pid, run, t0.Add(time.Duration(i+1)*time.Minute), []ActorSeen{{Digest: "d1", User: who, Host: "10.0.0." + who}}, d1, d2)
	}
	to := t0.Add(time.Hour)
	got, err := s.TopDigests(ctx, model.TopRequest{ProfileID: pid, From: t0, To: to, Limit: 10})
	require.NoError(t, err)
	m := digestMap(got)
	top := m["shop/d1"]
	require.Equal(t, 4, top.ActorCount)
	require.Len(t, top.Actors, 3, "el ranking lleva los 3 más vistos")
	require.Equal(t, "app", top.Actors[0].User)
	require.Equal(t, uint64(3), top.Actors[0].Seen)
	require.Equal(t, "ana", top.Actors[1].User)
	require.Equal(t, t0.Add(3*time.Minute).UnixMilli(), top.Actors[0].LastSeen)
	require.NotNil(t, m["shop/d2"].Actors, "sin datos: lista vacía, no null")
	require.Empty(t, m["shop/d2"].Actors)
	require.Zero(t, m["shop/d2"].ActorCount)

	detail, err := s.DigestDetail(ctx, pid, "d1", t0, to)
	require.NoError(t, err)
	require.Len(t, detail.Actors, 4, "el detalle lista a todos")

	// Fuera del rango no se cuenta.
	late, err := s.TopDigests(ctx, model.TopRequest{ProfileID: pid, From: t0.Add(2 * time.Hour), To: t0.Add(3 * time.Hour), Limit: 10})
	require.NoError(t, err)
	require.Empty(t, late)
}

// Un solo SELECT para todas las filas del ranking: el número de consultas no crece con las filas (sin N+1).
func TestTopActorsSingleQueryPerChunk(t *testing.T) {
	s := newStore(t)
	pid := seedProfile(t, s, "p")
	run := openRun(t, s, pid, t0, 30)
	var ds []DigestDelta
	var actors []ActorSeen
	for i := range 50 {
		d := "d" + string(rune('A'+i%26)) + string(rune('a'+i/26))
		ds = append(ds, DigestDelta{Schema: "shop", Digest: d, Text: "SELECT ?", Execs: 1, TotalUs: int64(100 + i)})
		actors = append(actors, ActorSeen{Digest: d, User: "app", Host: "h"})
	}
	putActors(t, s, pid, run, t0.Add(time.Minute), actors, ds...)
	got, err := s.TopDigests(context.Background(), model.TopRequest{ProfileID: pid, From: t0, To: t0.Add(time.Hour), Limit: 100})
	require.NoError(t, err)
	require.Len(t, got, 50)
	for _, st := range got {
		require.Equal(t, 1, st.ActorCount, st.Digest)
	}
}

func TestActorsRetentionAndPurge(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	pid := seedProfile(t, s, "p")
	run := openRun(t, s, pid, t0, 30)
	now := t0.Add(10 * 24 * time.Hour)
	old, fresh := now.Add(-6*24*time.Hour), now.Add(-time.Hour) // HourlyDays = 5
	putActors(t, s, pid, run, old, []ActorSeen{{Digest: "d1", User: "app", Host: "h"}})
	putActors(t, s, pid, run, fresh, []ActorSeen{{Digest: "d1", User: "ana", Host: "h"}})

	_, err := s.PurgeMonitor(ctx, now, MonitorRetention{RawDays: 2, HourlyDays: 5, DailyDays: 365})
	require.NoError(t, err)
	var users []string
	rows, err := s.db.Query(`SELECT user FROM monitor_digest_actor`)
	require.NoError(t, err)
	for rows.Next() {
		var u string
		require.NoError(t, rows.Scan(&u))
		users = append(users, u)
	}
	require.NoError(t, rows.Close())
	require.Equal(t, []string{"ana"}, users, "los actores caducan con la retención por hora")

	cut := now
	require.NoError(t, s.PurgeMonitorData(ctx, pid, &cut))
	require.Zero(t, count(t, s, "monitor_digest_actor"), "«borrar antes de» también los quita")

	putActors(t, s, pid, run, fresh, []ActorSeen{{Digest: "d1", User: "ana", Host: "h"}})
	require.NoError(t, s.PurgeMonitorData(ctx, pid, nil))
	require.Zero(t, count(t, s, "monitor_digest_actor"), "«Borrar datos» los quita")
}
