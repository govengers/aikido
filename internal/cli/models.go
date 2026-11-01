package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/llm/openrouter"

	urfavecli "github.com/urfave/cli/v3"
)

// modelCatalog is the slice of *openrouter.Client the models command needs.
type modelCatalog interface {
	Models(ctx context.Context) ([]openrouter.Model, error)
	ModelEndpoints(ctx context.Context, id string) (openrouter.ModelEndpoints, error)
	VideoModels(ctx context.Context) ([]openrouter.VideoModel, error)
	DecisionModels(ctx context.Context) ([]openrouter.Model, error)
}

func modelsCommand(factory ClientFactory) *urfavecli.Command {
	return &urfavecli.Command{
		Name:      "models",
		Usage:     "List OpenRouter models, or one model's providers with latency and throughput",
		ArgsUsage: "[filter | author/slug]",
		Description: "Without an argument, lists every model (id, context, $/M in and out).\n" +
			"A plain word filters that list by substring. An author/slug id prints the\n" +
			"providers serving that model: tag (the slug for --provider), quantization,\n" +
			"prices, uptime and p50 latency / throughput over the last 30 minutes.\n" +
			"--video lists the video models with the values `aikido video` accepts,\n" +
			"--decisions the models of the Decisions API (`aikido decide`).",
		Flags: []urfavecli.Flag{
			&urfavecli.BoolFlag{Name: "video", Usage: "List video-generation models (durations, resolutions, ratios, frames, audio)"},
			&urfavecli.BoolFlag{Name: "decisions", Usage: "List Decisions API models (Jev, ...)"},
		},
		Action: func(ctx context.Context, command *urfavecli.Command) error {
			client, err := factory()
			if err != nil {
				return err
			}
			catalog, ok := client.(modelCatalog)
			if !ok {
				return errors.New("models: the client has no model catalogue (OpenRouter only)")
			}
			argument := strings.TrimSpace(command.Args().First())
			out := resolveWriter(command)
			if command.Bool("video") {
				return printVideoModels(ctx, out, catalog, argument)
			}
			if command.Bool("decisions") {
				return printModels(ctx, out, catalog.DecisionModels, argument)
			}
			if strings.Contains(argument, "/") {
				return printEndpoints(ctx, out, catalog, argument)
			}
			return printModels(ctx, out, catalog.Models, argument)
		},
	}
}

func printModels(ctx context.Context, out io.Writer, list func(context.Context) ([]openrouter.Model, error), filter string) error {
	models, err := list(ctx)
	if err != nil {
		return err
	}
	table := newTable(out)
	fmt.Fprintln(table, "ID\tCONTEXT\t$/M IN\t$/M OUT")
	for _, model := range models {
		if !matchesFilter(model.ID, filter) {
			continue
		}
		fmt.Fprintf(table, "%s\t%d\t%.2f\t%.2f\n", model.ID, model.ContextLength, model.Pricing.Prompt.PerMillion(), model.Pricing.Completion.PerMillion())
	}
	return table.Flush()
}

func printVideoModels(ctx context.Context, out io.Writer, catalog modelCatalog, filter string) error {
	models, err := catalog.VideoModels(ctx)
	if err != nil {
		return err
	}
	table := newTable(out)
	fmt.Fprintln(table, "ID\tDURATIONS\tRESOLUTIONS\tRATIOS\tFRAMES\tAUDIO")
	for _, model := range models {
		if !matchesFilter(model.ID, filter) {
			continue
		}
		audio := "-"
		if model.GenerateAudio != nil {
			audio = fmt.Sprint(*model.GenerateAudio)
		}
		resolutions := model.SupportedResolutions
		if len(resolutions) == 0 {
			resolutions = model.SupportedSizes
		}
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\n", model.ID, durations(model.SupportedDurations),
			listOrDash(resolutions), listOrDash(model.SupportedAspectRatios), listOrDash(model.SupportedFrameImages), audio)
	}
	return table.Flush()
}

