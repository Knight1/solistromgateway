package runner

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Knight1/solistromgateway/internal/source"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeSource returns a fixed reading, or an error, and counts calls.
type fakeSource struct {
	name    string
	reading source.Reading
	err     error
	calls   atomic.Int32
}

func (f *fakeSource) Name() string { return f.name }
func (f *fakeSource) Read(ctx context.Context) (source.Reading, error) {
	f.calls.Add(1)
	// Honour the context the way a real source must: a cancelled read cannot
	// hand back a usable value.
	if err := ctx.Err(); err != nil {
		return source.Reading{}, err
	}
	return f.reading, f.err
}

// fakePusher records what it was asked to send and can fail a set number of
// times before succeeding.
type fakePusher struct {
	mu       sync.Mutex
	sent     []source.Reading
	failures int
	calls    atomic.Int32
}

func (f *fakePusher) Push(ctx context.Context, pushURL string, r source.Reading) error {
	f.calls.Add(1)
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failures > 0 {
		f.failures--
		return errors.New("push failed")
	}
	f.sent = append(f.sent, r)
	return nil
}

func (f *fakePusher) sentCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

func device(name string, s source.Source) Device {
	return Device{
		Name: name, Source: s, PushURL: "https://push.example.com/p?code=K",
		Interval: 50 * time.Millisecond, Timeout: 20 * time.Millisecond,
		Attempts: 3, Backoff: time.Millisecond,
	}
}

func TestPushesImmediatelyWithoutWaitingForTheFirstTick(t *testing.T) {
	// A person restarting the tool should see data at once, not after a
	// full interval of silence.
	src := &fakeSource{name: "a", reading: source.Reading{ProducingWatt: source.Int(782)}}
	p := &fakePusher{}

	d := device("a", src)
	d.Interval = time.Hour // so only the immediate read can happen

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	Run(ctx, []Device{d}, p, quietLogger())

	if p.sentCount() != 1 {
		t.Fatalf("sent %d readings, want 1 from the immediate first read", p.sentCount())
	}
	if got := p.sent[0].ProducingWatt; got == nil || *got != 782 {
		t.Errorf("sent the wrong reading: %v", got)
	}
}

func TestReadFailureSkipsThePush(t *testing.T) {
	// The fake deliberately returns a usable reading together with its error.
	// If it returned an empty one, the empty-reading check would stop the push
	// on its own and this test would pass even with the error check removed.
	// Pushing a stale number is worse than a gap: the API would read it as
	// current production that never happened.
	src := &fakeSource{
		name:    "a",
		reading: source.Reading{ProducingWatt: source.Int(500)},
		err:     errors.New("connection refused"),
	}
	p := &fakePusher{}

	d := device("a", src)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	Run(ctx, []Device{d}, p, quietLogger())

	if p.calls.Load() != 0 {
		t.Errorf("pushed %d times after read failures, want 0", p.calls.Load())
	}
	if src.calls.Load() == 0 {
		t.Error("the source was never read")
	}
}

func TestEmptyReadingIsNotPushed(t *testing.T) {
	src := &fakeSource{name: "a", reading: source.Reading{}}
	p := &fakePusher{}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	Run(ctx, []Device{device("a", src)}, p, quietLogger())

	if p.calls.Load() != 0 {
		t.Errorf("pushed %d times for an empty reading, want 0", p.calls.Load())
	}
}

func TestRetriesUntilSuccess(t *testing.T) {
	src := &fakeSource{name: "a", reading: source.Reading{ProducingWatt: source.Int(100)}}
	p := &fakePusher{failures: 2}

	d := device("a", src)
	d.Interval = time.Hour // isolate the first tick

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	Run(ctx, []Device{d}, p, quietLogger())

	if p.calls.Load() != 3 {
		t.Errorf("push attempted %d times, want 3 (two failures then success)", p.calls.Load())
	}
	if p.sentCount() != 1 {
		t.Errorf("delivered %d readings, want 1", p.sentCount())
	}
}

