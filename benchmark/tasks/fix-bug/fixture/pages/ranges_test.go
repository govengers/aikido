package pages

import (
	"reflect"
	"testing"
)

func TestParse(t *testing.T) {
	cases := []struct {
		in    string
		total int
		want  []int
	}{
		{"1-3,5", 10, []int{1, 2, 3, 5}},
		{"", 3, []int{1, 2, 3}},
		{" 2 , 4-5 ", 6, []int{2, 4, 5}},
		{"8-", 9, []int{8, 9}},
		{"-2", 9, []int{1, 2}},
		{"3-4,4-6", 10, []int{3, 4, 5, 6}},
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

func TestParseErrors(t *testing.T) {
	for _, in := range []string{"0", "11", "5-3", "a", "1,,2"} {
		if _, err := Parse(in, 10); err == nil {
			t.Errorf("Parse(%q) succeeded, want error", in)
		}
	}
}
