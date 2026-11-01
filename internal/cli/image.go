package cli

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/llm/openrouter"

	urfavecli "github.com/urfave/cli/v3"
)

// defaultImageModel is the compiled-in model. It is served only by
// OpenRouter's /api/v1/images endpoint, so it also heads defaultImagesAPIModels.
const defaultImageModel = "openai/gpt-image-2.5-flare"

// defaultImagesAPIModels are the OpenRouter models that exist only on
// /api/v1/images: they never answer on chat completions and are missing from a
// plain GET /models listing. OPENROUTER_IMAGES_API_MODELS replaces the list.
// Models that accept both endpoints (the Gemini image family,
// openai/gpt-5.4-image-2) must stay off it so they keep the chat path, which
// is the only one carrying a conversation. Never written.
var defaultImagesAPIModels = []string{
	"openai/gpt-image-2.5-flare",
	"openai/gpt-image-2.5-sunburst",
	"openai/gpt-image-2",
	"meta/muse-image",
}

// fallbackImageModel renders the frames the images endpoint cannot: ratios
// wider than 3:1 and the 0.5K tier.
const fallbackImageModel = "google/gemini-3.1-flash-image-preview"

// Images-endpoint frame limits (OpenAI gpt-image): sides multiples of 16, the
// longest edge at most 3840, at most 8,294,400 pixels, at most 3:1.
const (
	imagesMaximumEdge   = 3840
	imagesMaximumPixels = 3840 * 2160
	imagesMaximumRatio  = 3
	imagesSideStep      = 16
)

var errFrameUnsupported = errors.New("frame not supported on the images endpoint")

// maximumReferenceBytes caps the raw bytes of all --ref images together.
// Base64 inflates by a third and OpenRouter rejects more than 30 MB of
// downloaded content, so 20 MB raw is the last comfortable stop before a
// wasted round trip.
const maximumReferenceBytes = 20 << 20

func imageCommand(factory ClientFactory) *urfavecli.Command {
	return &urfavecli.Command{
		Name:      "image",
		Usage:     "Generate one or more images and write them to disk",
		ArgsUsage: "<prompt...>",
		// --ref takes file paths, which may legitimately contain commas.
		DisableSliceFlagSeparator: true,
		Flags:                     imageFlags(),
		Action: func(ctx context.Context, command *urfavecli.Command) error {
			client, err := factory()
			if err != nil {
				return err
			}
			return runImage(ctx, imageOptions{
				model:           command.String("model"),
				modelExplicit:   command.IsSet("model"),
				outputDirectory: command.String("out"),
				maximumTokens:   command.Int("max-tokens"),
				aspectRatio:     command.String("aspect"),
				imageSize:       command.String("size"),
				quality:         command.String("quality"),
				referencePaths:  command.StringSlice("ref"),
				imagesAPIModels: parseImagesAPIModels(command.String("images-api-models")),
				prompt:          promptFromArguments(command),
				warnings:        resolveErrorWriter(command),
			}, resolveWriter(command), client)
		},
	}
}

func imageFlags() []urfavecli.Flag {
	return []urfavecli.Flag{
		&urfavecli.StringFlag{
			Name:    "model",
			Aliases: []string{"m"},
			Value:   defaultImageModel,
			Sources: urfavecli.EnvVars("OPENROUTER_IMAGE_MODEL"),
			Usage:   "OpenRouter image model id (images-API models see --images-api-models)",
		},
		&urfavecli.StringFlag{
			Name:    "out",
			Aliases: []string{"o"},
			Value:   "out",
			Usage:   "Output directory for generated images",
		},
		&urfavecli.IntFlag{Name: "max-tokens", Value: 1024, Usage: "Max output tokens (chat-completions models only)"},
		&urfavecli.StringFlag{
			Name:    "aspect",
			Aliases: []string{"a"},
			Usage:   "Output aspect ratio W:H, e.g. 1:1, 4:5, 9:16, 16:9, 21:9; images-API models get the exact pixel frame (4:5 at 1K is 1024x1280), up to 3:1 (1:4/4:1/1:8/8:1 need gemini-3.1-flash-image-preview)",
		},
		&urfavecli.StringFlag{
			Name:  "size",
			Usage: "Output resolution: 1K (default), 2K, 4K (or 0.5K on gemini-3.1-flash-image-preview); with --aspect on images-API models the short edge is 1024/2048/4096, capped at 3840 long edge and 8.3 MP",
		},
		&urfavecli.StringSliceFlag{
			Name:  "ref",
			Usage: "Reference image file; repeat for several",
		},
		&urfavecli.StringFlag{
			Name:  "quality",
			Usage: "auto|low|medium|high|xhigh|max, images-API models only; max costs about 16x auto",
		},
		&urfavecli.StringFlag{
			Name:    "images-api-models",
			Value:   strings.Join(defaultImagesAPIModels, ","),
			Sources: urfavecli.EnvVars("OPENROUTER_IMAGES_API_MODELS"),
			Usage:   "Comma-separated model ids routed to /api/v1/images",
		},
	}
}

