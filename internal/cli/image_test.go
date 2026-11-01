package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/llm/llmtest"
	"github.com/govengers/aikido/llm/openrouter"
)

var fakePNG = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 'x', 'x'}

func TestRunImage_WritesInlineImageToDisk(t *testing.T) {
	stub := llmtest.NewStubClient(llmtest.TurnScript{Events: []llm.Event{
		{Kind: llm.EventImage, Image: &llm.ImagePart{ContentType: "image/png", Data: fakePNG}},
		{Kind: llm.EventUsage, Usage: &llm.Usage{PromptTokens: 5, CompletionTokens: 0, CostUSD: 0.0002}},
		{Kind: llm.EventEnd},
	}})
	directory := t.TempDir()
	var out bytes.Buffer
	err := runImage(context.Background(), imageOptions{
		model:           "google/gemini-3.1-flash-image-preview",
		outputDirectory: directory,
		prompt:          "a fox",
		maximumTokens:   256,
	}, &out, stub)
	if err != nil {
		t.Fatalf("runImage: %v", err)
	}

	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("read tmp directory: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 file, got %d: %+v", len(entries), entries)
	}
	if !strings.HasSuffix(entries[0].Name(), ".png") {
		t.Errorf("unexpected extension: %s", entries[0].Name())
	}
	data, err := os.ReadFile(filepath.Join(directory, entries[0].Name())) //nolint:gosec // G304: the test reads its own temp output
	if err != nil {
		t.Fatalf("read image: %v", err)
	}
	if !bytes.Equal(data, fakePNG) {
		t.Errorf("file contents mismatch")
	}
	if !strings.Contains(out.String(), "[usage]") {
		t.Errorf("missing usage line: %q", out.String())
	}

	requests := stub.Requests()
	if len(requests) != 1 || requests[0].Model != "google/gemini-3.1-flash-image-preview" {
		t.Errorf("model not threaded: %+v", requests)
	}
}

func TestRunImage_PrintsURLWhenNoInlineData(t *testing.T) {
	stub := llmtest.NewStubClient(llmtest.TurnScript{Events: []llm.Event{
		{Kind: llm.EventImage, Image: &llm.ImagePart{URL: "https://example.com/x.png", ContentType: "image/png"}},
		{Kind: llm.EventEnd},
	}})
	directory := t.TempDir()
	var out bytes.Buffer
	if err := runImage(context.Background(), imageOptions{model: "m", outputDirectory: directory, prompt: "hi"}, &out, stub); err != nil {
		t.Fatalf("runImage: %v", err)
	}
	if !strings.Contains(out.String(), "https://example.com/x.png") {
		t.Errorf("missing URL line: %q", out.String())
	}
	entries, _ := os.ReadDir(directory)
	if len(entries) != 0 {
		t.Errorf("unexpected files for URL-only image: %+v", entries)
	}
}

func TestRunImage_RequiresPrompt(t *testing.T) {
	stub := llmtest.NewStubClient()
	var out bytes.Buffer
	if err := runImage(context.Background(), imageOptions{model: "m", outputDirectory: t.TempDir()}, &out, stub); err == nil {
		t.Error("expected error for missing prompt")
	}
}

func TestRunImage_ErrorsWhenNoImages(t *testing.T) {
	stub := llmtest.NewStubClient(llmtest.TurnScript{Events: []llm.Event{
		{Kind: llm.EventTextDelta, Text: "no images here"},
		{Kind: llm.EventEnd},
	}})
	var out bytes.Buffer
	err := runImage(context.Background(), imageOptions{model: "m", outputDirectory: t.TempDir(), prompt: "hi"}, &out, stub)
	if err == nil || !strings.Contains(err.Error(), "no images") {
		t.Errorf("expected 'no images' error, got %v", err)
	}
}

