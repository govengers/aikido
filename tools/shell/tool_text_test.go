package shell

import "testing"

func TestResultModelText(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		result Result
		want   string
	}{
		{"output", Result{Output: "hi\n"}, "hi\n"},
		{"no output", Result{}, "(no output)"},
		{"failure", Result{Output: "boom\n", ExitCode: 2}, "boom\n\n[exit code 2]"},
		{"cut", Result{Output: "a…b", Truncated: true}, "a…b\n[the middle of the output was cut; filter it, e.g. with head, grep or sed -n]"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := testCase.result.ModelText(); got != testCase.want {
				t.Errorf("ModelText = %q; want %q", got, testCase.want)
			}
		})
	}
}
