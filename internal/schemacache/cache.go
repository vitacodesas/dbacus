// Package schemacache cachea lecturas de esquema por sesión: TTL, colapso de peticiones idénticas e invalidación (spec 05).
package schemacache

import (
	"context"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

const (
	DefaultTTL     = 5 * time.Minute
	DefaultTimeout = 10 * time.Second
)

type entry struct {
	v  any
	at time.Time
}

type Cache struct {
	ttl time.Duration
	now func() time.Time

	mu      sync.Mutex
	entries map[string]entry
	gen     uint64 // sube en cada invalidación: una carga iniciada antes no puede repoblar datos obsoletos
	group   singleflight.Group
}

func New(ttl time.Duration) *Cache {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &Cache{ttl: ttl, now: time.Now, entries: map[string]entry{}}
}

// Invalidate borra las entradas cuya clave empieza por prefix ("" = todas).
func (c *Cache) Invalidate(prefix string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	for k := range c.entries {
		if strings.HasPrefix(k, prefix) {
			delete(c.entries, k)
		}
	}
}

func (c *Cache) get(key string) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	if c.now().Sub(e.at) > c.ttl {
		delete(c.entries, key)
		return nil, false
	}
	return e.v, true
}

func (c *Cache) put(key string, v any, gen uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if gen == c.gen {
		c.entries[key] = entry{v: v, at: c.now()}
	}
}

// Load devuelve el valor cacheado o ejecuta fn una sola vez aunque haya varios llamadores simultáneos.
// fn corre con base (p. ej. el ctx de la sesión, que se cancela al desconectar) más timeout, no con el ctx del
// llamador: así un llamador que se va no aborta la carga de los demás. Cada llamador espera con su propio ctx.
// Los errores no se cachean. Los valores devueltos son compartidos: no deben mutarse.
func Load[T any](ctx context.Context, c *Cache, base context.Context, timeout time.Duration, key string, fn func(context.Context) (T, error)) (T, error) {
	var zero T
	if v, ok := c.get(key); ok {
		return v.(T), nil
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	c.mu.Lock()
	gen := c.gen
	c.mu.Unlock()

	ch := c.group.DoChan(key, func() (any, error) {
		lctx, cancel := context.WithTimeout(base, timeout)
		defer cancel()
		v, err := fn(lctx)
		if err != nil {
			return nil, err
		}
		c.put(key, v, gen)
		return v, nil
	})
	select {
	case r := <-ch:
		if r.Err != nil {
			return zero, r.Err
		}
		return r.Val.(T), nil
	case <-ctx.Done():
		return zero, ctx.Err()
	}
}
