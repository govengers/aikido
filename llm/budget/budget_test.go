package budget_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/llm/budget"
	"github.com/govengers/aikido/llm/llmtest"
)

var (
	januaryNoon = time.Date(2026, time.January, 15, 12, 0, 0, 0, time.UTC)
	january     = time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	januaryDay  = time.Date(2026, time.January, 15, 0, 0, 0, 0, time.UTC)
)

// harness is a Budget on a MemoryLedger with a user limit of 5 USD a month and a global limit of
// 100 USD a day, at a fixed clock.
type harness struct {
	budget  *budget.Budget
	ledger  *budget.MemoryLedger
	now     time.Time
	limits  []budget.Limit
	failure error // the last ChargeFailed error
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	spend := &harness{
		ledger: budget.NewMemoryLedger(),
		now:    januaryNoon,
		limits: []budget.Limit{
			{Key: "user:1", Period: budget.PeriodMonth, CapMicroUSD: 5_000_000},
			{Key: "global", Period: budget.PeriodDay, CapMicroUSD: 100_000_000},
		},
	}
	var err error
	spend.budget, err = budget.New(&budget.Options{
		Ledger:       spend.ledger,
		Limits:       func(context.Context) ([]budget.Limit, error) { return spend.limits, nil },
		ChargeFailed: func(_ context.Context, err error) { spend.failure = err },
		Now:          func() time.Time { return spend.now },
	})
	if err != nil {
		t.Fatal(err)
	}
	return spend
}

func (h *harness) wrap(t *testing.T, client llm.Client) llm.Client {
	t.Helper()
	wrapped, err := h.budget.Wrap(client)
	if err != nil {
		t.Fatal(err)
	}
	return wrapped
}

func (h *harness) spent(t *testing.T, accounts ...budget.Account) []int64 {
	t.Helper()
	spent, err := h.ledger.Spent(context.Background(), accounts)
	if err != nil {
		t.Fatal(err)
	}
	return spent
}

func (h *harness) charge(t *testing.T, account budget.Account, microUSD int64) {
	t.Helper()
	if err := h.ledger.Charge(context.Background(), []budget.Account{account}, microUSD); err != nil {
		t.Fatal(err)
	}
}

var (
	userAccount   = budget.Account{Key: "user:1", Period: budget.PeriodMonth, PeriodStart: january}
	globalAccount = budget.Account{Key: "global", Period: budget.PeriodDay, PeriodStart: januaryDay}
)

func costTurn(costUSD float64) llmtest.TurnScript {
	return llmtest.TurnScript{Events: []llm.Event{
		{Kind: llm.EventTextDelta, Text: "ok"},
		{Kind: llm.EventUsage, Usage: &llm.Usage{CostUSD: costUSD}},
		{Kind: llm.EventEnd, FinishReason: "stop"},
	}}
}

func TestNewValidatesOptions(t *testing.T) {
	limits := func(context.Context) ([]budget.Limit, error) { return nil, nil }
	for name, options := range map[string]*budget.Options{
		"nil options": nil,
		"no ledger":   {Limits: limits},
		"no limits":   {Ledger: budget.NewMemoryLedger()},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := budget.New(options); err == nil {
				t.Fatal("want an error")
			}
		})
	}
	spend := newHarness(t)
	if _, err := spend.budget.Wrap(nil); err == nil {
		t.Error("Wrap(nil) should fail")
	}
}

func TestStreamChargesTheBilledCostToEveryLimitBeforeTheChannelCloses(t *testing.T) {
	spend := newHarness(t)
	client := spend.wrap(t, llmtest.NewStubClient(costTurn(0.0125)))

	response, err := llm.Collect(context.Background(), client, llm.Request{Model: "m"})
	if err != nil || response.Text != "ok" {
		t.Fatalf("Collect = %+v, %v", response, err)
	}
	if spent := spend.spent(t, userAccount, globalAccount); !slices.Equal(spent, []int64{12_500, 12_500}) {
		t.Errorf("spent = %v; want 12500 micro-USD on both accounts", spent)
	}
}

func TestCompleteCharges(t *testing.T) {
	spend := newHarness(t)
	client := spend.wrap(t, llmtest.NewStubClient(costTurn(0.5)))
	if _, err := client.Complete(context.Background(), llm.Request{Model: "m"}); err != nil {
		t.Fatal(err)
	}
	if spent := spend.spent(t, userAccount); spent[0] != 500_000 {
		t.Errorf("spent = %d; want 500000", spent[0])
	}
}