func TestGivesUpAfterAttemptsExhausted(t *testing.T) {
	src := &fakeSource{name: "a", reading: source.Reading{ProducingWatt: source.Int(100)}}
	p := &fakePusher{failures: 99}

	d := device("a", src)
	d.Interval = time.Hour
	d.Attempts = 2

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	Run(ctx, []Device{d}, p, quietLogger())

	if p.calls.Load() != 2 {
		t.Errorf("push attempted %d times, want exactly 2", p.calls.Load())
	}
}

func TestOneFailingDeviceDoesNotStopAnother(t *testing.T) {
	bad := &fakeSource{name: "bad", err: errors.New("unreachable")}
	good := &fakeSource{name: "good", reading: source.Reading{ProducingWatt: source.Int(500)}}
	p := &fakePusher{}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	Run(ctx, []Device{device("bad", bad), device("good", good)}, p, quietLogger())

	// A 50ms interval over 300ms gives the healthy device roughly six ticks.
	// Insisting on several, rather than merely one, is what makes this test
	// fail if the devices were ever run one after another: a sequential Run
	// would not reach the second device until the first loop ended, leaving
	// time for at most a single tick.
	if n := p.sentCount(); n < 3 {
		t.Errorf("healthy device pushed %d times, want at least 3", n)
	}
	if bad.calls.Load() == 0 {
		t.Error("the failing device stopped being retried")
	}
}

func TestRunReturnsOnContextCancel(t *testing.T) {
	src := &fakeSource{name: "a", reading: source.Reading{ProducingWatt: source.Int(1)}}
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		Run(ctx, []Device{device("a", src)}, &fakePusher{}, quietLogger())
		close(done)
	}()

	time.Sleep(60 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after the context was cancelled")
	}
}

// countingPusher records the greatest number of pushes in flight at once.
//
// Unlike fakePusher it deliberately ignores its context and always sleeps the
// full delay. That is the point: it has to stay in flight past the tick
// deadline so that, if ticks ever ran concurrently, two of them would be
// pushing at the same moment and the peak would show it. A fake that returned
// early on cancellation would end before any overlap could be observed.
type countingPusher struct {
	delay time.Duration
	inUse atomic.Int32
	peak  atomic.Int32
}

func (c *countingPusher) Push(ctx context.Context, pushURL string, r source.Reading) error {
	n := c.inUse.Add(1)
	for {
		peak := c.peak.Load()
		if n <= peak || c.peak.CompareAndSwap(peak, n) {
			break
		}
	}
	defer c.inUse.Add(-1)

	time.Sleep(c.delay)
	return errors.New("push failed")
}

func TestTickCannotOverlapTheNextOne(t *testing.T) {
	// One device whose pushes take twice its interval. Ticks run one after
	// another, so however slow a tick is, only one push can ever be in flight
	// for a single device: the ticker's pending tick is simply picked up when
	// the previous tick finishes.
	//
	// Counting total pushes cannot show this, because the test's own deadline
	// bounds the total whether or not ticks overlap. Observing how many are in
	// flight at once measures the property directly.
	src := &fakeSource{name: "a", reading: source.Reading{ProducingWatt: source.Int(1)}}
	p := &countingPusher{delay: 100 * time.Millisecond}

	d := device("a", src)
	d.Interval = 50 * time.Millisecond
	d.Timeout = 45 * time.Millisecond
	d.Attempts = 1 // one push per tick keeps the timing easy to reason about
	d.Backoff = time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	Run(ctx, []Device{d}, p, quietLogger())

	if peak := p.peak.Load(); peak > 1 {
		t.Errorf("%d pushes were in flight at once for one device; ticks overlapped", peak)
	}
	if n := p.inUse.Load(); n != 0 {
		t.Errorf("%d pushes still in flight after Run returned", n)
	}
}

// timingPusher records the moment each push attempt arrived, and fails the
// first failures attempts before succeeding. done is closed once wanted
// attempts have been recorded, so the test can cancel its context immediately
// instead of waiting out a fixed deadline on every trial.
type timingPusher struct {
	mu       sync.Mutex
	times    []time.Time
	failures int
	wanted   int
	done     chan struct{}
}