type imageOptions struct {
	model           string
	modelExplicit   bool // -m or OPENROUTER_IMAGE_MODEL; no automatic fallback then
	pixelSize       string
	outputDirectory string
	prompt          string
	maximumTokens   int
	aspectRatio     string
	imageSize       string
	quality         string
	referencePaths  []string
	imagesAPIModels []string
	warnings        io.Writer // the fallback notice; nil discards it
}

// generatedImages is what either generation path returns.
type generatedImages struct {
	text   string
	images []llm.ImagePart
	usage  *llm.Usage
}

func runImage(ctx context.Context, options imageOptions, out io.Writer, client llm.Client) error {
	if options.prompt == "" {
		return errors.New("missing prompt: aikido image [flags] <prompt...>")
	}
	options = withImageDefaults(options)
	references, err := loadReferenceImages(options.referencePaths)
	if err != nil {
		return err
	}
	options, imagesAPI, err := routeImageRequest(options)
	if err != nil {
		return err
	}
	var generated generatedImages
	if imagesAPI {
		generated, err = generateViaImagesAPI(ctx, options, references, client)
	} else {
		generated, err = generateViaChat(ctx, options, references, client)
	}
	if err != nil {
		return err
	}
	if len(generated.images) == 0 {
		return errors.New("provider returned no images")
	}
	if err := writeImages(out, options.outputDirectory, generated.images); err != nil {
		return err
	}
	printImageSummary(out, generated)
	return nil
}

func withImageDefaults(options imageOptions) imageOptions {
	if options.model == "" {
		options.model = defaultImageModel
	}
	if options.outputDirectory == "" {
		options.outputDirectory = "out"
	}
	return options
}

// routeImageRequest decides the endpoint. An images-API model with an explicit
// aspect ratio gets the exact pixel frame; a frame the endpoint cannot render
// falls back to fallbackImageModel on the chat path, unless the user chose the
// model, which is an error then.
func routeImageRequest(options imageOptions) (imageOptions, bool, error) {
	imagesAPI := usesImagesAPI(options.model, options.imagesAPIModels)
	if !imagesAPI || options.aspectRatio == "" || options.aspectRatio == "auto" {
		return options, imagesAPI, nil
	}
	size, err := imagesAPISize(options.aspectRatio, options.imageSize)
	switch {
	case err == nil:
		options.pixelSize = size
		return options, true, nil
	case !errors.Is(err, errFrameUnsupported):
		return options, false, err
	case options.modelExplicit:
		return options, false, fmt.Errorf("%s cannot render %s (%w); use -m %s", options.model, frameDescription(options), err, fallbackImageModel)
	default:
		if options.warnings != nil {
			fmt.Fprintf(options.warnings, "aikido: %s cannot render %s, using %s\n", options.model, frameDescription(options), fallbackImageModel)
		}
		options.model, options.quality = fallbackImageModel, ""
		return options, false, nil
	}
}

func frameDescription(options imageOptions) string {
	frame := "--aspect " + options.aspectRatio
	if options.imageSize != "" {
		frame += " --size " + options.imageSize
	}
	return frame
}

// writeImages saves every inline image into directory and reports each file;
// URL-only images are printed instead.
func writeImages(out io.Writer, directory string, images []llm.ImagePart) error {
	//nolint:gosec // G301: the output directory the user asked for; world-readable like any CLI output, umask applies
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", directory, err)
	}
	stamp := time.Now().UTC().Format("20060102-150405")
	for index, image := range images {
		switch {
		case len(image.Data) > 0:
			path := filepath.Join(directory, fmt.Sprintf("image-%s-%02d%s", stamp, index, imageExtension(image.ContentType)))
			//nolint:gosec // G306: a generated image the user asked for; world-readable like any CLI output, umask applies
			if err := os.WriteFile(path, image.Data, 0o644); err != nil {
				return fmt.Errorf("write %s: %w", path, err)
			}
			fmt.Fprintf(out, "wrote %s (%d bytes, %s)\n", path, len(image.Data), image.ContentType)
		case image.URL != "":
			fmt.Fprintf(out, "image %d available at: %s\n", index, image.URL)
		}
	}
	return nil
}

func printImageSummary(out io.Writer, generated generatedImages) {
	if text := strings.TrimSpace(generated.text); text != "" {
		fmt.Fprintf(out, "\n%s\n", text)
	}
	if generated.usage != nil {
		fmt.Fprintf(out, "\n%s\n", formatUsage(generated.usage))
	}
}

