package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/llm/llmtest"
)

func TestVideoCommand_WritesClipAndThreadsFlags(t *testing.T) {
	clip := []byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom")
	stub := llmtest.NewStubClient()
	stub.ScriptVideos(llmtest.VideoScript{Response: llm.VideoResponse{
		Video: clip, ContentType: "video/mp4", JobID: "job1",
		Usage: &llm.Usage{CostUSD: 0.12, Duration: 48 * time.Second},
	}})
	directory := t.TempDir()
	frame := filepath.Join(directory, "first.png")
	if err := os.WriteFile(frame, fakePNG, 0o600); err != nil {
		t.Fatal(err)
	}

	var out, notices bytes.Buffer
	app := NewApp(func() (llm.Client, error) { return stub, nil })
	app.Writer, app.ErrWriter = &out, &notices
	err := app.Run(context.Background(), []string{
		"aikido", "video", "--out", directory, "-d", "4", "-r", "720p", "-a", "9:16",
		"--audio=false", "--first-frame", frame, "a", "cat", "yawns",
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if want := "aikido: rendering with " + defaultVideoModel + ", this takes one to five minutes\n"; notices.String() != want {
		t.Errorf("stderr = %q, want %q", notices.String(), want)
	}

	requests := stub.VideoRequests()
	if len(requests) != 1 {
		t.Fatalf("requests = %d", len(requests))
	}
	request := requests[0]
	if request.Model != defaultVideoModel || request.Prompt != "a cat yawns" || request.DurationSeconds != 4 || request.Resolution != "720p" || request.AspectRatio != "9:16" {
		t.Errorf("request = %+v", request)
	}
	if request.GenerateAudio == nil || *request.GenerateAudio || request.Seed != nil {
		t.Errorf("audio/seed = %v/%v, want false/unset", request.GenerateAudio, request.Seed)
	}
	if request.FirstFrame == nil || !strings.HasPrefix(request.FirstFrame.URL, "data:image/png;base64,") || request.LastFrame != nil {
		t.Errorf("frames = %+v / %+v", request.FirstFrame, request.LastFrame)
	}
	matches, _ := filepath.Glob(filepath.Join(directory, "video-*.mp4"))
	if len(matches) != 1 {
		t.Fatalf("clips written = %v", matches)
	}
	if got, _ := os.ReadFile(matches[0]); !bytes.Equal(got, clip) {
		t.Error("clip bytes differ")
	}
	if !strings.Contains(out.String(), "cost=$0.1200") || !strings.Contains(out.String(), "job=job1") {
		t.Errorf("output = %q", out.String())
	}
}

func TestVideoCommand_AudioUnsetStaysNil(t *testing.T) {
	stub := llmtest.NewStubClient()
	stub.ScriptVideos(llmtest.VideoScript{Err: llm.ErrContentFiltered, Response: llm.VideoResponse{JobID: "job9"}})
	app := NewApp(func() (llm.Client, error) { return stub, nil })
	app.Writer = &bytes.Buffer{}
	err := app.Run(context.Background(), []string{"aikido", "video", "--out", t.TempDir(), "p"})
	if !errors.Is(err, llm.ErrContentFiltered) || !strings.Contains(err.Error(), "job9") {
		t.Fatalf("err = %v, want the filter error with the job id", err)
	}
	if request := stub.VideoRequests()[0]; request.GenerateAudio != nil || request.DurationSeconds != 0 {
		t.Errorf("unset flags were sent: %+v", request)
	}
}

func TestDurations(t *testing.T) {
	testCases := []struct {
		input []int
		want  string
	}{
		{[]int{3, 4, 5, 6}, "3-6"},
		{[]int{8, 4, 6}, "4,6,8"},
		{[]int{5, 10}, "5,10"},
		{nil, "-"},
	}
	for _, testCase := range testCases {
		if got := durations(testCase.input); got != testCase.want {
			t.Errorf("durations(%v) = %q, want %q", testCase.input, got, testCase.want)
		}
	}
}
