package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/govengers/aikido/llm"

	urfavecli "github.com/urfave/cli/v3"
)

// defaultVideoModel is the cheapest model with first and last frame, audio
// and both 720p and 1080p: a 4 s 720p clip without audio is about 0.12 USD.
const defaultVideoModel = "google/veo-3.1-lite"

func videoCommand(factory ClientFactory) *urfavecli.Command {
	return &urfavecli.Command{
		Name:      "video",
		Usage:     "Generate a video clip and write it to disk (takes minutes)",
		ArgsUsage: "<prompt...>",
		Description: "Submits a job to OpenRouter's /api/v1/videos, polls it and downloads the clip.\n" +
			"Each model accepts a closed list of durations, resolutions and ratios and\n" +
			"rejects anything else: see `aikido models --video [filter]`. Unset flags are\n" +
			"not sent, leaving the model default.",
		DisableSliceFlagSeparator: true,
		Flags: []urfavecli.Flag{
			&urfavecli.StringFlag{
				Name:    "model",
				Aliases: []string{"m"},
				Value:   defaultVideoModel,
				Sources: urfavecli.EnvVars("OPENROUTER_VIDEO_MODEL"),
				Usage:   "OpenRouter video model id",
			},
			&urfavecli.StringFlag{Name: "out", Aliases: []string{"o"}, Value: "out", Usage: "Output directory"},
			&urfavecli.StringFlag{Name: "aspect", Aliases: []string{"a"}, Usage: "Aspect ratio W:H, e.g. 16:9, 9:16, 1:1"},
			&urfavecli.StringFlag{Name: "resolution", Aliases: []string{"r"}, Usage: "Resolution tier, e.g. 480p, 720p, 1080p, 4K"},
			&urfavecli.StringFlag{Name: "size", Usage: "Exact WIDTHxHEIGHT, instead of --aspect and --resolution"},
			&urfavecli.IntFlag{Name: "duration", Aliases: []string{"d"}, Usage: "Length in seconds"},
			&urfavecli.BoolFlag{Name: "audio", Usage: "Generate audio (--audio=false for a silent, cheaper clip); unset leaves the model default"},
			&urfavecli.IntFlag{Name: "seed", Usage: "Seed, where the provider honours one"},
			&urfavecli.StringFlag{Name: "first-frame", Usage: "Image file the clip starts from"},
			&urfavecli.StringFlag{Name: "last-frame", Usage: "Image file the clip ends on (models listing last_frame)"},
			&urfavecli.StringSliceFlag{Name: "ref", Usage: "Reference image file for style or identity; repeat for several; ignored when a frame is set"},
		},
		Action: func(ctx context.Context, command *urfavecli.Command) error {
			client, err := factory()
			if err != nil {
				return err
			}
			return runVideo(ctx, videoOptionsFromFlags(command), resolveWriter(command), client)
		},
	}
}

func videoOptionsFromFlags(command *urfavecli.Command) videoOptions {
	options := videoOptions{
		request: llm.VideoRequest{
			Model:           command.String("model"),
			Prompt:          promptFromArguments(command),
			AspectRatio:     command.String("aspect"),
			Resolution:      command.String("resolution"),
			Size:            command.String("size"),
			DurationSeconds: command.Int("duration"),
		},
		outputDirectory: command.String("out"),
		firstFramePath:  command.String("first-frame"),
		lastFramePath:   command.String("last-frame"),
		referencePaths:  command.StringSlice("ref"),
		warnings:        resolveErrorWriter(command),
	}
	if command.IsSet("audio") {
		options.request.GenerateAudio = new(command.Bool("audio"))
	}
	if command.IsSet("seed") {
		options.request.Seed = new(command.Int("seed"))
	}
	return options
}

type videoOptions struct {
	request         llm.VideoRequest
	outputDirectory string
	firstFramePath  string
	lastFramePath   string
	referencePaths  []string
	warnings        io.Writer // the progress notice; nil discards it
}

func runVideo(ctx context.Context, options videoOptions, out io.Writer, client llm.Client) error {
	if options.request.Prompt == "" {
		return errors.New("missing prompt: aikido video [flags] <prompt...>")
	}
	generator, ok := client.(llm.VideoGenerator)
	if !ok {
		return errors.New("client does not support video generation")
	}
	request, err := withVideoImages(options)
	if err != nil {
		return err
	}
	if options.outputDirectory == "" {
		options.outputDirectory = "out"
	}
	//nolint:gosec // G301: the output directory the user asked for; world-readable like any CLI output, umask applies
	if err := os.MkdirAll(options.outputDirectory, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", options.outputDirectory, err)
	}
	if options.warnings != nil {
		fmt.Fprintf(options.warnings, "aikido: rendering with %s, this takes one to five minutes\n", request.Model)
	}
	response, err := generator.GenerateVideo(ctx, request)
	if err != nil {
		if response.JobID != "" {
			return fmt.Errorf("%w (job %s)", err, response.JobID)
		}
		return err
	}
	return writeVideo(out, options.outputDirectory, response)
}

// withVideoImages returns the request with the frame and reference files
// loaded.
func withVideoImages(options videoOptions) (llm.VideoRequest, error) {
	request := options.request
	var err error
	if options.firstFramePath != "" {
		if request.FirstFrame, err = loadFrame(options.firstFramePath); err != nil {
			return llm.VideoRequest{}, err
		}
	}
	if options.lastFramePath != "" {
		if request.LastFrame, err = loadFrame(options.lastFramePath); err != nil {
			return llm.VideoRequest{}, err
		}
	}
	if request.References, err = loadReferenceImages(options.referencePaths); err != nil {
		return llm.VideoRequest{}, err
	}
	return request, nil
}

func writeVideo(out io.Writer, directory string, response llm.VideoResponse) error {
	extension := ".mp4"
	if response.ContentType != "video/mp4" {
		extension = ".bin"
	}
	path := filepath.Join(directory, "video-"+time.Now().UTC().Format("20060102-150405")+extension)
	//nolint:gosec // G306: a generated clip the user asked for; world-readable like any CLI output, umask applies
	if err := os.WriteFile(path, response.Video, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	fmt.Fprintf(out, "wrote %s (%d bytes, %s)\n", path, len(response.Video), response.ContentType)
	if response.Usage != nil {
		fmt.Fprintf(out, "\n[usage] cost=$%.4f duration=%s job=%s\n", response.Usage.CostUSD, response.Usage.Duration.Round(time.Second), response.JobID)
	}
	return nil
}

// loadFrame reads one --first-frame/--last-frame file.
func loadFrame(path string) (*llm.ImagePart, error) {
	parts, err := loadReferenceImages([]string{path})
	if err != nil {
		return nil, err
	}
	return &parts[0], nil
}