func (t *timingPusher) Push(ctx context.Context, pushURL string, r source.Reading) error {
	t.mu.Lock()
	t.times = append(t.times, time.Now())
	fail := t.failures > 0
	if fail {
		t.failures--
	}
	done := len(t.times) >= t.wanted
	t.mu.Unlock()
	if done {
		select {
		case <-t.done:
		default:
			close(t.done)
		}
	}
	if fail {
		return errors.New("push failed")
	}
	return nil
}

// TestBackoffDoublesEachRetry pins the README's explicit promise that each
// retry waits twice as long as the one before. It records the gaps between
// push attempts and checks the second gap is roughly double the first,
// repeating several times with generous tolerances so ordinary scheduling
// jitter cannot make it flaky.
func TestBackoffDoublesEachRetry(t *testing.T) {
	const trials = 10
	for trial := 0; trial < trials; trial++ {
		src := &fakeSource{name: "a", reading: source.Reading{ProducingWatt: source.Int(1)}}
		p := &timingPusher{failures: 2, wanted: 3, done: make(chan struct{})} // two failures, then success: two gaps to measure

		d := device("a", src)
		d.Interval = time.Hour // isolate the first tick
		d.Attempts = 3
		d.Backoff = 40 * time.Millisecond

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		runDone := make(chan struct{})
		go func() {
			Run(ctx, []Device{d}, p, quietLogger())
			close(runDone)
		}()

		select {
		case <-p.done:
		case <-time.After(2 * time.Second):
			t.Fatalf("trial %d: did not see 3 push attempts in time", trial)
		}
		cancel()
		<-runDone

		p.mu.Lock()
		times := append([]time.Time(nil), p.times...)
		p.mu.Unlock()

		if len(times) != 3 {
			t.Fatalf("trial %d: got %d push attempts, want 3", trial, len(times))
		}

		gap1 := times[1].Sub(times[0])
		gap2 := times[2].Sub(times[1])

		ratio := float64(gap2) / float64(gap1)
		if ratio < 1.5 || ratio > 3.0 {
			t.Errorf("trial %d: gap1=%v gap2=%v, ratio=%.2f, want roughly 2x", trial, gap1, gap2, ratio)
		}
	}
}

// TestShutdownDuringRetryDoesNotLogAsError proves that when a tick's context
// is cancelled out from under it (the normal shape of Ctrl-C during a retry
// backoff), the final "giving up" line is logged at debug level rather than
// error. A real, non-shutdown failure must still be logged at error level.
func TestShutdownDuringRetryDoesNotLogAsError(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	src := &fakeSource{name: "a", reading: source.Reading{ProducingWatt: source.Int(1)}}
	p := &fakePusher{failures: 1000}

	d := device("a", src)
	d.Interval = time.Hour
	d.Attempts = 100
	d.Backoff = 100 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		Run(ctx, []Device{d}, p, log)
		close(done)
	}()

	// Let it fail at least once and enter the backoff wait, then cancel to
	// simulate shutdown mid-retry.
	time.Sleep(30 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after the context was cancelled")
	}

	out := buf.String()
	if strings.Contains(out, "level=ERROR") {
		t.Errorf("expected no error-level log line on shutdown, got:\n%s", out)
	}
}

// TestGenuineFailureStillLogsAsError proves the suppression above does not
// silence real failures: when attempts are exhausted without the context
// ever being cancelled, the "giving up" line must still be at error level.
func TestGenuineFailureStillLogsAsError(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	src := &fakeSource{name: "a", reading: source.Reading{ProducingWatt: source.Int(1)}}
	p := &fakePusher{failures: 99}

	d := device("a", src)
	d.Interval = time.Hour
	d.Attempts = 2
	d.Backoff = time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	Run(ctx, []Device{d}, p, log)

	out := buf.String()
	if !strings.Contains(out, "level=ERROR") {
		t.Errorf("expected an error-level log line for a genuine failure, got:\n%s", out)
	}
}

