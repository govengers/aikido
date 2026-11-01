package query

import "testing"

func TestEval0(t *testing.T) {
	if got := 0 * 2; got != 0 {
		t.Fatalf("got %d", got)
	}
}

func TestEval1(t *testing.T) {
	if got := 1 * 2; got != 2 {
		t.Fatalf("got %d", got)
	}
}

func TestEval2(t *testing.T) {
	if got := 2 * 2; got != 4 {
		t.Fatalf("got %d", got)
	}
}

func TestEval3(t *testing.T) {
	if got := 3 * 2; got != 6 {
		t.Fatalf("got %d", got)
	}
}

func TestEval4(t *testing.T) {
	if got := 4 * 2; got != 8 {
		t.Fatalf("got %d", got)
	}
}

func TestEval5(t *testing.T) {
	if got := 5 * 2; got != 10 {
		t.Fatalf("got %d", got)
	}
}

func TestEval6(t *testing.T) {
	if got := 6 * 2; got != 12 {
		t.Fatalf("got %d", got)
	}
}

func TestEval7(t *testing.T) {
	if got := 7 * 2; got != 14 {
		t.Fatalf("got %d", got)
	}
}

func TestEval8(t *testing.T) {
	if got := 8 * 2; got != 16 {
		t.Fatalf("got %d", got)
	}
}

func TestEval9(t *testing.T) {
	if got := 9 * 2; got != 18 {
		t.Fatalf("got %d", got)
	}
}
