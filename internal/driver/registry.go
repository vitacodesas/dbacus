package driver

import (
	"fmt"
	"sort"
	"sync"
)

var (
	mu      sync.RWMutex
	drivers = map[string]Driver{}
)

// Register añade un driver; se llama desde init() de cada paquete. Un nombre repetido es un error de programación.
func Register(d Driver) {
	mu.Lock()
	defer mu.Unlock()
	if _, dup := drivers[d.Name()]; dup {
		panic(fmt.Sprintf("driver %q registrado dos veces", d.Name()))
	}
	drivers[d.Name()] = d
}

func Get(name string) (Driver, bool) {
	mu.RLock()
	defer mu.RUnlock()
	d, ok := drivers[name]
	return d, ok
}

// List devuelve los drivers ordenados por nombre.
func List() []Driver {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]Driver, 0, len(drivers))
	for _, d := range drivers {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

// Unregister solo existe para tests.
func Unregister(name string) {
	mu.Lock()
	defer mu.Unlock()
	delete(drivers, name)
}
