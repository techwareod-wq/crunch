package analytics

import "sync"

// The source registry. Sources are constructed WITH their providers at boot
// (cmd/service/providers wiring) and registered here — the registry cannot
// import the source packages itself, since they import this package for the
// Source contract. v1 registers "gsc" and "dfs_domain_rating"; a new source is
// one more Register call at the same wiring site.
var (
	registryMu sync.RWMutex
	registered []Source
	byName     = map[string]Source{}
)

// Register adds a source at boot. Re-registering a name replaces the previous
// entry (idempotent wiring; last write wins).
func Register(s Source) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, exists := byName[s.Name()]; exists {
		for i, r := range registered {
			if r.Name() == s.Name() {
				registered[i] = s
				break
			}
		}
	} else {
		registered = append(registered, s)
	}
	byName[s.Name()] = s
}

// Sources returns every registered source in registration order.
func Sources() []Source {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]Source, len(registered))
	copy(out, registered)
	return out
}

// Get resolves a source by name.
func Get(name string) (Source, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	s, ok := byName[name]
	return s, ok
}
