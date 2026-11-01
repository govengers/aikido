package vfstest_test

import (
	"testing"

	"github.com/govengers/aikido/vfs"
	"github.com/govengers/aikido/vfs/memory"
	"github.com/govengers/aikido/vfs/vfstest"
)

func TestRunConformanceOnTheReferenceBackend(t *testing.T) {
	vfstest.RunConformance(t, func() vfs.Storage { return memory.New() })
}
