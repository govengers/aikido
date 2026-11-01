package main

import "testing"

func TestChangedBytes(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"same", "same", 0},
		{"price: 4599,", "price: 4999,", 1},
		{"abc", "abXYc", 2},
		{"abc", "", 3},
		{"aaaa", "aa", 2},
	}
	for _, c := range cases {
		if got := changedBytes([]byte(c.a), []byte(c.b)); got != c.want {
			t.Errorf("changedBytes(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}
