//go:build smoke

package cli

import (
	"bytes"
	"context"
	"image"
	_ "image/jpeg"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/govengers/aikido/llm/openrouter"
)

// recordingTransport remembers the path and body of the last request so the
// smoke test can assert which endpoint actually took the call.
type recordingTransport struct {
	mutex sync.Mutex
	path  string
	body  []byte
}

func (rt *recordingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	var body []byte
	if request.Body != nil && request.GetBody != nil {
		if bodyReader, err := request.GetBody(); err == nil {
			buffer := new(bytes.Buffer)
			_, _ = buffer.ReadFrom(bodyReader)
			_ = bodyReader.Close()
			body = buffer.Bytes()
		}
	}
	rt.mutex.Lock()
	rt.path = request.URL.Path
	rt.body = body
	rt.mutex.Unlock()
	return http.DefaultTransport.RoundTrip(request)
}

func (rt *recordingTransport) snapshot() (string, []byte) {
	rt.mutex.Lock()
	defer rt.mutex.Unlock()
	return rt.path, rt.body
}

// TestSmoke_ImageGeneration is the acceptance check from the plan, run live
// against OpenRouter. It costs roughly 0.05 USD per run and never runs in CI:
// it needs both the `smoke` build tag and AIKIDO_LIVE_SMOKE=1.
//
//	just smoke
func TestSmoke_ImageGeneration(t *testing.T) {
	if os.Getenv("AIKIDO_LIVE_SMOKE") != "1" {
		t.Skip("set AIKIDO_LIVE_SMOKE=1 to run the live smoke test")
	}
	apiKey := os.Getenv("OPENROUTER_API_KEY")
	if apiKey == "" {
		t.Skip("OPENROUTER_API_KEY is not set")
	}

	testCases := []struct {
		name     string
		model    string
		list     []string
		wantPath string
		wantPNG  bool
	}{
		{
			name:     "flare_images_api",
			model:    defaultImageModel,
			list:     defaultImagesAPIModels,
			wantPath: "/api/v1/images",
			wantPNG:  true,
		},
		{
			name:     "gemini_chat_path",
			model:    "google/gemini-3.1-flash-image-preview",
			list:     defaultImagesAPIModels,
			wantPath: "/api/v1/chat/completions",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			transport := &recordingTransport{}
			client, err := openrouter.NewClient(&openrouter.Options{
				APIKey:      apiKey,
				HTTPClient:  &http.Client{Transport: transport},
				HTTPReferer: "https://github.com/govengers/aikido",
				XTitle:      "aikido CLI",
			})
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}

			directory := t.TempDir()
			var out bytes.Buffer
			err = runImage(context.Background(), imageOptions{
				model:           testCase.model,
				outputDirectory: directory,
				prompt:          "a red cube on white",
				maximumTokens:   1024,
				imagesAPIModels: testCase.list,
			}, &out, client)
			if err != nil {
				t.Fatalf("runImage: %v\n%s", err, out.String())
			}

			path, body := transport.snapshot()
			if path != testCase.wantPath {
				t.Errorf("endpoint = %q, want %q", path, testCase.wantPath)
			}
			if !bytes.Contains(body, []byte(`"`+testCase.model+`"`)) {
				t.Errorf("request body does not name %s: %s", testCase.model, body)
			}
			assertOneDecodableImage(t, directory, testCase.wantPNG)
		})
	}
}

// assertOneDecodableImage checks that directory holds exactly one file that
// decodes as an image, a PNG when wantPNG.
func assertOneDecodableImage(t *testing.T, directory string, wantPNG bool) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("read out directory: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("wrote %d files, want 1", len(entries))
	}
	imageFile, err := os.Open(filepath.Join(directory, entries[0].Name())) //nolint:gosec // G304: the test's own temp output
	if err != nil {
		t.Fatalf("open image: %v", err)
	}
	defer imageFile.Close() // read-only
	if wantPNG {
		if _, err := png.Decode(imageFile); err != nil {
			t.Errorf("png.Decode: %v", err)
		}
		return
	}
	if _, _, err := image.Decode(imageFile); err != nil {
		t.Errorf("image.Decode: %v", err)
	}
}