func TestASpentLimitRefusesTheCallWithoutCallingTheProvider(t *testing.T) {
	spend := newHarness(t)
	spend.charge(t, userAccount, 5_000_000)
	stub := llmtest.NewStubClient(costTurn(0.1), costTurn(0.1))
	client := spend.wrap(t, stub)

	if _, err := client.Stream(context.Background(), llm.Request{}); !errors.Is(err, budget.ErrBudgetExceeded) {
		t.Errorf("Stream = %v; want ErrBudgetExceeded", err)
	}
	if _, err := client.Complete(context.Background(), llm.Request{}); !errors.Is(err, budget.ErrBudgetExceeded) {
		t.Errorf("Complete = %v; want ErrBudgetExceeded", err)
	}
	if requests := stub.Requests(); len(requests) != 0 {
		t.Errorf("the provider got %d requests; want none", len(requests))
	}
}

func TestANewPeriodStartsFromZero(t *testing.T) {
	spend := newHarness(t)
	spend.charge(t, userAccount, 5_000_000)
	spend.now = time.Date(2026, time.February, 1, 0, 0, 0, 0, time.UTC)
	client := spend.wrap(t, llmtest.NewStubClient(costTurn(0.1)))
	if _, err := client.Complete(context.Background(), llm.Request{}); err != nil {
		t.Fatalf("Complete in the next month = %v; want it admitted", err)
	}
}

// On the first of a month the day and the month of a key start on the same instant; they are still
// two accounts, so a call charged only to the month does not use up the day.
func TestTheDayAndTheMonthOfOneKeyAreSeparateAccounts(t *testing.T) {
	spend := newHarness(t)
	spend.now = january.Add(time.Hour)
	monthly := budget.Limit{Key: "user:1", Period: budget.PeriodMonth, CapMicroUSD: 5_000_000}
	daily := budget.Limit{Key: "user:1", Period: budget.PeriodDay, CapMicroUSD: 1_000_000}
	spend.limits = []budget.Limit{monthly}
	client := spend.wrap(t, llmtest.NewStubClient(costTurn(2), costTurn(0.1)))
	if _, err := client.Complete(context.Background(), llm.Request{}); err != nil {
		t.Fatal(err)
	}
	spend.limits = []budget.Limit{daily}
	if _, err := client.Complete(context.Background(), llm.Request{}); err != nil {
		t.Fatalf("a call under the daily limit = %v; want it admitted", err)
	}
	day := budget.Account{Key: "user:1", Period: budget.PeriodDay, PeriodStart: january}
	if spent := spend.spent(t, userAccount, day); !slices.Equal(spent, []int64{2_000_000, 100_000}) {
		t.Errorf("spent = %v; want the month 2000000 and the day 100000", spent)
	}
}

func TestLimitsDecideWhatIsCheckedAndCharged(t *testing.T) {
	t.Run("no limits", func(t *testing.T) {
		spend := newHarness(t)
		spend.limits = nil
		client := spend.wrap(t, llmtest.NewStubClient(costTurn(1)))
		if _, err := client.Complete(context.Background(), llm.Request{}); err != nil {
			t.Fatal(err)
		}
		if spent := spend.spent(t, userAccount, globalAccount); !slices.Equal(spent, []int64{0, 0}) {
			t.Errorf("spent = %v; want nothing charged", spent)
		}
	})
	t.Run("one account twice", func(t *testing.T) {
		spend := newHarness(t)
		spend.limits = append(spend.limits, budget.Limit{Key: "user:1", Period: budget.PeriodMonth, CapMicroUSD: 1_000_000})
		client := spend.wrap(t, llmtest.NewStubClient(costTurn(0.25)))
		if _, err := client.Complete(context.Background(), llm.Request{}); err != nil {
			t.Fatal(err)
		}
		if spent := spend.spent(t, userAccount); spent[0] != 250_000 {
			t.Errorf("spent = %d; want the account charged once", spent[0])
		}
	})
	for name, limit := range map[string]budget.Limit{
		"empty key":      {Period: budget.PeriodDay, CapMicroUSD: 1},
		"negative cap":   {Key: "k", Period: budget.PeriodDay, CapMicroUSD: -1},
		"unknown period": {Key: "k", Period: "fortnight", CapMicroUSD: 1},
	} {
		t.Run(name, func(t *testing.T) {
			spend := newHarness(t)
			spend.limits = []budget.Limit{limit}
			client := spend.wrap(t, llmtest.NewStubClient(costTurn(0.25)))
			if _, err := client.Complete(context.Background(), llm.Request{}); err == nil {
				t.Error("an invalid limit admitted the call")
			}
		})
	}
	t.Run("limits error", func(t *testing.T) {
		sentinel := errors.New("no identity")
		limited, err := budget.New(&budget.Options{
			Ledger: budget.NewMemoryLedger(),
			Limits: func(context.Context) ([]budget.Limit, error) { return nil, sentinel },
		})
		if err != nil {
			t.Fatal(err)
		}
		client, err := limited.Wrap(llmtest.NewStubClient(costTurn(0.25)))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.Complete(context.Background(), llm.Request{}); !errors.Is(err, sentinel) {
			t.Errorf("Complete = %v; want the Limits error", err)
		}
	})
}

