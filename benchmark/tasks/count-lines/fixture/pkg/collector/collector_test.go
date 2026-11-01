package collector

import "testing"

func TestCollector0(t *testing.T) {
	if got := 0 * 2; got != 0 {
		t.Fatalf("got %d", got)
	}
}

func TestCollector1(t *testing.T) {
	if got := 1 * 2; got != 2 {
		t.Fatalf("got %d", got)
	}
}

func TestCollector2(t *testing.T) {
	if got := 2 * 2; got != 4 {
		t.Fatalf("got %d", got)
	}
}

func TestCollector3(t *testing.T) {
	if got := 3 * 2; got != 6 {
		t.Fatalf("got %d", got)
	}
}

func TestCollector4(t *testing.T) {
	if got := 4 * 2; got != 8 {
		t.Fatalf("got %d", got)
	}
}
