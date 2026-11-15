package budget

import (
	"context"
	"errors"

	"github.com/govengers/aikido/llm"
)

// Wrap returns a client that makes every call of client only while no limit of the call is spent
// and charges the call's billed cost afterwards. The cost of a stream is charged before its channel
// closes, so the next call of the same caller sees it.
//
// The returned client implements exactly the optional capabilities llm.Capability finds on client
// (llm.ImageGenerator, llm.SpeechGenerator, llm.VideoGenerator, llm.Decider), each checked and
// charged the same way; speech is checked but never charged, since providers report no cost for
// it. Its Unwrap returns client.
//
// With llm.NewObservedClient, wrap the observed client: Wrap(observed). Refused calls then never
// reach the provider or its metrics, and the capabilities behind the observed client stay
// reachable.
func (b *Budget) Wrap(client llm.Client) (llm.Client, error) {
	if client == nil {
		return nil, errors.New("aikido/llm/budget: Wrap needs a client")
	}
	observed, err := llm.NewObservedClient(&llm.ObservedClientOptions{Client: client, Name: "budget", Observers: []llm.Observer{charger{budget: b}}})
	if err != nil {
		return nil, err
	}
	return withCapabilities(&budgeted{budget: b, client: client, observed: observed}), nil
}

// withCapabilities returns base extended by the capabilities of its client: Go cannot add methods
// to a value at run time, so every combination is its own type.
func withCapabilities(base *budgeted) llm.Client {
	image, hasImage := llm.Capability[llm.ImageGenerator](base.client)
	speech, hasSpeech := llm.Capability[llm.SpeechGenerator](base.client)
	video, hasVideo := llm.Capability[llm.VideoGenerator](base.client)
	decider, hasDecider := llm.Capability[llm.Decider](base.client)
	set := capabilitySet{
		images: imageGenerator{base, image}, voices: speechGenerator{base, speech},
		videos: videoGenerator{base, video}, decisions: decisionMaker{base, decider},
		others: [3]bool{hasSpeech, hasVideo, hasDecider},
	}
	if hasImage {
		return withImage(base, set)
	}
	return withoutImage(base, set)
}

// capabilitySet holds the wrapped capabilities and which of speech, video and decisions the client
// has.
type capabilitySet struct {
	images    imageGenerator
	voices    speechGenerator
	videos    videoGenerator
	decisions decisionMaker
	others    [3]bool
}

func withImage(base *budgeted, set capabilitySet) llm.Client {
	switch set.others {
	case [3]bool{false, false, true}:
		return struct {
			*budgeted
			imageGenerator
			decisionMaker
		}{base, set.images, set.decisions}
	case [3]bool{false, true, false}:
		return struct {
			*budgeted
			imageGenerator
			videoGenerator
		}{base, set.images, set.videos}
	case [3]bool{false, true, true}:
		return struct {
			*budgeted
			imageGenerator
			videoGenerator
			decisionMaker
		}{base, set.images, set.videos, set.decisions}
	case [3]bool{true, false, false}:
		return struct {
			*budgeted
			imageGenerator
			speechGenerator
		}{base, set.images, set.voices}
	case [3]bool{true, false, true}:
		return struct {
			*budgeted
			imageGenerator
			speechGenerator
			decisionMaker
		}{base, set.images, set.voices, set.decisions}
	case [3]bool{true, true, false}:
		return struct {
			*budgeted
			imageGenerator
			speechGenerator
			videoGenerator
		}{base, set.images, set.voices, set.videos}
	case [3]bool{true, true, true}:
		return struct {
			*budgeted
			imageGenerator
			speechGenerator
			videoGenerator
			decisionMaker
		}{base, set.images, set.voices, set.videos, set.decisions}
	default:
		return struct {
			*budgeted
			imageGenerator
		}{base, set.images}
	}
}

func withoutImage(base *budgeted, set capabilitySet) llm.Client {
	switch set.others {
	case [3]bool{false, false, true}:
		return struct {
			*budgeted
			decisionMaker
		}{base, set.decisions}
	case [3]bool{false, true, false}:
		return struct {
			*budgeted
			videoGenerator
		}{base, set.videos}
	case [3]bool{false, true, true}:
		return struct {
			*budgeted
			videoGenerator
			decisionMaker
		}{base, set.videos, set.decisions}
	case [3]bool{true, false, false}:
		return struct {
			*budgeted
			speechGenerator
		}{base, set.voices}
	case [3]bool{true, false, true}:
		return struct {
			*budgeted
			speechGenerator
			decisionMaker
		}{base, set.voices, set.decisions}
	case [3]bool{true, true, false}:
		return struct {
			*budgeted
			speechGenerator
			videoGenerator
		}{base, set.voices, set.videos}
	case [3]bool{true, true, true}:
		return struct {
			*budgeted
			speechGenerator
			videoGenerator
			decisionMaker
		}{base, set.voices, set.videos, set.decisions}
	default:
		return base
	}
}

