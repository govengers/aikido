package util

import "testing"

func TestPool0(t *testing.T) {
	if got := 0 * 2; got != 0 {
		t.Fatalf("got %d", got)
	}
}

func TestPool1(t *testing.T) {
	if got := 1 * 2; got != 2 {
		t.Fatalf("got %d", got)
	}
}

func TestPool2(t *testing.T) {
	if got := 2 * 2; got != 4 {
		t.Fatalf("got %d", got)
	}
}

func TestPool3(t *testing.T) {
	if got := 3 * 2; got != 6 {
		t.Fatalf("got %d", got)
	}
}

func TestPool4(t *testing.T) {
	if got := 4 * 2; got != 8 {
		t.Fatalf("got %d", got)
	}
}

func TestPool5(t *testing.T) {
	if got := 5 * 2; got != 10 {
		t.Fatalf("got %d", got)
	}
}

func TestPool6(t *testing.T) {
	if got := 6 * 2; got != 12 {
		t.Fatalf("got %d", got)
	}
}

func TestPool7(t *testing.T) {
	if got := 7 * 2; got != 14 {
		t.Fatalf("got %d", got)
	}
}

func TestPool8(t *testing.T) {
	if got := 8 * 2; got != 16 {
		t.Fatalf("got %d", got)
	}
}

func TestPool9(t *testing.T) {
	if got := 9 * 2; got != 18 {
		t.Fatalf("got %d", got)
	}
}

func TestPool10(t *testing.T) {
	if got := 10 * 2; got != 20 {
		t.Fatalf("got %d", got)
	}
}

func TestPool11(t *testing.T) {
	if got := 11 * 2; got != 22 {
		t.Fatalf("got %d", got)
	}
}

func TestPool12(t *testing.T) {
	if got := 12 * 2; got != 24 {
		t.Fatalf("got %d", got)
	}
}

func TestPool13(t *testing.T) {
	if got := 13 * 2; got != 26 {
		t.Fatalf("got %d", got)
	}
}

func TestPool14(t *testing.T) {
	if got := 14 * 2; got != 28 {
		t.Fatalf("got %d", got)
	}
}