// generateViaImagesAPI renders through OpenRouter's /api/v1/images. The
// endpoint has no message list, so --max-tokens has nothing to apply to.
func generateViaImagesAPI(ctx context.Context, options imageOptions, references []llm.ImagePart, client llm.Client) (generatedImages, error) {
	generator, ok := client.(llm.ImageGenerator)
	if !ok {
		return generatedImages{}, errors.New("client does not support the images endpoint")
	}
	request := llm.ImageRequest{
		Model:       options.model,
		Prompt:      options.prompt,
		AspectRatio: options.aspectRatio,
		ImageSize:   options.imageSize,
		Quality:     options.quality,
		References:  references,
	}
	// The endpoint refuses size mixed with aspect_ratio/resolution.
	if options.pixelSize != "" {
		request.Size, request.AspectRatio, request.ImageSize = options.pixelSize, "", ""
	}
	response, err := generator.GenerateImage(ctx, request)
	if err != nil {
		return generatedImages{}, err
	}
	return generatedImages{images: response.Images, usage: response.Usage}, nil
}

// generateViaChat renders through chat completions with image output, the path
// every dual-endpoint model (Gemini, gpt-5.4-image-2) takes.
func generateViaChat(ctx context.Context, options imageOptions, references []llm.ImagePart, client llm.Client) (generatedImages, error) {
	if options.quality != "" {
		return generatedImages{}, errors.New("--quality is only supported by images-API models (see --images-api-models)")
	}
	request := llm.Request{
		Model: options.model,
		Messages: []llm.Message{
			{Role: llm.RoleUser, Content: options.prompt, Images: references},
		},
		MaximumTokens: options.maximumTokens,
		Modalities:    []string{"image", "text"},
	}
	if options.aspectRatio != "" || options.imageSize != "" {
		request.ImageConfiguration = &llm.ImageConfiguration{
			AspectRatio: options.aspectRatio,
			ImageSize:   options.imageSize,
		}
	}
	response, err := client.Complete(ctx, request)
	if err != nil {
		return generatedImages{}, hintImagesAPI(err)
	}
	return generatedImages{text: response.Text, images: response.Images, usage: response.Usage}, nil
}

// imagesAPISize turns an aspect ratio and resolution tier into the exact
// WIDTHxHEIGHT the images endpoint takes. The aspect_ratio field alone rejects
// 4:5 and renders 9:16 and 3:4 both as 1024x1536; explicit pixels come back
// exact. The tier sets the short edge (1K = 1024), then the frame shrinks to
// the endpoint's edge and pixel caps, both sides on multiples of 16.
func imagesAPISize(aspect, tier string) (string, error) {
	width, height, err := parseAspectRatio(aspect)
	if err != nil {
		return "", err
	}
	shortEdge, err := tierShortEdge(tier)
	if err != nil {
		return "", err
	}
	shortSide, longSide := float64(min(width, height)), float64(max(width, height))
	if longSide/shortSide > imagesMaximumRatio {
		return "", errFrameUnsupported
	}
	short, long := fitImagesFrame(shortEdge, shortEdge*longSide/shortSide)
	if width < height {
		return fmt.Sprintf("%dx%d", short, long), nil
	}
	return fmt.Sprintf("%dx%d", long, short), nil
}

func parseAspectRatio(aspect string) (width, height int, err error) {
	widthText, heightText, found := strings.Cut(strings.TrimSpace(aspect), ":")
	width, widthError := strconv.Atoi(widthText)
	height, heightError := strconv.Atoi(heightText)
	if !found || widthError != nil || heightError != nil || width <= 0 || height <= 0 {
		return 0, 0, fmt.Errorf("invalid --aspect %q: want W:H such as 4:5", aspect)
	}
	return width, height, nil
}

// tierShortEdge maps a --size tier to the short edge in pixels; the images
// endpoint has no 0.5K tier.
func tierShortEdge(tier string) (float64, error) {
	switch strings.ToUpper(strings.TrimSpace(tier)) {
	case "", "1K":
		return 1024, nil
	case "2K":
		return 2048, nil
	case "4K":
		return 4096, nil
	default:
		return 0, errFrameUnsupported
	}
}