// budgeted is the llm.Client part of a wrapped client. Stream and Complete go through an
// ObservedClient whose only observer charges the call, so the stream handling (abandoned streams,
// panics) is the one ObservedClient already has.
type budgeted struct {
	budget   *Budget
	client   llm.Client
	observed *llm.ObservedClient
}

func (c *budgeted) Unwrap() llm.Client { return c.client }

func (c *budgeted) Stream(ctx context.Context, request llm.Request) (<-chan llm.Event, error) {
	accounts, err := c.budget.admit(ctx)
	if err != nil {
		return nil, err
	}
	return c.observed.Stream(context.WithValue(ctx, accountsKey{}, accounts), request)
}

func (c *budgeted) Complete(ctx context.Context, request llm.Request) (llm.Response, error) {
	accounts, err := c.budget.admit(ctx)
	if err != nil {
		return llm.Response{}, err
	}
	return c.observed.Complete(context.WithValue(ctx, accountsKey{}, accounts), request)
}

// accountsKey carries the accounts of a call from budgeted to charger.
type accountsKey struct{}

// charger charges a finished Stream or Complete call to the accounts in its context.
type charger struct{ budget *Budget }

func (charger) CallStarted(ctx context.Context, _ *llm.Call) context.Context { return ctx }

func (c charger) CallFinished(ctx context.Context, call *llm.Call, outcome *llm.CallOutcome) {
	accounts, _ := ctx.Value(accountsKey{}).([]Account) // set by budgeted for every call
	c.budget.charge(ctx, accounts, c.budget.costOf(ctx, call.Model, outcome.Usage, outcome.Err))
}

// costOf is the cost to charge for a call: the billed cost the provider reported, else
// CancelledCostUSD when the call ended because its context did.
func (b *Budget) costOf(ctx context.Context, model string, usage *llm.Usage, err error) float64 {
	switch {
	case usage != nil:
		return usage.CostUSD
	case b.cancelledCost != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)):
		return b.cancelledCost(ctx, model)
	default:
		return 0
	}
}

type imageGenerator struct {
	client    *budgeted
	generator llm.ImageGenerator
}

func (g imageGenerator) GenerateImage(ctx context.Context, request llm.ImageRequest) (llm.ImageResponse, error) {
	accounts, err := g.client.budget.admit(ctx)
	if err != nil {
		return llm.ImageResponse{}, err
	}
	response, err := g.generator.GenerateImage(ctx, request)
	g.client.budget.charge(ctx, accounts, g.client.budget.costOf(ctx, request.Model, response.Usage, err))
	return response, err
}

type speechGenerator struct {
	client    *budgeted
	generator llm.SpeechGenerator
}

func (g speechGenerator) Speak(ctx context.Context, request llm.SpeechRequest) (llm.SpeechResponse, error) {
	if _, err := g.client.budget.admit(ctx); err != nil {
		return llm.SpeechResponse{}, err
	}
	return g.generator.Speak(ctx, request)
}

type videoGenerator struct {
	client    *budgeted
	generator llm.VideoGenerator
}

func (g videoGenerator) GenerateVideo(ctx context.Context, request llm.VideoRequest) (llm.VideoResponse, error) {
	accounts, err := g.client.budget.admit(ctx)
	if err != nil {
		return llm.VideoResponse{}, err
	}
	response, err := g.generator.GenerateVideo(ctx, request)
	g.client.budget.charge(ctx, accounts, g.client.budget.costOf(ctx, request.Model, response.Usage, err))
	return response, err
}

type decisionMaker struct {
	client  *budgeted
	decider llm.Decider
}

func (d decisionMaker) Decide(ctx context.Context, request llm.DecisionRequest) (llm.DecisionResponse, error) {
	accounts, err := d.client.budget.admit(ctx)
	if err != nil {
		return llm.DecisionResponse{}, err
	}
	response, err := d.decider.Decide(ctx, request)
	d.client.budget.charge(ctx, accounts, d.client.budget.costOf(ctx, request.Model, response.Usage, err))
	return response, err
}
