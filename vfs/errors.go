package vfs

import "errors"

var (
	// ErrPathInvalid is returned for a path that breaks a rule of ValidatePath
	// or, in backends that enforce it, resolves outside the storage root.
	ErrPathInvalid = errors.New("aikido/vfs: invalid path")
	// ErrFileNotFound is returned by ReadFile and DeleteFile for a path that
	// holds no file.
	ErrFileNotFound = errors.New("aikido/vfs: file not found")
	// ErrFileTooLarge is returned when content exceeds a size limit the caller
	// configured (agent.VFSToolOptions.MaximumFileBytes).
	ErrFileTooLarge = errors.New("aikido/vfs: file too large")
	// ErrReadOnly is returned by Storage backends that do not support mutation
	// (e.g. vfs/embedfs). Callers can errors.Is-check it to fall back to a
	// writable backend or to surface a friendly message.
	ErrReadOnly = errors.New("aikido/vfs: storage is read-only")
)
