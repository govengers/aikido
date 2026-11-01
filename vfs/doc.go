// Package vfs is the pluggable storage abstraction for AI-managed projects.
//
// The base Storage contract is intentionally minimal (4 methods). Optional
// capabilities, scoping (ScopedStorage) and search (Searchable), extend it via
// interface assertion or registration-time binding, mirroring the capability
// pattern used in govengers/auth.
//
// Backends: vfs/memory (in-process, searchable), vfs/osfs (a directory on
// disk) and vfs/embedfs (any fs.FS, read-only, searchable). Other backends are
// caller-implemented and verified with vfs/vfstest.RunConformance.
package vfs