// TestExhaustedTickBudgetStillLogsAsError proves that a tick which runs out
// of its own budget (Attempts x Timeout plus backoff does not fit inside
// Interval) is a real, ongoing failure and must stay at error level. Only a
// shutdown is quiet. Earlier this check looked at the tick's own context,
// which is done in both cases, so a device failing forever would have been
// logged at debug and effectively invisible at the default log level.
func TestExhaustedTickBudgetStillLogsAsError(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	src := &fakeSource{name: "a", reading: source.Reading{ProducingWatt: source.Int(1)}}
	p := &fakePusher{failures: 1000}

	d := device("a", src)
	d.Interval = 60 * time.Millisecond
	d.Timeout = 25 * time.Millisecond
	d.Attempts = 10
	d.Backoff = 30 * time.Millisecond // budget cannot fit ten attempts

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	Run(ctx, []Device{d}, p, log)

	if !strings.Contains(buf.String(), "level=ERROR") {
		t.Errorf("a tick that exhausted its budget should log at error level, got:\n%s", buf.String())
	}
}

// scriptedSource fails or succeeds according to a script, one entry per read,
// and repeats the final entry once the script runs out.
type scriptedSource struct {
	name    string
	script  []bool // true means this read fails
	reading source.Reading
	calls   atomic.Int32
}

func (s *scriptedSource) Name() string { return s.name }

func (s *scriptedSource) Read(ctx context.Context) (source.Reading, error) {
	n := int(s.calls.Add(1)) - 1
	if err := ctx.Err(); err != nil {
		return source.Reading{}, err
	}
	if n >= len(s.script) {
		n = len(s.script) - 1
	}
	if s.script[n] {
		return source.Reading{}, errors.New("unreachable")
	}
	return s.reading, nil
}

// countLines returns how many non-blank lines contain all of the substrings.
func countLines(out string, substrings ...string) int {
	n := 0
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		matched := true
		for _, s := range substrings {
			if !strings.Contains(line, s) {
				matched = false
				break
			}
		}
		if matched {
			n++
		}
	}
	return n
}

func debugLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})), &buf
}

func TestFailureEpisodeWarnsOnceThenStaysQuiet(t *testing.T) {
	// A dark inverter fails every tick all night. Only the first failure of a
	// run deserves the operator's attention.
	t0 := time.Date(2026, 10, 7, 19, 0, 0, 0, time.UTC)
	var e failureEpisode

	if !e.failed(t0) {
		t.Error("the first failure of a run should be reported")
	}
	if e.failed(t0.Add(time.Minute)) {
		t.Error("a repeat a minute later should stay quiet")
	}
	if e.failed(t0.Add(5 * time.Minute)) {
		t.Error("a repeat five minutes later should stay quiet")
	}
}

func TestFailureEpisodeRemindsAfterTheReminderInterval(t *testing.T) {
	// A device that never comes back must not go unnoticed at the default log
	// level, so a continuing failure speaks up again periodically.
	t0 := time.Date(2026, 10, 7, 19, 0, 0, 0, time.UTC)
	var e failureEpisode

	e.failed(t0)
	if e.failed(t0.Add(remindAfter - time.Second)) {
		t.Error("just short of the reminder interval should stay quiet")
	}
	if !e.failed(t0.Add(remindAfter + time.Second)) {
		t.Error("past the reminder interval should speak up again")
	}
	if e.failed(t0.Add(remindAfter + 2*time.Second)) {
		t.Error("the reminder should reset the clock, so the next repeat is quiet")
	}
}