func TestImageCommand_EnvVarOverridesModel(t *testing.T) {
	t.Setenv("OPENROUTER_IMAGE_MODEL", "custom/model-from-env")

	captured := make(chan llm.Request, 1)
	factory := func() (llm.Client, error) {
		stub := llmtest.NewStubClient(llmtest.TurnScript{Events: []llm.Event{
			{Kind: llm.EventImage, Image: &llm.ImagePart{ContentType: "image/png", Data: fakePNG}},
			{Kind: llm.EventEnd},
		}})
		return &capturingClient{inner: stub, sink: captured}, nil
	}

	app := NewApp(factory)
	directory := t.TempDir()
	if err := app.Run(context.Background(), []string{"aikido", "image", "--out", directory, "say hi"}); err != nil {
		t.Fatalf("app.Run: %v", err)
	}
	select {
	case request := <-captured:
		if request.Model != "custom/model-from-env" {
			t.Errorf("model = %q, want custom/model-from-env", request.Model)
		}
	default:
		t.Fatal("no request captured")
	}
}

func TestImageCommand_ThreadsAspectAndSize(t *testing.T) {
	captured := make(chan llm.Request, 1)
	factory := func() (llm.Client, error) {
		stub := llmtest.NewStubClient(llmtest.TurnScript{Events: []llm.Event{
			{Kind: llm.EventImage, Image: &llm.ImagePart{ContentType: "image/png", Data: fakePNG}},
			{Kind: llm.EventEnd},
		}})
		return &capturingClient{inner: stub, sink: captured}, nil
	}
	app := NewApp(factory)
	directory := t.TempDir()
	arguments := []string{"aikido", "image", "--out", directory, "-m", "google/gemini-3.1-flash-image-preview", "--aspect", "16:9", "--size", "2K", "wide shot"}
	if err := app.Run(context.Background(), arguments); err != nil {
		t.Fatalf("app.Run: %v", err)
	}
	select {
	case request := <-captured:
		if request.ImageConfiguration == nil {
			t.Fatal("ImageConfig not threaded")
		}
		if request.ImageConfiguration.AspectRatio != "16:9" {
			t.Errorf("AspectRatio = %q, want 16:9", request.ImageConfiguration.AspectRatio)
		}
		if request.ImageConfiguration.ImageSize != "2K" {
			t.Errorf("ImageSize = %q, want 2K", request.ImageConfiguration.ImageSize)
		}
	default:
		t.Fatal("no request captured")
	}
}

func TestImageCommand_OmitsImageConfigWhenUnset(t *testing.T) {
	captured := make(chan llm.Request, 1)
	factory := func() (llm.Client, error) {
		stub := llmtest.NewStubClient(llmtest.TurnScript{Events: []llm.Event{
			{Kind: llm.EventImage, Image: &llm.ImagePart{ContentType: "image/png", Data: fakePNG}},
			{Kind: llm.EventEnd},
		}})
		return &capturingClient{inner: stub, sink: captured}, nil
	}
	app := NewApp(factory)
	directory := t.TempDir()
	if err := app.Run(context.Background(), []string{"aikido", "image", "--out", directory, "-m", "google/gemini-3.1-flash-image-preview", "hi"}); err != nil {
		t.Fatalf("app.Run: %v", err)
	}
	select {
	case request := <-captured:
		if request.ImageConfiguration != nil {
			t.Errorf("ImageConfig = %+v, want nil when flags unset", request.ImageConfiguration)
		}
	default:
		t.Fatal("no request captured")
	}
}

type capturingClient struct {
	inner llm.Client
	sink  chan<- llm.Request
}

func (c *capturingClient) Stream(ctx context.Context, request llm.Request) (<-chan llm.Event, error) {
	select {
	case c.sink <- request:
	default:
	}
	return c.inner.Stream(ctx, request)
}

func (c *capturingClient) Complete(ctx context.Context, request llm.Request) (llm.Response, error) {
	select {
	case c.sink <- request:
	default:
	}
	return c.inner.Complete(ctx, request)
}

func (c *capturingClient) GenerateImage(ctx context.Context, request llm.ImageRequest) (llm.ImageResponse, error) {
	generator, ok := c.inner.(llm.ImageGenerator)
	if !ok {
		return llm.ImageResponse{}, errors.New("inner client is not an llm.ImageGenerator")
	}
	return generator.GenerateImage(ctx, request)
}

