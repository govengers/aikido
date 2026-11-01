package openrouter

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/govengers/aikido/llm"
)

// fakeMP4 is the smallest byte string http.DetectContentType calls video/mp4.
var fakeMP4 = []byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom")

// fakeVideoAPI serves the three video endpoints. Polls answer statuses in
// order, the last one repeating; a completed poll lists a URL unless noURL.
type fakeVideoAPI struct {
	statuses  []string
	failWith  any
	noURL     bool
	clipBytes int64 // declared Content-Length of the clip; 0 sends fakeMP4 as is
	polls     atomic.Int32
	submit    map[string]any
}

func (api *fakeVideoAPI) client(t *testing.T) *Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /videos", func(writer http.ResponseWriter, request *http.Request) {
		if err := json.NewDecoder(request.Body).Decode(&api.submit); err != nil {
			t.Error(err)
		}
		writer.WriteHeader(http.StatusAccepted)
		_, _ = writer.Write([]byte(`{"id":"job1","polling_url":"x","status":"pending"}`))
	})
	mux.HandleFunc("GET /videos/job1", func(writer http.ResponseWriter, _ *http.Request) {
		poll := int(api.polls.Add(1)) - 1
		status := api.statuses[min(poll, len(api.statuses)-1)]
		job := map[string]any{"id": "job1", "status": status}
		if status == "completed" {
			job["usage"] = map[string]any{"cost": 0.42}
			if !api.noURL {
				job["unsigned_urls"] = []string{"https://openrouter.ai/api/v1/videos/job1/content?index=0"}
			}
		}
		if status == "failed" {
			job["error"] = api.failWith
		}
		_ = json.NewEncoder(writer).Encode(job)
	})
	mux.HandleFunc("GET /videos/job1/content", func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Query().Get("index") != "0" {
			t.Errorf("index = %q", request.URL.Query().Get("index"))
		}
		writer.Header().Set("Content-Type", "application/octet-stream")
		if api.clipBytes > 0 {
			writer.Header().Set("Content-Length", strconv.FormatInt(api.clipBytes, 10))
		}
		_, _ = writer.Write(fakeMP4)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client, err := NewClient(&Options{APIKey: "k", BaseURL: server.URL, VideoPollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestGenerateVideo_SubmitsPollsAndDownloads(t *testing.T) {
	t.Parallel()
	api := &fakeVideoAPI{statuses: []string{"pending", "in_progress", "completed"}}
	off, seed := false, 7
	response, err := api.client(t).GenerateVideo(context.Background(), llm.VideoRequest{
		Model: "kwaivgi/kling-v3.0-pro", Prompt: "the cup steams", AspectRatio: "9:16", Resolution: "720p",
		DurationSeconds: 5, GenerateAudio: &off, Seed: &seed,
		FirstFrame: &llm.ImagePart{Data: []byte("jpg"), ContentType: "image/jpeg"},
		LastFrame:  &llm.ImagePart{URL: "https://example.com/last.png"},
	})
	if err != nil {
		t.Fatalf("GenerateVideo: %v", err)
	}
	if string(response.Video) != string(fakeMP4) || response.ContentType != "video/mp4" || response.JobID != "job1" {
		t.Fatalf("response = %q %q %q", response.Video, response.ContentType, response.JobID)
	}
	if response.Usage == nil || response.Usage.CostUSD != 0.42 || response.Usage.Duration <= 0 {
		t.Fatalf("usage = %+v", response.Usage)
	}
	if polls := api.polls.Load(); polls != 3 {
		t.Fatalf("polled %d times, want 3", polls)
	}
	submitted := api.submit
	if submitted["aspect_ratio"] != "9:16" || submitted["resolution"] != "720p" || submitted["duration"] != 5.0 || submitted["generate_audio"] != false || submitted["seed"] != 7.0 {
		t.Fatalf("submit body = %+v", submitted)
	}
	frames, _ := submitted["frame_images"].([]any)
	if len(frames) != 2 {
		t.Fatalf("frames = %+v", frames)
	}
	first := frames[0].(map[string]any)
	if first["frame_type"] != "first_frame" || !strings.HasPrefix(first["image_url"].(map[string]any)["url"].(string), "data:image/jpeg;base64,") {
		t.Fatalf("first frame = %+v", first)
	}
	if last := frames[1].(map[string]any); last["frame_type"] != "last_frame" || last["image_url"].(map[string]any)["url"] != "https://example.com/last.png" {
		t.Fatalf("last frame = %+v", last)
	}
}

func TestBuildVideoBody_OptionalFieldsStayOffTheWire(t *testing.T) {
	t.Parallel()
	body, err := buildVideoBody(llm.VideoRequest{Model: "m", Prompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != `{"model":"m","prompt":"p"}` {
		t.Fatalf("body = %s", body)
	}
	for _, request := range []llm.VideoRequest{
		{Model: "m"},
		{Prompt: "p"},
		{Model: "m", Prompt: "p", Size: "1280x720", Resolution: "720p"},
		{Model: "m", Prompt: "p", FirstFrame: &llm.ImagePart{}},
	} {
		if _, err := buildVideoBody(request); !errors.Is(err, llm.ErrInvalidRequest) {
			t.Errorf("%+v: err = %v, want ErrInvalidRequest", request, err)
		}
	}
}

func TestGenerateVideo_Refusals(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name string
		api  *fakeVideoAPI
		want error
	}{
		{"moderation object", &fakeVideoAPI{statuses: []string{"pending", "failed"}, failWith: map[string]any{"message": "content flagged by moderation"}}, llm.ErrContentFiltered},
		{"plain failure string", &fakeVideoAPI{statuses: []string{"failed"}, failWith: "gpu fell over"}, llm.ErrServerError},
		{"completed without output", &fakeVideoAPI{statuses: []string{"completed"}, noURL: true}, llm.ErrContentFiltered},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			response, err := testCase.api.client(t).GenerateVideo(context.Background(), llm.VideoRequest{Model: "m", Prompt: "p"})
			if !errors.Is(err, testCase.want) {
				t.Fatalf("err = %v, want %v", err, testCase.want)
			}
			if response.JobID != "job1" {
				t.Errorf("JobID = %q, want it kept on failure", response.JobID)
			}
		})
	}
}

func TestGenerateVideo_SubmitErrors(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		status int
		body   string
		want   error
	}{
		{400, `{"error":{"message":"prompt violates our content policy"}}`, llm.ErrContentFiltered},
		{400, `{"error":{"message":"Duration 5s is not supported"}}`, llm.ErrInvalidRequest},
		{402, `{"error":{"message":"insufficient credits"}}`, llm.ErrAuthentication},
	}
	for _, testCase := range testCases {
		server := newTestServer(t, func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(testCase.status)
			_, _ = writer.Write([]byte(testCase.body))
		})
		client, err := NewClient(&Options{APIKey: "k", BaseURL: server.URL, MaximumAttempts: 1})
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.GenerateVideo(context.Background(), llm.VideoRequest{Model: "m", Prompt: "p"})
		if !errors.Is(err, testCase.want) {
			t.Errorf("%d %s: err = %v, want %v", testCase.status, testCase.body, err, testCase.want)
		}
	}
}