func TestFailureEpisodeRecoveryReportsCountAndDuration(t *testing.T) {
	t0 := time.Date(2026, 10, 7, 19, 0, 0, 0, time.UTC)
	var e failureEpisode

	e.failed(t0)
	e.failed(t0.Add(time.Minute))
	e.failed(t0.Add(2 * time.Minute))

	count, since := e.recovered(t0.Add(3 * time.Minute))
	if count != 3 {
		t.Errorf("count = %d, want 3", count)
	}
	if since != 3*time.Minute {
		t.Errorf("duration = %v, want 3m", since)
	}

	// The episode is over, so a later recovery has nothing to report.
	if count, _ := e.recovered(t0.Add(4 * time.Minute)); count != 0 {
		t.Errorf("a second recovery reported %d failures, want 0", count)
	}
}

func TestFailureEpisodeRecoveryOnAHealthyDeviceReportsNothing(t *testing.T) {
	var e failureEpisode
	if count, _ := e.recovered(time.Now()); count != 0 {
		t.Errorf("count = %d, want 0 when there was no failure to recover from", count)
	}
}

func TestRepeatedReadFailuresWarnOnlyOnce(t *testing.T) {
	// The night case: thousands of identical failures must not become thousands
	// of warnings.
	log, buf := debugLogger()
	src := &fakeSource{name: "a", err: errors.New("connection refused")}

	d := device("a", src)
	d.Interval = 20 * time.Millisecond
	d.Timeout = 5 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	Run(ctx, []Device{d}, &fakePusher{}, log)

	out := buf.String()
	if n := countLines(out, "level=WARN", "could not read device"); n != 1 {
		t.Errorf("got %d warnings about an unreadable device, want exactly 1:\n%s", n, out)
	}
	if n := countLines(out, "level=DEBUG", "could not read device"); n < 3 {
		t.Errorf("got %d debug lines for the repeats, want several so debug still shows them all:\n%s", n, out)
	}
}

func TestDeviceRecoveryLogsHowLongItWasDown(t *testing.T) {
	// The line the owner wants at breakfast.
	log, buf := debugLogger()
	src := &scriptedSource{
		name:    "a",
		script:  []bool{true, true, true, false},
		reading: source.Reading{ProducingWatt: source.Int(782)},
	}

	d := device("a", src)
	d.Interval = 20 * time.Millisecond
	d.Timeout = 5 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	Run(ctx, []Device{d}, &fakePusher{}, log)

	out := buf.String()
	if n := countLines(out, "level=INFO", "answering again"); n != 1 {
		t.Errorf("got %d recovery lines, want exactly 1:\n%s", n, out)
	}
	if !strings.Contains(out, "failures=3") {
		t.Errorf("the recovery line should say how many failures it covered:\n%s", out)
	}
}

func TestASecondOutageWarnsAgain(t *testing.T) {
	// Suppression is per run of failures, not for the life of the process.
	log, buf := debugLogger()
	src := &scriptedSource{
		name:    "a",
		script:  []bool{true, false, true},
		reading: source.Reading{ProducingWatt: source.Int(500)},
	}

	d := device("a", src)
	d.Interval = 20 * time.Millisecond
	d.Timeout = 5 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	Run(ctx, []Device{d}, &fakePusher{}, log)

	out := buf.String()
	if n := countLines(out, "level=WARN", "could not read device"); n != 2 {
		t.Errorf("got %d warnings across two separate outages, want 2:\n%s", n, out)
	}
}

func TestRepeatedPushFailuresLogErrorOnlyOnce(t *testing.T) {
	// A Solistrom outage repeats as relentlessly as a dark inverter.
	log, buf := debugLogger()
	src := &fakeSource{name: "a", reading: source.Reading{ProducingWatt: source.Int(1)}}
	p := &fakePusher{failures: 1000}

	d := device("a", src)
	d.Interval = 20 * time.Millisecond
	d.Timeout = 5 * time.Millisecond
	d.Attempts = 1

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	Run(ctx, []Device{d}, p, log)

	out := buf.String()
	if n := countLines(out, "level=ERROR", "giving up"); n != 1 {
		t.Errorf("got %d error lines for a continuing push failure, want exactly 1:\n%s", n, out)
	}
	if n := countLines(out, "level=DEBUG", "giving up"); n < 3 {
		t.Errorf("got %d debug lines for the repeats, want several:\n%s", n, out)
	}
}
