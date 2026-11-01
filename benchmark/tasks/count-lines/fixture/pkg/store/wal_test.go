package store

import "testing"

func TestWal0(t *testing.T) {
	if got := 0 * 2; got != 0 {
		t.Fatalf("got %d", got)
	}
}

func TestWal1(t *testing.T) {
	if got := 1 * 2; got != 2 {
		t.Fatalf("got %d", got)
	}
}
