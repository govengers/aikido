package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/govengers/aikido/llm"

	urfavecli "github.com/urfave/cli/v3"
)

// defaultDecisionModel tracks TypeSafe's newest Jev release.
const defaultDecisionModel = "~typesafe/jev-latest"

// maximumStdinStateBytes caps a state piped on stdin.
const maximumStdinStateBytes = 4 << 20

func decideCommand(factory ClientFactory) *urfavecli.Command {
	return &urfavecli.Command{
		Name:      "decide",
		Usage:     "Ask a decision model (Jev, ...) typed questions about a state",
		ArgsUsage: "<state...>  (or the state on stdin)",
		Description: "Sends the state and the questions to OpenRouter's Decisions API and prints one\n" +
			"answer per question. Questions are NAME=INSTRUCTIONS, choice and score add\n" +
			"=OPT1,OPT2,... (score levels ordered first to last):\n\n" +
			"  aikido decide --noul 'is_bug=Is this a bug report?' \\\n" +
			"    --choice 'team=Which team owns it?=payments,frontend,account' \\\n" +
			"    --score 'urgency=How urgent?=can wait,this week,blocking revenue' \\\n" +
			"    'Checkout shows a blank page after I click Pay.'\n\n" +
			"Decision models: `aikido models --decisions`.",
		DisableSliceFlagSeparator: true,
		Flags: []urfavecli.Flag{
			&urfavecli.StringFlag{
				Name:    "model",
				Aliases: []string{"m"},
				Value:   defaultDecisionModel,
				Sources: urfavecli.EnvVars("OPENROUTER_DECISION_MODEL"),
				Usage:   "OpenRouter decision model id",
			},
			&urfavecli.StringSliceFlag{Name: "noul", Usage: "Yes/no question NAME=INSTRUCTIONS; repeatable"},
			&urfavecli.StringSliceFlag{Name: "choice", Usage: "Choice question NAME=INSTRUCTIONS=OPT1,OPT2,...; repeatable"},
			&urfavecli.StringSliceFlag{Name: "score", Usage: "Score question NAME=INSTRUCTIONS=LEVEL1,LEVEL2,...; repeatable"},
			&urfavecli.StringSliceFlag{Name: "box", Usage: "Bounding-box question on --image NAME=INSTRUCTIONS; repeatable"},
			&urfavecli.StringSliceFlag{Name: "image", Usage: "Image file added to the state; repeatable"},
			newProviderFlag(),
		},
		Action: func(ctx context.Context, command *urfavecli.Command) error {
			client, err := factory()
			if err != nil {
				return err
			}
			request, err := decisionRequest(command)
			if err != nil {
				return err
			}
			return runDecide(withProviderFlag(ctx, command), request, resolveWriter(command), client)
		},
	}
}

func decisionRequest(command *urfavecli.Command) (llm.DecisionRequest, error) {
	questions, err := parseQuestions(command.StringSlice("noul"), command.StringSlice("choice"), command.StringSlice("score"), command.StringSlice("box"))
	if err != nil {
		return llm.DecisionRequest{}, err
	}
	text, err := stateText(command)
	if err != nil {
		return llm.DecisionRequest{}, err
	}
	images, err := loadReferenceImages(command.StringSlice("image"))
	if err != nil {
		return llm.DecisionRequest{}, err
	}
	state, err := decisionState(text, images)
	if err != nil {
		return llm.DecisionRequest{}, err
	}
	return llm.DecisionRequest{Model: command.String("model"), State: state, Questions: questions}, nil
}

// stateText is the positional arguments, or stdin when they are empty or "-".
func stateText(command *urfavecli.Command) (string, error) {
	text := promptFromArguments(command)
	if text != "" && text != "-" {
		return text, nil
	}
	piped, err := io.ReadAll(io.LimitReader(resolveReader(command), maximumStdinStateBytes))
	if err != nil {
		return "", fmt.Errorf("read state from stdin: %w", err)
	}
	return strings.TrimSpace(string(piped)), nil
}

// decisionState is the text alone, or a list of the text and the images when
// there are images.
func decisionState(text string, images []llm.ImagePart) (any, error) {
	if len(images) == 0 {
		if text == "" {
			return nil, errors.New("missing state: pass it as arguments, on stdin, or as --image")
		}
		return text, nil
	}
	state := []any{}
	if text != "" {
		state = append(state, text)
	}
	for _, image := range images {
		state = append(state, image)
	}
	return state, nil
}

// questionKind is one of the --noul/--choice/--score/--box flags.
type questionKind struct {
	flag     string
	withList bool // the value ends in =A,B,...
	build    func(instructions string, list []string) llm.DecisionQuestion
}

