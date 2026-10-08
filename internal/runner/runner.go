// Package runner drives one polling loop per device.
package runner

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/Knight1/solistromgateway/internal/source"
)

// maxBackoff keeps the doubling from overflowing time.Duration on a device
// configured with many attempts, which would turn the wait into a busy loop.
const maxBackoff = 5 * time.Minute

// remindAfter is how long a continuing failure stays quiet before speaking up
// again, so a device that never comes back cannot go unnoticed by somebody
// watching at the default log level.
const remindAfter = 30 * time.Minute

// failureEpisode collapses one run of identical failures into a few log lines.
//
// A dark inverter is unreachable on every tick of a long night, which at a ten
// second interval is thousands of identical warnings. This tracks the run so
// the first failure is reported in full, the repeats drop to debug, a reminder
// goes out periodically, and recovery can say how long the outage lasted.
//
// It deliberately holds nothing that could influence a reading or a push. Every
// field here decides only what gets logged.
type failureEpisode struct {
	count     int
	startedAt time.Time
	lastSpoke time.Time
}

// failed records a failure and reports whether this one deserves attention
// rather than being demoted to debug.
func (e *failureEpisode) failed(now time.Time) bool {
	e.count++
	if e.count == 1 {
		e.startedAt = now
		e.lastSpoke = now
		return true
	}
	if now.Sub(e.lastSpoke) >= remindAfter {
		e.lastSpoke = now
		return true
	}
	return false
}

// recovered ends the run, returning how many failures it covered and how long
// it lasted. A count of zero means there was nothing to recover from.
func (e *failureEpisode) recovered(now time.Time) (int, time.Duration) {
	if e.count == 0 {
		return 0, 0
	}
	count, since := e.count, now.Sub(e.startedAt)
	*e = failureEpisode{}
	return count, since
}

// deviceLog carries one device's logging state. It is owned by that device's
// loop goroutine and shared with nothing, so it needs no locking.
type deviceLog struct {
	log   *slog.Logger
	read  failureEpisode
	empty failureEpisode
	push  failureEpisode
}

// failing logs a failure at the given level the first time and periodically
// after that, and at debug for the repeats in between.
func (dl *deviceLog) failing(e *failureEpisode, level slog.Level, msg string, args ...any) {
	if !e.failed(time.Now()) {
		dl.log.Debug(msg, args...)
		return
	}
	if level == slog.LevelError {
		dl.log.Error(msg, args...)
		return
	}
	dl.log.Warn(msg, args...)
}

// healthy ends any run of failures on e and announces the recovery.
func (dl *deviceLog) healthy(e *failureEpisode, msg string) {
	if count, since := e.recovered(time.Now()); count > 0 {
		dl.log.Info(msg, "failures", count, "down_for", since.Round(time.Second))
	}
}

// Pusher sends one reading. Implemented by push.Client.
type Pusher interface {
	Push(ctx context.Context, pushURL string, r source.Reading) error
}

// Device is everything one polling loop needs.
type Device struct {
	Name     string
	Source   source.Source
	PushURL  string
	Interval time.Duration
	Timeout  time.Duration
	Attempts int
	Backoff  time.Duration
}

// Run polls every device until ctx is cancelled, then waits for the in-flight
// work to finish.
//
// Each device runs independently: one that is unreachable keeps retrying on
// its own schedule without affecting the others.
func Run(ctx context.Context, devices []Device, p Pusher, log *slog.Logger) {
	var wg sync.WaitGroup
	for _, d := range devices {
		wg.Add(1)
		go func(d Device) {
			defer wg.Done()
			loop(ctx, d, p, log.With("device", d.Name))
		}(d)
	}
	wg.Wait()
}

func loop(ctx context.Context, d Device, p Pusher, log *slog.Logger) {
	dl := &deviceLog{log: log}

	// Read once straight away so a restart shows data immediately instead of
	// after a full interval of silence.
	tick(ctx, d, p, dl)

	ticker := time.NewTicker(d.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tick(ctx, d, p, dl)
		}
	}
}

// tick performs one read and, if it worked, one push with retries.
//
// loop calls tick synchronously, one at a time, which is what actually
// prevents a tick from overlapping the next one. The timeout below instead
// bounds how long one tick's retries may run, so a slow or failing device
// cannot make the schedule drift indefinitely.
func tick(ctx context.Context, d Device, p Pusher, dl *deviceLog) {
	tickCtx, cancelTick := context.WithTimeout(ctx, d.Interval)
	defer cancelTick()

	readCtx, cancelRead := context.WithTimeout(tickCtx, d.Timeout)
	reading, err := d.Source.Read(readCtx)
	cancelRead()
	if err != nil {
		// Skipping is deliberate: a gap is better than re-sending a stale
		// number that Solistrom would read as current.
		dl.failing(&dl.read, slog.LevelWarn, "could not read device, skipping this push", "error", err)
		return
	}
	dl.healthy(&dl.read, "device is answering again")

	if reading.IsEmpty() {
		dl.failing(&dl.empty, slog.LevelWarn, "device returned no usable values, skipping this push")
		return
	}
	dl.healthy(&dl.empty, "device is reporting usable values again")

	if err := pushWithRetries(tickCtx, d, p, reading, dl.log); err != nil {
		// A failure during shutdown is expected and not worth alarming anyone
		// about. Only the parent context being done means shutdown; tickCtx
		// also expires when a tick merely runs out of its own budget, which is
		// a real, ongoing failure and must stay visible.
		if ctx.Err() != nil {
			dl.log.Debug("push abandoned because the process is shutting down", "error", err)
			return
		}
		dl.failing(&dl.push, slog.LevelError, "giving up on this push until the next interval", "error", err)
		return
	}
	dl.healthy(&dl.push, "pushes are being accepted again")
}

func pushWithRetries(ctx context.Context, d Device, p Pusher, r source.Reading, log *slog.Logger) error {
	backoff := d.Backoff
	var err error

	for attempt := 1; attempt <= d.Attempts; attempt++ {
		pushCtx, cancel := context.WithTimeout(ctx, d.Timeout)
		err = p.Push(pushCtx, d.PushURL, r)
		cancel()

		if err == nil {
			log.Debug("pushed", "attempt", attempt)
			return nil
		}
		if attempt == d.Attempts {
			break
		}

		log.Debug("push attempt failed", "attempt", attempt, "of", d.Attempts, "waiting", backoff, "error", err)
		select {
		case <-ctx.Done():
			return err
		case <-time.After(backoff):
		}
		if backoff < maxBackoff {
			backoff *= 2
		}
	}
	return err
}
