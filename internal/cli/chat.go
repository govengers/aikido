package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/govengers/aikido/llm"

	urfavecli "github.com/urfave/cli/v3"
)

func chatCommand(factory ClientFactory) *urfavecli.Command {
	return &urfavecli.Command{
		Name:      "chat",
		Usage:     "Run a one-shot chat completion",
		ArgsUsage: "<prompt...>",
		Flags: []urfavecli.Flag{
			&urfavecli.StringFlag{Name: "model", Aliases: []string{"m"}, Value: "anthropic/claude-haiku-4.5", Usage: "OpenRouter model id"},
			&urfavecli.StringFlag{Name: "system", Aliases: []string{"s"}, Usage: "System prompt"},
			&urfavecli.IntFlag{Name: "max-tokens", Value: 512, Usage: "Max output tokens"},
			&urfavecli.FloatFlag{Name: "temperature", Aliases: []string{"t"}, Value: -1, Usage: "Sampling temperature; <0 leaves provider default"},
			newProviderFlag(),
		},
		Action: func(ctx context.Context, command *urfavecli.Command) error {
			client, err := factory()
			if err != nil {
				return err
			}
			return runChat(withProviderFlag(ctx, command), chatOptions{
				model:         command.String("model"),
				system:        command.String("system"),
				maximumTokens: command.Int("max-tokens"),
				temperature:   command.Float("temperature"),
				prompt:        promptFromArguments(command),
			}, resolveWriter(command), client)
		},
	}
}

type chatOptions struct {
	model         string
	system        string
	prompt        string
	maximumTokens int
	temperature   float64 // negative leaves the provider default
}

func runChat(ctx context.Context, options chatOptions, out io.Writer, client llm.Client) error {
	if options.prompt == "" {
		return errors.New("missing prompt: aikido chat [flags] <prompt...>")
	}
	response, err := llm.Collect(ctx, client, chatRequest(options))
	if err != nil {
		return err
	}
	fmt.Fprintln(out, response.Text)
	if response.Usage != nil {
		fmt.Fprintf(out, "\n%s\n", formatUsage(response.Usage))
	}
	return nil
}

func chatRequest(options chatOptions) llm.Request {
	messages := make([]llm.Message, 0, 2)
	if options.system != "" {
		messages = append(messages, llm.Message{Role: llm.RoleSystem, Content: options.system})
	}
	messages = append(messages, llm.Message{Role: llm.RoleUser, Content: options.prompt})
	request := llm.Request{
		Model:         options.model,
		Messages:      messages,
		MaximumTokens: options.maximumTokens,
	}
	if options.temperature >= 0 {
		request.Temperature = new(float32(options.temperature))
	}
	return request
}
