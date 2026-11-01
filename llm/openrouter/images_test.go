package openrouter

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/govengers/aikido/llm"
)

func TestGenerateImage_RequestOnWire(t *testing.T) {
	t.Parallel()
	var (
		gotPath   string
		gotHeader http.Header
		gotBody   []byte
	)
	server := newTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
		gotPath = request.URL.Path
		gotHeader = request.Header.Clone()
		gotBody, _ = io.ReadAll(request.Body)
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write(loadFixture(t, "images_b64.json"))
	})

	client, err := NewClient(&Options{
		APIKey:      "sk-test",
		BaseURL:     server.URL,
		HTTPReferer: "https://github.com/govengers/aikido",
		XTitle:      "aikido CLI",
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if _, err := client.GenerateImage(context.Background(), llm.ImageRequest{
		Model:       "openai/gpt-image-2.5-flare",
		Prompt:      "a red cube on white",
		AspectRatio: "9:16",
		ImageSize:   "1K",
		Quality:     "high",
		References:  []llm.ImagePart{{URL: "data:image/jpeg;base64,QUJD", ContentType: "image/jpeg"}},
	}); err != nil {
		t.Fatalf("GenerateImage: %v", err)
	}

	if gotPath != "/images" {
		t.Errorf("path = %q, want /images", gotPath)
	}
	if got := gotHeader.Get("Accept"); got != "application/json" {
		t.Errorf("Accept = %q, want application/json", got)
	}
	if got := gotHeader.Get("Authorization"); got != "Bearer sk-test" {
		t.Errorf("Authorization = %q", got)
	}
	if got := gotHeader.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := gotHeader.Get("HTTP-Referer"); got != "https://github.com/govengers/aikido" {
		t.Errorf("HTTP-Referer = %q", got)
	}
	if got := gotHeader.Get("X-Title"); got != "aikido CLI" {
		t.Errorf("X-Title = %q", got)
	}

	var body map[string]any
	if err := json.Unmarshal(gotBody, &body); err != nil {
		t.Fatalf("decode body: %v (%s)", err, gotBody)
	}
	want := map[string]any{
		"model":        "openai/gpt-image-2.5-flare",
		"prompt":       "a red cube on white",
		"aspect_ratio": "9:16",
		"resolution":   "1K",
		"quality":      "high",
	}
	for field, value := range want {
		if body[field] != value {
			t.Errorf("body[%q] = %v, want %v", field, body[field], value)
		}
	}
	if _, ok := body["size"]; ok {
		t.Error("body carries a `size` field; the tier must ride on `resolution`")
	}
	references, ok := body["input_references"].([]any)
	if !ok || len(references) != 1 {
		t.Fatalf("input_references = %v", body["input_references"])
	}
	reference := references[0].(map[string]any)
	if reference["type"] != "image_url" {
		t.Errorf("reference type = %v", reference["type"])
	}
	if referenceURL := reference["image_url"].(map[string]any)["url"]; referenceURL != "data:image/jpeg;base64,QUJD" {
		t.Errorf("reference url = %v", referenceURL)
	}
}

func TestGenerateImage_OmitsUnsetFields(t *testing.T) {
	t.Parallel()
	var gotBody []byte
	server := newTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
		gotBody, _ = io.ReadAll(request.Body)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write(loadFixture(t, "images_b64.json"))
	})
	client := newTestClient(t, server)

	if _, err := client.GenerateImage(context.Background(), llm.ImageRequest{
		Model:  "openai/gpt-image-2.5-flare",
		Prompt: "a red cube on white",
	}); err != nil {
		t.Fatalf("GenerateImage: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(gotBody, &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	for _, field := range []string{"aspect_ratio", "resolution", "size", "quality", "input_references"} {
		if _, ok := body[field]; ok {
			t.Errorf("body carries %q when unset: %s", field, gotBody)
		}
	}
}

