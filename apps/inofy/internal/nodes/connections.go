package nodes

import "sync"

// Registry is the App's mutable catalog of approved provider
// connections. Connections may arrive at startup (-connections
// file) or via the authenticated API; the executor reads through
// the same concurrency-safe view either way.
type Registry struct {
	mu sync.RWMutex
	m  map[string]Connection
}

// NewRegistry returns a registry seeded with the given
// connections (may be nil).
func NewRegistry(seed map[string]Connection) *Registry {
	r := &Registry{m: map[string]Connection{}}
	for id, c := range seed {
		r.m[id] = c
	}
	return r
}

// Get returns the connection for id.
func (r *Registry) Get(id string) (Connection, bool) {
	if r == nil {
		return Connection{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.m[id]
	return c, ok
}

// Set adds or replaces a connection.
func (r *Registry) Set(id string, c Connection) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.m[id] = c
}

// Delete removes a connection; unknown ids are ignored.
func (r *Registry) Delete(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.m, id)
}

// List returns a snapshot copy of all connections.
func (r *Registry) List() map[string]Connection {
	if r == nil {
		return map[string]Connection{}
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]Connection, len(r.m))
	for id, c := range r.m {
		out[id] = c
	}
	return out
}