// fitImagesFrame shrinks a short x long frame to the endpoint's edge and pixel
// caps and rounds both sides to multiples of 16 that still fit.
func fitImagesFrame(short, long float64) (shortPixels, longPixels int) {
	if long > imagesMaximumEdge {
		short, long = short*imagesMaximumEdge/long, imagesMaximumEdge
	}
	if scale := math.Sqrt(imagesMaximumPixels / (short * long)); scale < 1 {
		short, long = short*scale, long*scale
	}
	shortPixels, longPixels = roundToSideStep(short), roundToSideStep(long)
	for longPixels > imagesMaximumEdge || shortPixels*longPixels > imagesMaximumPixels {
		if longPixels > imagesMaximumEdge {
			longPixels -= imagesSideStep
		} else {
			shortPixels -= imagesSideStep
		}
	}
	return shortPixels, longPixels
}

func roundToSideStep(pixels float64) int {
	return int(math.Round(pixels/imagesSideStep)) * imagesSideStep
}

// hintImagesAPI annotates the provider error a model raises when it is served
// only by the images endpoint. The id is missing from a plain /models listing,
// so the raw 404 reads like a typo. OpenRouter states the reason only in the
// message of its error envelope (*openrouter.APIError); other errors pass
// through unchanged.
func hintImagesAPI(err error) error {
	var apiError *openrouter.APIError
	if !errors.As(err, &apiError) {
		return err
	}
	message := strings.ToLower(apiError.Message)
	if !strings.Contains(message, "/api/v1/images") && !strings.Contains(message, "output modalities") {
		return err
	}
	return fmt.Errorf("%w (model may need the images endpoint: add it to OPENROUTER_IMAGES_API_MODELS)", err)
}

// parseImagesAPIModels splits the comma-separated flag value. An empty result
// falls back to the compiled-in list; a non-matching value such as "none"
// disables images-endpoint routing entirely.
func parseImagesAPIModels(value string) []string {
	var models []string
	for part := range strings.SplitSeq(value, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			models = append(models, trimmed)
		}
	}
	if len(models) == 0 {
		return defaultImagesAPIModels
	}
	return models
}

// usesImagesAPI reports whether model is served by /api/v1/images.
func usesImagesAPI(model string, imagesAPIModels []string) bool {
	want := normalizeModelID(model)
	if want == "" {
		return false
	}
	for _, entry := range imagesAPIModels {
		if normalizeModelID(entry) == want {
			return true
		}
	}
	return false
}

// normalizeModelID trims whitespace and drops OpenRouter's ":variant" suffix
// (":free", ":nitro", ...), which selects routing rather than a different model.
func normalizeModelID(id string) string {
	id = strings.TrimSpace(id)
	base, _, _ := strings.Cut(id, ":")
	return base
}

// loadReferenceImages reads --ref files into data: URIs. Both generation paths
// take the same []llm.ImagePart; only URL is read downstream.
func loadReferenceImages(paths []string) ([]llm.ImagePart, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	references := make([]llm.ImagePart, 0, len(paths))
	remaining := int64(maximumReferenceBytes)
	for _, path := range paths {
		data, err := readReferenceFile(path, remaining)
		if err != nil {
			return nil, err
		}
		remaining -= int64(len(data))
		contentType := referenceContentType(path, data)
		if !strings.HasPrefix(contentType, "image/") {
			return nil, fmt.Errorf("reference %s is not an image (detected %s)", path, contentType)
		}
		references = append(references, llm.ImagePart{
			URL:         "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(data),
			ContentType: contentType,
		})
	}
	return references, nil
}

// referenceContentType trusts the extension first and sniffs the bytes only
// when the extension is unknown. Parameters such as "; charset" are dropped.
func referenceContentType(path string, data []byte) string {
	contentType, _, _ := strings.Cut(mime.TypeByExtension(filepath.Ext(path)), ";")
	contentType = strings.TrimSpace(contentType)
	if contentType == "" {
		return http.DetectContentType(data)
	}
	return contentType
}

// readReferenceFile reads at most limit bytes from a regular file. The stat
// gates the read rather than trailing it: os.ReadFile on /dev/zero never
// returns, and a mistyped --ref movie.mp4 would otherwise pull gigabytes into
// memory before the non-image check rejects it. The LimitReader then catches a
// file that grew between the stat and the read.
func readReferenceFile(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path) //nolint:gosec // G304: reading the file the user passed as --ref is the point
	if err != nil {
		return nil, fmt.Errorf("read reference image %s: %w", path, err)
	}
	defer file.Close() // read-only; a close error carries no information
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat reference image %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("reference %s is not a regular file", path)
	}
	if info.Size() > limit {
		return nil, fmt.Errorf("reference images exceed %d bytes in total", maximumReferenceBytes)
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read reference image %s: %w", path, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("reference images exceed %d bytes in total", maximumReferenceBytes)
	}
	return data, nil
}

func imageExtension(contentType string) string {
	switch strings.ToLower(contentType) {
	case "image/png":
		return ".png"
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	default:
		return ".bin"
	}
}