func TestStatusReportsTheSpendPerLimit(t *testing.T) {
	spend := newHarness(t)
	spend.charge(t, userAccount, 5_000_000)
	statuses, err := spend.budget.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 2 || statuses[0].SpentMicroUSD != 5_000_000 || !statuses[0].Exceeded() ||
		statuses[1].SpentMicroUSD != 0 || statuses[1].Exceeded() || !statuses[1].PeriodStart.Equal(januaryDay) {
		t.Errorf("Status = %+v", statuses)
	}
}

// failingLedger reads like an empty ledger and fails every charge.
type failingLedger struct{}

func (failingLedger) Spent(_ context.Context, accounts []budget.Account) ([]int64, error) {
	return make([]int64, len(accounts)), nil
}

func (failingLedger) Charge(context.Context, []budget.Account, int64) error {
	return errors.New("database down")
}

func TestAFailedChargeReachesChargeFailedAndKeepsTheResult(t *testing.T) {
	var reported error
	failing, err := budget.New(&budget.Options{
		Ledger: failingLedger{},
		Limits: func(context.Context) ([]budget.Limit, error) {
			return []budget.Limit{{Key: "k", Period: budget.PeriodDay, CapMicroUSD: 1}}, nil
		},
		ChargeFailed: func(_ context.Context, err error) { reported = err },
	})
	if err != nil {
		t.Fatal(err)
	}
	client, err := failing.Wrap(llmtest.NewStubClient(costTurn(0.25)))
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Complete(context.Background(), llm.Request{})
	if err != nil || response.Text != "ok" {
		t.Errorf("Complete = %+v, %v; want the result", response, err)
	}
	if reported == nil {
		t.Error("ChargeFailed was not told")
	}
}

// cancellingClient cancels the caller's context during the call, then reports a cost: the
// provider billed a call whose caller gave up.
type cancellingClient struct {
	llm.Client
	cancel context.CancelFunc
}

func (c cancellingClient) Complete(context.Context, llm.Request) (llm.Response, error) {
	c.cancel()
	return llm.Response{Usage: &llm.Usage{CostUSD: 0.75}}, context.Canceled
}