// TestGenerateVideo_ClipAboveTheCap pins maximumVideoBytes: a clip declared
// larger is refused before it is read, with the JobID kept.
func TestGenerateVideo_ClipAboveTheCap(t *testing.T) {
	t.Parallel()
	api := &fakeVideoAPI{statuses: []string{"completed"}, clipBytes: maximumVideoBytes + 1}
	response, err := api.client(t).GenerateVideo(context.Background(), llm.VideoRequest{Model: "m", Prompt: "p"})
	if !errors.Is(err, llm.ErrInvalidResponse) {
		t.Fatalf("err = %v, want ErrInvalidResponse", err)
	}
	if response.JobID != "job1" {
		t.Errorf("JobID = %q, want job1", response.JobID)
	}
}

func TestGenerateVideo_ContextEndsPolling(t *testing.T) {
	t.Parallel()
	api := &fakeVideoAPI{statuses: []string{"pending"}}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := api.client(t).GenerateVideo(ctx, llm.VideoRequest{Model: "m", Prompt: "p"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
}

func TestVideoModels_Decodes(t *testing.T) {
	t.Parallel()
	server := newTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/videos/models" {
			t.Errorf("path = %s", request.URL.Path)
		}
		_, _ = writer.Write([]byte(`{"data":[{"id":"google/veo-3.1-lite","supported_durations":[4,6,8],"supported_resolutions":["720p"],"supported_frame_images":["first_frame"],"generate_audio":true,"seed":null,"pricing_skus":{"duration_seconds_720p":"0.03"}}]}`))
	})
	client := newTestClient(t, server)
	models, err := client.VideoModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	model := models[0]
	if model.ID != "google/veo-3.1-lite" || len(model.SupportedDurations) != 3 || model.GenerateAudio == nil || !*model.GenerateAudio || model.Seed != nil || model.PricingSKUs["duration_seconds_720p"] != "0.03" {
		t.Fatalf("model = %+v", model)
	}
}