// chatOnlyClient implements llm.Client and nothing else, standing in for a
// provider that has no images endpoint.
type chatOnlyClient struct{ inner llm.Client }

func (c *chatOnlyClient) Stream(ctx context.Context, request llm.Request) (<-chan llm.Event, error) {
	return c.inner.Stream(ctx, request)
}

func (c *chatOnlyClient) Complete(ctx context.Context, request llm.Request) (llm.Response, error) {
	return c.inner.Complete(ctx, request)
}

// imagesStub returns a stub scripted with one PNG on the images endpoint.
func imagesStub() *llmtest.StubClient {
	stub := llmtest.NewStubClient()
	stub.ScriptImages(llmtest.ImageScript{Response: llm.ImageResponse{
		Images: []llm.ImagePart{{ContentType: "image/png", Data: fakePNG}},
		Usage:  &llm.Usage{CostUSD: 0.0053},
	}})
	return stub
}

func TestNormalizeModelID(t *testing.T) {
	testCases := map[string]string{
		"openai/gpt-image-2.5-flare":      "openai/gpt-image-2.5-flare",
		"  openai/gpt-image-2.5-flare  ":  "openai/gpt-image-2.5-flare",
		"openai/gpt-image-2.5-flare:free": "openai/gpt-image-2.5-flare",
		" meta/muse-image:nitro ":         "meta/muse-image",
		"":                                "",
	}
	for input, want := range testCases {
		if got := normalizeModelID(input); got != want {
			t.Errorf("normalizeModelID(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestUsesImagesAPI(t *testing.T) {
	list := defaultImagesAPIModels
	testCases := []struct {
		model string
		want  bool
	}{
		{"openai/gpt-image-2.5-flare", true},
		{"openai/gpt-image-2.5-flare:free", true},
		{"  openai/gpt-image-2.5-flare  ", true},
		{"meta/muse-image", true},
		{"google/gemini-3.1-flash-image-preview", false},
		{"openai/gpt-5.4-image-2", false},
		{"", false},
	}
	for _, testCase := range testCases {
		if got := usesImagesAPI(testCase.model, list); got != testCase.want {
			t.Errorf("usesImagesAPI(%q) = %v, want %v", testCase.model, got, testCase.want)
		}
	}
	// A list entry may carry the same noise the model id does.
	if !usesImagesAPI("x/y", []string{" x/y:free "}) {
		t.Error("list entries must be normalized too")
	}
	if usesImagesAPI("openai/gpt-image-2.5-flare", []string{"none"}) {
		t.Error(`"none" must disable images routing`)
	}
}

func TestParseImagesAPIModels(t *testing.T) {
	testCases := []struct {
		input string
		want  []string
	}{
		{"a/b,c/d", []string{"a/b", "c/d"}},
		{" a/b , c/d ", []string{"a/b", "c/d"}},
		{"a/b,,c/d,", []string{"a/b", "c/d"}},
		{"none", []string{"none"}},
		{"", defaultImagesAPIModels},
		{"  ,  ", defaultImagesAPIModels},
	}
	for _, testCase := range testCases {
		got := parseImagesAPIModels(testCase.input)
		if strings.Join(got, "|") != strings.Join(testCase.want, "|") {
			t.Errorf("parseImagesAPIModels(%q) = %v, want %v", testCase.input, got, testCase.want)
		}
	}
}

// clearImageEnv removes both image env vars for the duration of the test.
func clearImageEnv(t *testing.T) {
	t.Helper()
	unsetForTest(t, "OPENROUTER_IMAGE_MODEL")
	unsetForTest(t, "OPENROUTER_IMAGES_API_MODELS")
}

func TestImageExtension(t *testing.T) {
	testCases := map[string]string{
		"image/png":  ".png",
		"IMAGE/JPEG": ".jpg",
		"image/jpg":  ".jpg",
		"image/webp": ".webp",
		"image/gif":  ".gif",
		"image/avif": ".bin",
		"":           ".bin",
	}
	for contentType, want := range testCases {
		if got := imageExtension(contentType); got != want {
			t.Errorf("imageExtension(%q) = %q, want %q", contentType, got, want)
		}
	}
}

func TestImageCommand_DefaultModelUsesImagesAPI(t *testing.T) {
	clearImageEnv(t)
	stub := imagesStub()
	app := NewApp(func() (llm.Client, error) { return stub, nil })
	directory := t.TempDir()
	if err := app.Run(context.Background(), []string{"aikido", "image", "--out", directory, "a red cube on white"}); err != nil {
		t.Fatalf("app.Run: %v", err)
	}

	requests := stub.ImageRequests()
	if len(requests) != 1 {
		t.Fatalf("image requests = %d, want 1", len(requests))
	}
	if requests[0].Model != defaultImageModel {
		t.Errorf("model = %q, want %q", requests[0].Model, defaultImageModel)
	}
	if requests[0].Prompt != "a red cube on white" {
		t.Errorf("prompt = %q", requests[0].Prompt)
	}
	if calls := len(stub.Requests()); calls != 0 {
		t.Errorf("chat requests = %d, want 0 on the images path", calls)
	}
	entries, _ := os.ReadDir(directory)
	if len(entries) != 1 || !strings.HasSuffix(entries[0].Name(), ".png") {
		t.Errorf("output files = %+v, want one PNG", entries)
	}
}

func TestImageCommand_EmptyEnvFallsBackToDefault(t *testing.T) {
	clearImageEnv(t)
	t.Setenv("OPENROUTER_IMAGE_MODEL", "")
	stub := imagesStub()
	app := NewApp(func() (llm.Client, error) { return stub, nil })
	if err := app.Run(context.Background(), []string{"aikido", "image", "--out", t.TempDir(), "hi"}); err != nil {
		t.Fatalf("app.Run: %v", err)
	}
	requests := stub.ImageRequests()
	if len(requests) != 1 || requests[0].Model != defaultImageModel {
		t.Errorf("image requests = %+v, want one call on %s", requests, defaultImageModel)
	}
}

func TestImageCommand_HelpShowsFlareDefault(t *testing.T) {
	clearImageEnv(t)
	var out bytes.Buffer
	app := NewApp(func() (llm.Client, error) { return nil, errors.New("factory must not run for --help") })
	app.Writer = &out
	if err := app.Run(context.Background(), []string{"aikido", "image", "--help"}); err != nil {
		t.Fatalf("app.Run: %v", err)
	}
	help := out.String()
	if !strings.Contains(help, defaultImageModel) {
		t.Errorf("--help does not show the default model %q:\n%s", defaultImageModel, help)
	}
	if !strings.Contains(help, "OPENROUTER_IMAGE_MODEL") {
		t.Errorf("--help does not show the env override:\n%s", help)
	}
	for _, flag := range []string{"--ref", "--quality", "--images-api-models"} {
		if !strings.Contains(help, flag) {
			t.Errorf("--help does not list %s:\n%s", flag, help)
		}
	}
}

func TestImageCommand_ImagesAPIModelsEnv(t *testing.T) {
	clearImageEnv(t)
	t.Setenv("OPENROUTER_IMAGES_API_MODELS", "x/y, z/w")

	// x/y is now images-API only.
	stub := imagesStub()
	app := NewApp(func() (llm.Client, error) { return stub, nil })
	if err := app.Run(context.Background(), []string{"aikido", "image", "--out", t.TempDir(), "-m", "x/y", "hi"}); err != nil {
		t.Fatalf("app.Run: %v", err)
	}
	if requests := stub.ImageRequests(); len(requests) != 1 || requests[0].Model != "x/y" {
		t.Errorf("image requests = %+v, want one call on x/y", requests)
	}

	// flare fell off the list, so it takes chat completions.
	chat := llmtest.NewStubClient(llmtest.TurnScript{Events: []llm.Event{
		{Kind: llm.EventImage, Image: &llm.ImagePart{ContentType: "image/png", Data: fakePNG}},
		{Kind: llm.EventEnd},
	}})
	chatApp := NewApp(func() (llm.Client, error) { return chat, nil })
	if err := chatApp.Run(context.Background(), []string{"aikido", "image", "--out", t.TempDir(), "hi"}); err != nil {
		t.Fatalf("app.Run: %v", err)
	}
	if requests := chat.Requests(); len(requests) != 1 || requests[0].Model != defaultImageModel {
		t.Errorf("chat requests = %+v, want one call on %s", requests, defaultImageModel)
	}
	if calls := len(chat.ImageRequests()); calls != 0 {
		t.Errorf("image requests = %d, want 0", calls)
	}
}

func TestImageCommand_FlagBeatsEnv(t *testing.T) {
	clearImageEnv(t)
	t.Setenv("OPENROUTER_IMAGES_API_MODELS", "x/y")

	stub := imagesStub()
	app := NewApp(func() (llm.Client, error) { return stub, nil })
	arguments := []string{"aikido", "image", "--out", t.TempDir(), "--images-api-models", "openai/gpt-image-2.5-flare", "hi"}
	if err := app.Run(context.Background(), arguments); err != nil {
		t.Fatalf("app.Run: %v", err)
	}
	if requests := stub.ImageRequests(); len(requests) != 1 || requests[0].Model != defaultImageModel {
		t.Errorf("image requests = %+v, want the flag list to win over the env", requests)
	}
}

func TestImageCommand_ThreadsQualityAndRefs(t *testing.T) {
	clearImageEnv(t)
	referencePath := filepath.Join(t.TempDir(), "ref.png")
	if err := os.WriteFile(referencePath, fakePNG, 0o600); err != nil {
		t.Fatalf("write reference: %v", err)
	}
	stub := imagesStub()
	app := NewApp(func() (llm.Client, error) { return stub, nil })
	arguments := []string{
		"aikido", "image", "--out", t.TempDir(),
		"--quality", "high", "--aspect", "9:16", "--size", "2K", "--ref", referencePath,
		"a red cube on white",
	}
	if err := app.Run(context.Background(), arguments); err != nil {
		t.Fatalf("app.Run: %v", err)
	}
	requests := stub.ImageRequests()
	if len(requests) != 1 {
		t.Fatalf("image requests = %d, want 1", len(requests))
	}
	got := requests[0]
	if got.Quality != "high" || got.Size != "2048x3648" || got.AspectRatio != "" || got.ImageSize != "" {
		t.Errorf("request = %+v", got)
	}
	if len(got.References) != 1 {
		t.Fatalf("references = %d, want 1", len(got.References))
	}
	wantURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(fakePNG)
	if got.References[0].URL != wantURL {
		t.Errorf("reference URL = %q, want %q", got.References[0].URL, wantURL)
	}
}

func TestImageCommand_RefWithComma(t *testing.T) {
	clearImageEnv(t)
	referencePath := filepath.Join(t.TempDir(), "a,b.png")
	if err := os.WriteFile(referencePath, fakePNG, 0o600); err != nil {
		t.Fatalf("write reference: %v", err)
	}
	stub := imagesStub()
	app := NewApp(func() (llm.Client, error) { return stub, nil })
	if err := app.Run(context.Background(), []string{"aikido", "image", "--out", t.TempDir(), "--ref", referencePath, "hi"}); err != nil {
		t.Fatalf("app.Run: %v", err)
	}
	requests := stub.ImageRequests()
	if len(requests) != 1 || len(requests[0].References) != 1 {
		t.Fatalf("a comma in the path split the flag: %+v", requests)
	}
}

func TestRunImage_RefsOnChatPath(t *testing.T) {
	directory := t.TempDir()
	referencePath := filepath.Join(directory, "ref.png")
	if err := os.WriteFile(referencePath, fakePNG, 0o600); err != nil {
		t.Fatalf("write reference: %v", err)
	}
	stub := llmtest.NewStubClient(llmtest.TurnScript{Events: []llm.Event{
		{Kind: llm.EventImage, Image: &llm.ImagePart{ContentType: "image/png", Data: fakePNG}},
		{Kind: llm.EventEnd},
	}})
	var out bytes.Buffer
	err := runImage(context.Background(), imageOptions{
		model:           "google/gemini-3.1-flash-image-preview",
		outputDirectory: directory,
		prompt:          "a fox",
		referencePaths:  []string{referencePath},
	}, &out, stub)
	if err != nil {
		t.Fatalf("runImage: %v", err)
	}
	requests := stub.Requests()
	if len(requests) != 1 || len(requests[0].Messages) != 1 {
		t.Fatalf("requests = %+v", requests)
	}
	images := requests[0].Messages[0].Images
	if len(images) != 1 || !strings.HasPrefix(images[0].URL, "data:image/png;base64,") {
		t.Errorf("Messages[0].Images = %+v", images)
	}
}

func TestRunImage_QualityRejectedOnChatPath(t *testing.T) {
	stub := llmtest.NewStubClient()
	var out bytes.Buffer
	err := runImage(context.Background(), imageOptions{
		model:           "google/gemini-3.1-flash-image-preview",
		outputDirectory: t.TempDir(),
		prompt:          "hi",
		quality:         "high",
	}, &out, stub)
	if err == nil || !strings.Contains(err.Error(), "--quality") {
		t.Errorf("err = %v, want a --quality rejection", err)
	}
}

func TestRunImage_ClientWithoutImageGenerator(t *testing.T) {
	client := &chatOnlyClient{inner: llmtest.NewStubClient()}
	var out bytes.Buffer
	err := runImage(context.Background(), imageOptions{
		model:           defaultImageModel,
		outputDirectory: t.TempDir(),
		prompt:          "hi",
		imagesAPIModels: defaultImagesAPIModels,
	}, &out, client)
	if err == nil || !strings.Contains(err.Error(), "images endpoint") {
		t.Errorf("err = %v, want a clear capability error", err)
	}
}

func writeReferenceFile(t *testing.T, path string, content []byte) string {
	t.Helper()
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadReferenceImages(t *testing.T) {
	directory := t.TempDir()
	png := writeReferenceFile(t, filepath.Join(directory, "a.png"), fakePNG)
	// No extension: the MIME type has to be sniffed from the bytes.
	sniffed := writeReferenceFile(t, filepath.Join(directory, "noext"), fakePNG)

	if got, err := loadReferenceImages(nil); err != nil || got != nil {
		t.Errorf("loadReferenceImages(nil) = %+v, %v", got, err)
	}

	parts, err := loadReferenceImages([]string{png, sniffed})
	if err != nil {
		t.Fatalf("loadReferenceImages: %v", err)
	}
	if len(parts) != 2 {
		t.Fatalf("parts = %d, want 2", len(parts))
	}
	for index, part := range parts {
		if part.ContentType != "image/png" {
			t.Errorf("part %d ContentType = %q, want image/png", index, part.ContentType)
		}
		if part.URL != "data:image/png;base64,"+base64.StdEncoding.EncodeToString(fakePNG) {
			t.Errorf("part %d URL = %q", index, part.URL)
		}
	}
}

func TestLoadReferenceImages_Rejects(t *testing.T) {
	directory := t.TempDir()
	png := writeReferenceFile(t, filepath.Join(directory, "a.png"), fakePNG)
	notAnImage := writeReferenceFile(t, filepath.Join(directory, "notes.txt"), []byte("hello"))
	big := writeReferenceFile(t, filepath.Join(directory, "big.png"), append(append([]byte{}, fakePNG...), make([]byte, maximumReferenceBytes)...))

	testCases := []struct {
		name  string
		paths []string
		want  string
	}{
		{"not an image", []string{notAnImage}, "not an image"},
		{"missing file", []string{filepath.Join(directory, "missing.png")}, "no such file"},
		{"over the size budget", []string{big}, "exceed"},
		// The budget is for all references together, not per file.
		{"budget spans every reference", []string{png, big}, "exceed"},
		// A directory is not a regular file and must be rejected before any read.
		{"directory", []string{directory}, "regular file"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := loadReferenceImages(testCase.paths); err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Errorf("err = %v, want one containing %q", err, testCase.want)
			}
		})
	}
}

// TestLoadReferenceImages_RejectsCharacterDeviceBeforeReading pins the reason
// the size check runs on the stat rather than on the bytes: os.ReadFile on
// /dev/zero never returns. The stat gate must reject it immediately.
func TestLoadReferenceImages_RejectsCharacterDeviceBeforeReading(t *testing.T) {
	if _, err := os.Stat("/dev/zero"); err != nil {
		t.Skip("/dev/zero is not available")
	}
	done := make(chan error, 1)
	go func() {
		_, err := loadReferenceImages([]string{"/dev/zero"})
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "regular file") {
			t.Errorf("err = %v, want a non-regular-file rejection", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("loadReferenceImages did not return on /dev/zero within 5s")
	}
}

func TestHintImagesAPI(t *testing.T) {
	if hintImagesAPI(nil) != nil {
		t.Error("nil error must stay nil")
	}
	for _, unrelated := range []error{
		&openrouter.APIError{StatusCode: 401, Message: "bad key"},
		errors.New("openrouter: status 404: only available via the /api/v1/images endpoint"), // text alone is no signal
	} {
		if got := hintImagesAPI(unrelated); got.Error() != unrelated.Error() {
			t.Errorf("unrelated error was rewritten: %v", got)
		}
	}
	for _, apiError := range []*openrouter.APIError{
		{StatusCode: 404, Message: "This model is only available via the /api/v1/images endpoint"},
		{StatusCode: 400, Message: "model does not support the requested output modalities"},
	} {
		wrapped := fmt.Errorf("complete: %w", apiError)
		got := hintImagesAPI(wrapped)
		want := wrapped.Error() + " (model may need the images endpoint: add it to OPENROUTER_IMAGES_API_MODELS)"
		if got.Error() != want {
			t.Errorf("hintImagesAPI = %q; want %q", got, want)
		}
		if !errors.Is(got, apiError) {
			t.Errorf("hint dropped the error chain: %v", got)
		}
	}
}

func TestImagesAPISize(t *testing.T) {
	testCases := []struct{ aspect, tier, want string }{
		{"4:5", "", "1024x1280"},
		{"4:5", "1K", "1024x1280"},
		{"5:4", "1k", "1280x1024"},
		{"9:16", "", "1024x1824"},
		{"16:9", "", "1824x1024"},
		{"3:4", "", "1024x1360"},
		{"1:1", "", "1024x1024"},
		{"21:9", "", "2384x1024"},
		{"3:1", "", "3072x1024"},
		{"2:3", "2K", "2048x3072"},
		{"4:5", "2K", "2048x2560"},
		{"9:16", "2K", "2048x3648"},
		{"16:9", "4K", "3840x2160"},
		{"1:1", "4K", "2880x2880"},
		{"4:5", "4K", "2576x3216"},
		{" 4:5 ", "", "1024x1280"},
	}
	for _, testCase := range testCases {
		got, err := imagesAPISize(testCase.aspect, testCase.tier)
		if err != nil || got != testCase.want {
			t.Errorf("imagesAPISize(%q, %q) = %q, %v; want %q", testCase.aspect, testCase.tier, got, err, testCase.want)
			continue
		}
		var width, height int
		if _, err := fmt.Sscanf(got, "%dx%d", &width, &height); err != nil {
			t.Fatalf("parse %q: %v", got, err)
		}
		if width%16 != 0 || height%16 != 0 || max(width, height) > imagesMaximumEdge || width*height > imagesMaximumPixels {
			t.Errorf("%q breaks the endpoint limits", got)
		}
	}
}

func TestImagesAPISize_Unsupported(t *testing.T) {
	for _, testCase := range []struct{ aspect, tier string }{{"1:4", ""}, {"8:1", "2K"}, {"4:5", "0.5K"}} {
		if _, err := imagesAPISize(testCase.aspect, testCase.tier); !errors.Is(err, errFrameUnsupported) {
			t.Errorf("imagesAPISize(%q, %q) err = %v, want errFrameUnsupported", testCase.aspect, testCase.tier, err)
		}
	}
	for _, bad := range []string{"4x5", "a:b", "0:5", "4:", "-4:5"} {
		if _, err := imagesAPISize(bad, ""); err == nil || errors.Is(err, errFrameUnsupported) {
			t.Errorf("imagesAPISize(%q) err = %v, want a parse error", bad, err)
		}
	}
}

func TestImageCommand_DefaultModelSendsPixelsFor45(t *testing.T) {
	clearImageEnv(t)
	stub := imagesStub()
	app := NewApp(func() (llm.Client, error) { return stub, nil })
	if err := app.Run(context.Background(), []string{"aikido", "image", "--out", t.TempDir(), "--aspect", "4:5", "hi"}); err != nil {
		t.Fatalf("app.Run: %v", err)
	}
	requests := stub.ImageRequests()
	if len(requests) != 1 || requests[0].Size != "1024x1280" || requests[0].AspectRatio != "" || requests[0].ImageSize != "" {
		t.Errorf("image requests = %+v, want one call with size 1024x1280 only", requests)
	}
}

func TestImageCommand_AutoAspectPassesThrough(t *testing.T) {
	clearImageEnv(t)
	stub := imagesStub()
	app := NewApp(func() (llm.Client, error) { return stub, nil })
	if err := app.Run(context.Background(), []string{"aikido", "image", "--out", t.TempDir(), "--aspect", "auto", "hi"}); err != nil {
		t.Fatalf("app.Run: %v", err)
	}
	if requests := stub.ImageRequests(); len(requests) != 1 || requests[0].AspectRatio != "auto" || requests[0].Size != "" {
		t.Errorf("image requests = %+v, want aspect auto passed through", requests)
	}
}

func TestImageCommand_UnsupportedFrameFallsBackForDefaultModel(t *testing.T) {
	clearImageEnv(t)
	chat := llmtest.NewStubClient(llmtest.TurnScript{Events: []llm.Event{
		{Kind: llm.EventImage, Image: &llm.ImagePart{ContentType: "image/png", Data: fakePNG}},
		{Kind: llm.EventEnd},
	}})
	app := NewApp(func() (llm.Client, error) { return chat, nil })
	var stdout, stderr bytes.Buffer
	app.Writer, app.ErrWriter = &stdout, &stderr
	arguments := []string{"aikido", "image", "--out", t.TempDir(), "--aspect", "1:4", "--quality", "low", "hi"}
	if err := app.Run(context.Background(), arguments); err != nil {
		t.Fatalf("app.Run: %v", err)
	}
	wantNotice := "aikido: " + defaultImageModel + " cannot render --aspect 1:4, using " + fallbackImageModel + "\n"
	if stderr.String() != wantNotice {
		t.Errorf("stderr = %q, want the fallback notice %q", stderr.String(), wantNotice)
	}
	if strings.Contains(stdout.String(), "cannot render") {
		t.Errorf("the fallback notice leaked to stdout: %q", stdout.String())
	}
	requests := chat.Requests()
	if len(requests) != 1 || requests[0].Model != fallbackImageModel || requests[0].ImageConfiguration == nil || requests[0].ImageConfiguration.AspectRatio != "1:4" {
		t.Errorf("chat requests = %+v, want one %s call with 1:4", requests, fallbackImageModel)
	}
	if calls := len(chat.ImageRequests()); calls != 0 {
		t.Errorf("image requests = %d, want 0", calls)
	}
}

func TestImageCommand_UnsupportedFrameErrorsForExplicitModel(t *testing.T) {
	for _, viaEnv := range []bool{false, true} {
		clearImageEnv(t)
		arguments := []string{"aikido", "image", "--out", t.TempDir(), "--aspect", "1:4"}
		if viaEnv {
			t.Setenv("OPENROUTER_IMAGE_MODEL", defaultImageModel)
		} else {
			arguments = append(arguments, "-m", defaultImageModel)
		}
		stub := imagesStub()
		app := NewApp(func() (llm.Client, error) { return stub, nil })
		err := app.Run(context.Background(), append(arguments, "hi"))
		if err == nil || !strings.Contains(err.Error(), "-m "+fallbackImageModel) {
			t.Errorf("viaEnv=%v: err = %v, want a hint naming %s", viaEnv, err, fallbackImageModel)
		}
		if calls := len(stub.ImageRequests()) + len(stub.Requests()); calls != 0 {
			t.Errorf("viaEnv=%v: %d provider calls, want 0", viaEnv, calls)
		}
	}
}
