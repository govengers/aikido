package vfs

// ScopedStorage is an OPTIONAL capability for backends that multiplex tenants
// (or any other namespace). The library never sees a scope parameter on
// Storage methods: callers pre-bind via Scope and hand the resulting Storage
// to RegisterVFSTools (ADR-013).
type ScopedStorage interface {
	// Scope returns a Storage that sees only the files of scope.
	Scope(scope string) Storage
}