func TestBuildImagesBody_Size(t *testing.T) {
	t.Parallel()
	body, err := buildImagesBody(llm.ImageRequest{Model: "m", Prompt: "p", Size: "1024x1280", Quality: "medium"})
	if err != nil {
		t.Fatalf("buildImagesBody: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["size"] != "1024x1280" {
		t.Errorf("size = %v", decoded["size"])
	}
	for _, field := range []string{"aspect_ratio", "resolution"} {
		if _, ok := decoded[field]; ok {
			t.Errorf("body carries %q next to size", field)
		}
	}
	for _, bad := range []llm.ImageRequest{
		{Model: "m", Prompt: "p", Size: "1K"},
		{Model: "m", Prompt: "p", Size: "1024x"},
		{Model: "m", Prompt: "p", Size: "1024x1280", AspectRatio: "4:5"},
		{Model: "m", Prompt: "p", Size: "1024x1280", ImageSize: "1K"},
	} {
		if _, err := buildImagesBody(bad); !errors.Is(err, llm.ErrInvalidRequest) {
			t.Errorf("Size %q with ratio %q tier %q: err = %v, want ErrInvalidRequest", bad.Size, bad.AspectRatio, bad.ImageSize, err)
		}
	}
}

func TestGenerateImage_DecodesB64(t *testing.T) {
	t.Parallel()
	server := newTestServer(t, jsonHandler(loadFixture(t, "images_b64.json")))
	client := newTestClient(t, server)

	response, err := client.GenerateImage(context.Background(), llm.ImageRequest{Model: "m", Prompt: "p"})
	if err != nil {
		t.Fatalf("GenerateImage: %v", err)
	}
	if len(response.Images) != 1 {
		t.Fatalf("images = %d, want 1", len(response.Images))
	}
	image := response.Images[0]
	if len(image.Data) == 0 {
		t.Fatal("no inline bytes decoded")
	}
	if !isPNG(image.Data) {
		t.Errorf("decoded bytes are not a PNG: %x", image.Data[:8])
	}
	if image.ContentType != "image/png" {
		t.Errorf("ContentType = %q", image.ContentType)
	}
	if image.URL != "" {
		t.Errorf("URL = %q, want empty for an inline image", image.URL)
	}
	if response.Usage == nil || response.Usage.CostUSD == 0 {
		t.Errorf("usage = %+v, want a non-zero cost", response.Usage)
	}
}

func TestGenerateImage_URLKeepsContentType(t *testing.T) {
	t.Parallel()
	server := newTestServer(t, jsonHandler(loadFixture(t, "images_url.json")))
	client := newTestClient(t, server)

	response, err := client.GenerateImage(context.Background(), llm.ImageRequest{Model: "m", Prompt: "p"})
	if err != nil {
		t.Fatalf("GenerateImage: %v", err)
	}
	if len(response.Images) != 1 {
		t.Fatalf("images = %d, want 1", len(response.Images))
	}
	if got := response.Images[0].URL; got != "https://cdn.openrouter.ai/gen/abc.png" {
		t.Errorf("URL = %q", got)
	}
	if got := response.Images[0].ContentType; got != "image/png" {
		t.Errorf("ContentType = %q, want image/png", got)
	}
	if len(response.Images[0].Data) != 0 {
		t.Error("URL datum must not carry inline bytes")
	}
}

func TestGenerateImage_SniffsContentType(t *testing.T) {
	t.Parallel()
	server := newTestServer(t, jsonHandler(loadFixture(t, "images_no_media_type.json")))
	client := newTestClient(t, server)

	response, err := client.GenerateImage(context.Background(), llm.ImageRequest{Model: "m", Prompt: "p"})
	if err != nil {
		t.Fatalf("GenerateImage: %v", err)
	}
	if len(response.Images) != 1 {
		t.Fatalf("images = %d, want 1", len(response.Images))
	}
	if got := response.Images[0].ContentType; got != "image/png" {
		t.Errorf("sniffed ContentType = %q, want image/png", got)
	}
}

func TestGenerateImage_ErrorEnvelopeNoRetry(t *testing.T) {
	t.Parallel()
	var hits int32
	server := newTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
		atomic.AddInt32(&hits, 1)
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write(loadFixture(t, "images_error_envelope.json"))
	})
	client := newTestClient(t, server)

	_, err := client.GenerateImage(context.Background(), llm.ImageRequest{Model: "m", Prompt: "p"})
	if !errors.Is(err, llm.ErrServerError) {
		t.Fatalf("err = %v, want ErrServerError", err)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("hits = %d, want 1 (a 200 envelope is parsed outside the retry loop)", got)
	}
}

