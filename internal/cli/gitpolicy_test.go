package cli

import (
	"strings"
	"testing"
)

// TestGitPolicy replays the review's S23: an allowed git runs programs from
// -c core.sshCommand=..., -c diff.external, --upload-pack and friends.
func TestGitPolicy(t *testing.T) {
	allow := gitPolicy("/work/repo")
	testCases := []struct {
		arguments string
		want      string // substring of the error; empty means allowed
	}{
		{"status --short", ""},
		{"log -c --oneline", ""},
		{"-C /work/repo/sub status", ""},
		{"-C sub log", ""},
		{"--git-dir=/work/repo/.git log", ""},
		{"commit -m msg -- --exec", ""},
		{"-c core.sshCommand=touch fetch", "-c is not allowed"},
		{"-cdiff.external=x diff", "-cdiff.external is not allowed"},
		{"--config-env=core.pager=X log", "--config-env is not allowed"},
		{"--exec-path=/tmp/x status", "--exec-path is not allowed"},
		{"-C /etc status", "lies outside /work/repo"},
		{"-C ../other status", "must not climb"},
		{"--work-tree /home log", "lies outside"},
		{"fetch --upload-pack=touch origin", "git fetch --upload-pack is not allowed"},
		{"push --receive-pack touch origin", "git push --receive-pack is not allowed"},
		{"ls-remote --exec=x origin", "--exec is not allowed"},
		{"clone -u touch url", "git clone -u"},
		{"clone --config core.hooksPath=x url", "--config is not allowed"},
	}
	for _, testCase := range testCases {
		err := allow(strings.Fields(testCase.arguments))
		switch {
		case testCase.want == "" && err != nil:
			t.Errorf("git %s refused: %v", testCase.arguments, err)
		case testCase.want != "" && (err == nil || !strings.Contains(err.Error(), testCase.want)):
			t.Errorf("git %s: error %v, want %q", testCase.arguments, err, testCase.want)
		}
	}
}