// parseQuestions turns the --noul/--choice/--score/--box values into questions.
func parseQuestions(nouls, choices, scores, boxes []string) (map[string]llm.DecisionQuestion, error) {
	groups := []struct {
		kind   questionKind
		values []string
	}{
		{questionKind{flag: "noul", build: buildNoulQuestion}, nouls},
		{questionKind{flag: "choice", withList: true, build: buildChoiceQuestion}, choices},
		{questionKind{flag: "score", withList: true, build: buildScoreQuestion}, scores},
		{questionKind{flag: "box", build: buildBoxQuestion}, boxes},
	}
	questions := map[string]llm.DecisionQuestion{}
	for _, group := range groups {
		for _, value := range group.values {
			name, question, err := parseQuestion(group.kind, value)
			if err != nil {
				return nil, err
			}
			if _, duplicate := questions[name]; duplicate {
				return nil, fmt.Errorf("question name %q used twice", name)
			}
			questions[name] = question
		}
	}
	if len(questions) == 0 {
		return nil, errors.New("no questions: add --noul, --choice, --score or --box")
	}
	return questions, nil
}

func parseQuestion(kind questionKind, value string) (string, llm.DecisionQuestion, error) {
	name, instructions, found := strings.Cut(value, "=")
	name, instructions = strings.TrimSpace(name), strings.TrimSpace(instructions)
	var list []string
	if found && kind.withList {
		instructions, list, found = cutList(instructions)
	}
	if !found || name == "" || instructions == "" || (kind.withList && len(list) == 0) {
		want := "NAME=INSTRUCTIONS"
		if kind.withList {
			want += "=A,B,..."
		}
		return "", llm.DecisionQuestion{}, fmt.Errorf("--%s %q: want %s", kind.flag, value, want)
	}
	return name, kind.build(instructions, list), nil
}

// cutList splits "INSTRUCTIONS=A,B,..." at the last "=", so the instructions
// may contain "=" themselves.
func cutList(text string) (instructions string, list []string, found bool) {
	separator := strings.LastIndex(text, "=")
	if separator < 0 {
		return text, nil, false
	}
	for item := range strings.SplitSeq(text[separator+1:], ",") {
		if item = strings.TrimSpace(item); item != "" {
			list = append(list, item)
		}
	}
	return strings.TrimSpace(text[:separator]), list, true
}

func buildNoulQuestion(instructions string, _ []string) llm.DecisionQuestion {
	return llm.NoulQuestion(instructions)
}

func buildChoiceQuestion(instructions string, list []string) llm.DecisionQuestion {
	options := make(map[string]string, len(list))
	for _, option := range list {
		options[option] = option
	}
	return llm.ChoiceQuestion(instructions, options)
}

func buildScoreQuestion(instructions string, list []string) llm.DecisionQuestion {
	return llm.ScoreQuestion(instructions, list...)
}

func buildBoxQuestion(instructions string, _ []string) llm.DecisionQuestion {
	return llm.BoxQuestion(instructions)
}

func runDecide(ctx context.Context, request llm.DecisionRequest, out io.Writer, client llm.Client) error {
	decider, ok := client.(llm.Decider)
	if !ok {
		return errors.New("client does not support the Decisions API")
	}
	response, err := decider.Decide(ctx, request)
	if err != nil {
		return err
	}
	table := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for _, name := range slices.Sorted(maps.Keys(response.Answers)) {
		answer := response.Answers[name]
		fmt.Fprintf(table, "%s\t%s\t%s\n", name, answer.Kind, formatAnswer(answer))
	}
	if err := table.Flush(); err != nil {
		return err
	}
	if response.Usage != nil {
		fmt.Fprintf(out, "\n%s model=%s\n", formatUsage(response.Usage), response.Model)
	}
	return nil
}

func formatAnswer(answer llm.DecisionAnswer) string {
	switch answer.Kind {
	case llm.DecisionNoul:
		return fmt.Sprintf("%.2f", answer.Noul)
	case llm.DecisionChoice:
		return fmt.Sprintf("%s (confidence %.2f; %s)", answer.Choice, answer.Confidence, formatProbabilities(answer.Probabilities, nil))
	case llm.DecisionScore:
		return fmt.Sprintf("%.2f (confidence %.2f; %s)", answer.Score, answer.Confidence, formatProbabilities(answer.Probabilities, answer.Legend))
	case llm.DecisionBox:
		if answer.Box == nil {
			return "-"
		}
		box := answer.Box
		return fmt.Sprintf("top=%.2f bottom=%.2f left=%.2f right=%.2f (confidence %.2f)", box.Top, box.Bottom, box.Left, box.Right, answer.Confidence)
	}
	return ""
}

// formatProbabilities lists probabilities high to low, keys renamed through
// legend.
func formatProbabilities(probabilities map[string]float64, legend map[string]string) string {
	keys := slices.Collect(maps.Keys(probabilities))
	slices.SortFunc(keys, func(left, right string) int {
		if probabilities[left] > probabilities[right] {
			return -1
		}
		if probabilities[left] < probabilities[right] {
			return 1
		}
		return strings.Compare(left, right)
	})
	parts := make([]string, len(keys))
	for index, key := range keys {
		label := key
		if renamed, ok := legend[key]; ok {
			label = renamed
		}
		parts[index] = fmt.Sprintf("%s %.2f", label, probabilities[key])
	}
	return strings.Join(parts, ", ")
}