func TestGenerateImage_ContentFilterEnvelope(t *testing.T) {
	t.Parallel()
	server := newTestServer(t, jsonHandler(loadFixture(t, "images_content_filter.json")))
	client := newTestClient(t, server)

	_, err := client.GenerateImage(context.Background(), llm.ImageRequest{Model: "m", Prompt: "p"})
	if !errors.Is(err, llm.ErrContentFiltered) {
		t.Fatalf("err = %v, want ErrContentFiltered", err)
	}
}

func TestGenerateImage_400NoRetry(t *testing.T) {
	t.Parallel()
	var hits int32
	server := newTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
		atomic.AddInt32(&hits, 1)
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = writer.Write(loadFixture(t, "images_400_aspect.json"))
	})
	client := newTestClient(t, server)

	_, err := client.GenerateImage(context.Background(), llm.ImageRequest{
		Model: "openai/gpt-image-2.5-flare", Prompt: "p", AspectRatio: "4:5",
	})
	if !errors.Is(err, llm.ErrInvalidRequest) {
		t.Fatalf("err = %v, want ErrInvalidRequest", err)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("hits = %d, want 1 (no retry on 400)", got)
	}
}

func TestGenerateImage_5xxRetryThenSuccess(t *testing.T) {
	t.Parallel()
	var (
		hits   int32
		bodies [][]byte
	)
	server := newTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
		rawBody, _ := io.ReadAll(request.Body)
		bodies = append(bodies, rawBody)
		if atomic.AddInt32(&hits, 1) == 1 {
			writer.WriteHeader(http.StatusServiceUnavailable)
			_, _ = writer.Write([]byte(`{"error":{"message":"upstream down"}}`))
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write(loadFixture(t, "images_b64.json"))
	})
	client := newTestClient(t, server)

	response, err := client.GenerateImage(context.Background(), llm.ImageRequest{Model: "m", Prompt: "p"})
	if err != nil {
		t.Fatalf("GenerateImage: %v", err)
	}
	if len(response.Images) != 1 {
		t.Errorf("images = %d, want 1", len(response.Images))
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Fatalf("hits = %d, want 2 (one 503 + one success)", got)
	}
	if len(bodies) != 2 || string(bodies[0]) != string(bodies[1]) {
		t.Errorf("retry sent a different body:\n%s\n%s", bodies[0], bodies[1])
	}
}

// TestGenerateImage_201IsError pins the 200-only rule (ADR-029): model-arena
// accepts any 2xx, aikido does not, so an unexpected 201 surfaces as a
// retried server error rather than being silently parsed.
func TestGenerateImage_201IsError(t *testing.T) {
	t.Parallel()
	server := newTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusCreated)
		_, _ = writer.Write(loadFixture(t, "images_b64.json"))
	})
	client := newTestClient(t, server)

	if _, err := client.GenerateImage(context.Background(), llm.ImageRequest{Model: "m", Prompt: "p"}); err == nil {
		t.Fatal("expected an error on 201")
	}
}

func TestBuildImagesBody_Validation(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name    string
		request llm.ImageRequest
	}{
		{"empty model", llm.ImageRequest{Prompt: "p"}},
		{"blank model", llm.ImageRequest{Model: "  ", Prompt: "p"}},
		{"empty prompt", llm.ImageRequest{Model: "m"}},
		{"blank prompt", llm.ImageRequest{Model: "m", Prompt: "  "}},
		{"reference without url", llm.ImageRequest{Model: "m", Prompt: "p", References: []llm.ImagePart{{Data: []byte("x")}}}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			_, err := buildImagesBody(testCase.request)
			if !errors.Is(err, llm.ErrInvalidRequest) {
				t.Errorf("err = %v, want ErrInvalidRequest", err)
			}
		})
	}
}
