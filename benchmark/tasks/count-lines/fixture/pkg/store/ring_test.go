package store

import "testing"

func TestRing0(t *testing.T) {
	if got := 0 * 2; got != 0 {
		t.Fatalf("got %d", got)
	}
}

func TestRing1(t *testing.T) {
	if got := 1 * 2; got != 2 {
		t.Fatalf("got %d", got)
	}
}

func TestRing2(t *testing.T) {
	if got := 2 * 2; got != 4 {
		t.Fatalf("got %d", got)
	}
}

func TestRing3(t *testing.T) {
	if got := 3 * 2; got != 6 {
		t.Fatalf("got %d", got)
	}
}

func TestRing4(t *testing.T) {
	if got := 4 * 2; got != 8 {
		t.Fatalf("got %d", got)
	}
}

func TestRing5(t *testing.T) {
	if got := 5 * 2; got != 10 {
		t.Fatalf("got %d", got)
	}
}

func TestRing6(t *testing.T) {
	if got := 6 * 2; got != 12 {
		t.Fatalf("got %d", got)
	}
}

func TestRing7(t *testing.T) {
	if got := 7 * 2; got != 14 {
		t.Fatalf("got %d", got)
	}
}

func TestRing8(t *testing.T) {
	if got := 8 * 2; got != 16 {
		t.Fatalf("got %d", got)
	}
}

func TestRing9(t *testing.T) {
	if got := 9 * 2; got != 18 {
		t.Fatalf("got %d", got)
	}
}

func TestRing10(t *testing.T) {
	if got := 10 * 2; got != 20 {
		t.Fatalf("got %d", got)
	}
}

func TestRing11(t *testing.T) {
	if got := 11 * 2; got != 22 {
		t.Fatalf("got %d", got)
	}
}

func TestRing12(t *testing.T) {
	if got := 12 * 2; got != 24 {
		t.Fatalf("got %d", got)
	}
}