func TestACallWhoseCallerGaveUpIsStillCharged(t *testing.T) {
	spend := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := spend.wrap(t, cancellingClient{Client: llmtest.NewStubClient(), cancel: cancel})
	if _, err := client.Complete(ctx, llm.Request{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Complete = %v", err)
	}
	if spent := spend.spent(t, userAccount); spent[0] != 750_000 {
		t.Errorf("spent = %d; want 750000", spent[0])
	}
	if spend.failure != nil {
		t.Errorf("ChargeFailed = %v", spend.failure)
	}
}

// A caller that stops a stream before the provider reported its cost is charged CancelledCostUSD,
// and nothing without it.
func TestAStoppedStreamIsChargedTheCancelledCost(t *testing.T) {
	for name, test := range map[string]struct {
		cancelledCost func(context.Context, string) float64
		want          int64
	}{
		"with CancelledCostUSD": {func(_ context.Context, model string) float64 {
			if model != "m" {
				return 1
			}
			return 0.3
		}, 300_000},
		"without": {nil, 0},
	} {
		t.Run(name, func(t *testing.T) {
			spend := newHarness(t)
			stopped, err := budget.New(&budget.Options{
				Ledger:           spend.ledger,
				Limits:           func(context.Context) ([]budget.Limit, error) { return spend.limits, nil },
				CancelledCostUSD: test.cancelledCost,
				Now:              func() time.Time { return spend.now },
			})
			if err != nil {
				t.Fatal(err)
			}
			block := make(chan struct{})
			defer close(block)
			client, err := stopped.Wrap(llmtest.NewStubClient(llmtest.TurnScript{Block: block, Events: costTurn(5).Events}))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			events, err := client.Stream(ctx, llm.Request{Model: "m"})
			if err != nil {
				t.Fatal(err)
			}
			cancel() // the user pressed stop before the provider's usage arrived
			for range events {
			}
			if spent := spend.spent(t, userAccount); spent[0] != test.want {
				t.Errorf("spent = %d; want %d", spent[0], test.want)
			}
		})
	}
}

// queuedClient streams from a producer that queued the whole stream, up to its usage, and closed
// it before the caller read anything, like OpenRouter's buffered channel at the end of a stream.
type queuedClient struct {
	llm.Client
	costUSD float64
}

func (c queuedClient) Stream(context.Context, llm.Request) (<-chan llm.Event, error) {
	events := make(chan llm.Event, 18)
	for range 16 {
		events <- llm.Event{Kind: llm.EventTextDelta, Text: "a"}
	}
	events <- llm.Event{Kind: llm.EventUsage, Usage: &llm.Usage{CostUSD: c.costUSD}}
	events <- llm.Event{Kind: llm.EventEnd, FinishReason: "stop"}
	close(events)
	return events, nil
}

// A caller that stops a stream whose usage is already queued is charged the billed cost, also
// through an ObservedClient in between (the recommended order).
func TestAStoppedStreamWithItsUsageQueuedIsChargedTheBilledCost(t *testing.T) {
	spend := newHarness(t)
	observed, err := llm.NewObservedClient(&llm.ObservedClientOptions{Client: queuedClient{costUSD: 2}, Name: "stub", Observers: []llm.Observer{nopObserver{}}})
	if err != nil {
		t.Fatal(err)
	}
	client := spend.wrap(t, observed)
	ctx, cancel := context.WithCancel(context.Background())
	events, err := client.Stream(ctx, llm.Request{})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	for range events {
	}
	if spent := spend.spent(t, userAccount); spent[0] != 2_000_000 {
		t.Errorf("spent = %d; want 2000000", spent[0])
	}
}

func TestCapabilitiesAreCheckedAndCharged(t *testing.T) {
	spend := newHarness(t)
	stub := llmtest.NewStubClient()
	stub.ScriptImages(llmtest.ImageScript{Response: llm.ImageResponse{Usage: &llm.Usage{CostUSD: 0.04}}})
	stub.ScriptVideos(llmtest.VideoScript{Response: llm.VideoResponse{Usage: &llm.Usage{CostUSD: 1}}})
	stub.ScriptDecisions(llmtest.DecisionScript{Response: llm.DecisionResponse{Usage: &llm.Usage{CostUSD: 0.001}}})
	stub.ScriptSpeech(llmtest.SpeechScript{Response: llm.SpeechResponse{Audio: []byte("pcm")}})
	client := spend.wrap(t, stub)
	ctx := context.Background()

	if _, err := client.(llm.ImageGenerator).GenerateImage(ctx, llm.ImageRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.(llm.VideoGenerator).GenerateVideo(ctx, llm.VideoRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.(llm.Decider).Decide(ctx, llm.DecisionRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.(llm.SpeechGenerator).Speak(ctx, llm.SpeechRequest{}); err != nil {
		t.Fatal(err)
	}
	if spent := spend.spent(t, userAccount); spent[0] != 1_041_000 {
		t.Errorf("spent = %d; want 1041000 (image, video, decision; speech reports no cost)", spent[0])
	}

	spend.charge(t, userAccount, 5_000_000)
	for name, call := range map[string]func() error{
		"image":  func() error { _, err := client.(llm.ImageGenerator).GenerateImage(ctx, llm.ImageRequest{}); return err },
		"video":  func() error { _, err := client.(llm.VideoGenerator).GenerateVideo(ctx, llm.VideoRequest{}); return err },
		"decide": func() error { _, err := client.(llm.Decider).Decide(ctx, llm.DecisionRequest{}); return err },
		"speech": func() error { _, err := client.(llm.SpeechGenerator).Speak(ctx, llm.SpeechRequest{}); return err },
	} {
		if err := call(); !errors.Is(err, budget.ErrBudgetExceeded) {
			t.Errorf("%s over the cap = %v; want ErrBudgetExceeded", name, err)
		}
	}
}

// Each test client implements llm.Client and the capabilities its embedded interfaces name.
type (
	plainClient struct{ llm.Client }
	imageClient struct {
		llm.Client
		llm.ImageGenerator
	}
	speechClient struct {
		llm.Client
		llm.SpeechGenerator
	}
	videoDecider struct {
		llm.Client
		llm.VideoGenerator
		llm.Decider
	}
	allButSpeech struct {
		llm.Client
		llm.ImageGenerator
		llm.VideoGenerator
		llm.Decider
	}
)

func TestWrapKeepsExactlyTheCapabilitiesOfTheClient(t *testing.T) {
	stub := llmtest.NewStubClient()
	observed, err := llm.NewObservedClient(&llm.ObservedClientOptions{Client: stub, Name: "stub", Observers: []llm.Observer{nopObserver{}}})
	if err != nil {
		t.Fatal(err)
	}
	for name, test := range map[string]struct {
		client llm.Client
		want   [4]bool // image, speech, video, decider
	}{
		"none":                   {plainClient{stub}, [4]bool{}},
		"image":                  {imageClient{stub, stub}, [4]bool{true, false, false, false}},
		"speech":                 {speechClient{stub, stub}, [4]bool{false, true, false, false}},
		"video and decider":      {videoDecider{stub, stub, stub}, [4]bool{false, false, true, true}},
		"all but speech":         {allButSpeech{stub, stub, stub, stub}, [4]bool{true, false, true, true}},
		"all":                    {stub, [4]bool{true, true, true, true}},
		"all behind a decorator": {observed, [4]bool{true, true, true, true}},
	} {
		t.Run(name, func(t *testing.T) {
			spend := newHarness(t)
			wrapped := spend.wrap(t, test.client)
			_, image := wrapped.(llm.ImageGenerator)
			_, speech := wrapped.(llm.SpeechGenerator)
			_, video := wrapped.(llm.VideoGenerator)
			_, decider := wrapped.(llm.Decider)
			if got := [4]bool{image, speech, video, decider}; got != test.want {
				t.Errorf("capabilities = %v; want %v", got, test.want)
			}
			if unwrapper, ok := wrapped.(interface{ Unwrap() llm.Client }); !ok || unwrapper.Unwrap() != test.client {
				t.Error("Unwrap does not return the wrapped client")
			}
		})
	}
}

type nopObserver struct{}

func (nopObserver) CallStarted(ctx context.Context, _ *llm.Call) context.Context { return ctx }
func (nopObserver) CallFinished(context.Context, *llm.Call, *llm.CallOutcome)    {}

func TestConcurrentCallsAreAllCharged(t *testing.T) {
	spend := newHarness(t)
	const calls = 8
	turns := make([]llmtest.TurnScript, calls)
	for index := range turns {
		turns[index] = costTurn(0.001)
	}
	client := spend.wrap(t, llmtest.NewStubClient(turns...))
	var group sync.WaitGroup
	for range calls {
		group.Go(func() {
			if _, err := llm.Collect(context.Background(), client, llm.Request{}); err != nil {
				t.Error(err)
			}
		})
	}
	group.Wait()
	if spent := spend.spent(t, userAccount); spent[0] != calls*1000 {
		t.Errorf("spent = %d; want %d", spent[0], calls*1000)
	}
}

func TestPeriodStart(t *testing.T) {
	instant := time.Date(2026, time.March, 31, 23, 30, 0, 0, time.FixedZone("UTC-1", -60*60)) // April 1st 00:30 UTC
	day, err := budget.PeriodDay.Start(instant)
	if err != nil || !day.Equal(time.Date(2026, time.April, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("PeriodDay.Start = %v, %v", day, err)
	}
	month, err := budget.PeriodMonth.Start(instant)
	if err != nil || !month.Equal(time.Date(2026, time.April, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("PeriodMonth.Start = %v, %v", month, err)
	}
}
