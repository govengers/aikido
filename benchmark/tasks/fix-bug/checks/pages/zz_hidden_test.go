package pages

import (
	"reflect"
	"testing"
)

func TestBenchHidden(t *testing.T) {
	cases := []struct {
		in    string
		total int
		want  []int
	}{
		{"7", 7, []int{7}},
		{"1-1", 4, []int{1}},
		{"2-4,9-10", 12, []int{2, 3, 4, 9, 10}},
		{"-", 3, []int{1, 2, 3}},
		{"", 1, []int{1}},
	}
	for _, c := range cases {
		got, err := Parse(c.in, c.total)
		if err != nil {
			t.Fatalf("Parse(%q, %d): %v", c.in, c.total, err)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("Parse(%q, %d) = %v, want %v", c.in, c.total, got, c.want)
		}
	}
}