func printEndpoints(ctx context.Context, out io.Writer, catalog modelCatalog, id string) error {
	modelEndpoints, err := catalog.ModelEndpoints(ctx, id)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "%s (%s)\n\n", modelEndpoints.ID, modelEndpoints.Name)
	table := newTable(out)
	fmt.Fprintln(table, "PROVIDER\tTAG\tQUANT\tCONTEXT\t$/M IN\t$/M OUT\tUPTIME 30m\tLATENCY p50\tTHROUGHPUT p50\tSTATUS")
	for _, endpoint := range modelEndpoints.Endpoints {
		uptime, latency, throughput := "-", "-", "-"
		if endpoint.UptimeLast30Minutes != nil {
			uptime = fmt.Sprintf("%.1f%%", *endpoint.UptimeLast30Minutes)
		}
		if endpoint.LatencyLast30Minutes != nil {
			latency = fmt.Sprintf("%.0f ms", endpoint.LatencyLast30Minutes.P50)
		}
		if endpoint.ThroughputLast30Minutes != nil {
			throughput = fmt.Sprintf("%.0f tok/s", endpoint.ThroughputLast30Minutes.P50)
		}
		fmt.Fprintf(table, "%s\t%s\t%s\t%d\t%.2f\t%.2f\t%s\t%s\t%s\t%d\n",
			endpoint.ProviderName, endpoint.Tag, endpoint.Quantization, endpoint.ContextLength,
			endpoint.Pricing.Prompt.PerMillion(), endpoint.Pricing.Completion.PerMillion(),
			uptime, latency, throughput, endpoint.Status)
	}
	return table.Flush()
}

func newTable(out io.Writer) *tabwriter.Writer {
	return tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
}

// matchesFilter reports whether id contains filter, case-insensitively; an
// empty filter matches everything.
func matchesFilter(id, filter string) bool {
	return strings.Contains(strings.ToLower(id), strings.ToLower(filter))
}

// durations prints a contiguous run as "3-15" and anything else as a list.
func durations(seconds []int) string {
	if len(seconds) == 0 {
		return "-"
	}
	seconds = slices.Sorted(slices.Values(seconds))
	first, last := seconds[0], seconds[len(seconds)-1]
	if len(seconds) > 2 && last-first == len(seconds)-1 {
		return fmt.Sprintf("%d-%d", first, last)
	}
	texts := make([]string, len(seconds))
	for index, duration := range seconds {
		texts[index] = fmt.Sprint(duration)
	}
	return strings.Join(texts, ",")
}

func listOrDash(values []string) string {
	if len(values) == 0 {
		return "-"
	}
	return strings.Join(values, ",")
}

// newProviderFlag is the --provider flag of chat, agent and decide. Each
// command gets its own instance: urfave/cli keeps the parsed value in the flag.
func newProviderFlag() *urfavecli.StringSliceFlag {
	return &urfavecli.StringSliceFlag{
		Name:  "provider",
		Usage: "OpenRouter provider slug(s) to route to; one pins it (no fallbacks), several form a fallback chain (see `aikido models author/slug`)",
	}
}

// withProviderFlag applies --provider to ctx for the OpenRouter client.
func withProviderFlag(ctx context.Context, command *urfavecli.Command) context.Context {
	order := command.StringSlice("provider")
	if len(order) == 0 {
		return ctx
	}
	return openrouter.WithProvider(ctx, &openrouter.ProviderPreferences{Order: order, AllowFallbacks: new(len(order) > 1)})
}

// formatUsage renders one model call's usage line.
func formatUsage(usage *llm.Usage) string {
	line := fmt.Sprintf("[usage] prompt=%d completion=%d cost=$%.6f", usage.PromptTokens, usage.CompletionTokens, usage.CostUSD)
	if usage.Provider != "" {
		line += " provider=" + usage.Provider
	}
	if usage.Duration > 0 {
		line += fmt.Sprintf(" latency=%dms duration=%dms", usage.Latency.Milliseconds(), usage.Duration.Milliseconds())
	}
	if usage.TokensPerSecond > 0 {
		line += fmt.Sprintf(" throughput=%.1ftok/s", usage.TokensPerSecond)
	}
	return line
}
