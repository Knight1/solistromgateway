package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Knight1/solistromgateway/internal/config"
	"github.com/Knight1/solistromgateway/internal/push"
	"github.com/Knight1/solistromgateway/internal/runner"
	"github.com/Knight1/solistromgateway/internal/source"
	"github.com/Knight1/solistromgateway/internal/source/ahoydtu"
	"github.com/Knight1/solistromgateway/internal/source/apsystems"
	"github.com/Knight1/solistromgateway/internal/source/growatt"
)

func TestBuildDevicesMapsEveryField(t *testing.T) {
	cfg := &config.Config{Devices: []config.Device{{
		Name: "growatt-pv", Type: config.TypeGrowatt, Connection: "http",
		URL: "http://10.0.0.240", PushURL: "https://push.example.com/p?code=K",
		Battery: "auto",
	}}}
	// Durations are unexported inside config.Duration, so go through Load in
	// the real path; here just check the fields that are plain values.
	devices, err := buildDevices(cfg)
	if err != nil {
		t.Fatalf("buildDevices: %v", err)
	}
	if len(devices) != 1 {
		t.Fatalf("got %d devices, want 1", len(devices))
	}
	d := devices[0]
	if d.Name != "growatt-pv" {
		t.Errorf("name = %q", d.Name)
	}
	if d.PushURL != "https://push.example.com/p?code=K" {
		t.Errorf("push url = %q", d.PushURL)
	}
	if d.Source == nil {
		t.Error("source was not built")
	}
	if d.Source.Name() != "growatt-pv" {
		t.Errorf("source name = %q", d.Source.Name())
	}
}

func TestBuildDevicesRejectsUnimplementedType(t *testing.T) {
	// Config validation catches this first in the real path, but buildDevices
	// must not silently skip a device it cannot build.
	cfg := &config.Config{Devices: []config.Device{{
		Name: "dtu", Type: config.TypeOpenDTU, Connection: "http",
		URL: "http://10.0.0.9", PushURL: "https://push.example.com/p?code=K",
	}}}
	if _, err := buildDevices(cfg); err == nil {
		t.Fatal("want an error for an unimplemented device type")
	}
}

func TestParseLogLevel(t *testing.T) {
	tests := map[string]string{
		"debug": "DEBUG", "info": "INFO", "warn": "WARN", "error": "ERROR",
		"INFO": "INFO", "nonsense": "INFO",
	}
	for in, want := range tests {
		if got := parseLogLevel(in).String(); got != want {
			t.Errorf("parseLogLevel(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestExampleConfigIsValid(t *testing.T) {
	// The file a person copies must actually parse and validate.
	cfg, err := config.Load("../../config.example.json")
	if err != nil {
		t.Fatalf("config.example.json does not validate: %v", err)
	}
	if len(cfg.Devices) == 0 {
		t.Fatal("the example should show at least one device")
	}
	if cfg.Devices[0].Interval.Duration() < 5*time.Second {
		t.Error("the example interval is below the minimum")
	}
}

func TestExampleConfigHoldsNoRealCredential(t *testing.T) {
	cfg, err := config.Load("../../config.example.json")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !strings.Contains(cfg.Devices[0].PushURL, "example.com") {
		t.Errorf("the example push_url must point at example.com, got %q", cfg.Devices[0].PushURL)
	}
}

func TestEndToEndPayloadFromRealCapture(t *testing.T) {
	// The real capture is from an inverter with no battery and no meter, which
	// reports a literal 0 for SOC, battery voltage and both grid registers.
	// The body that reaches the API must carry production only: a "soc":0 would
	// tell the account the battery is flat, and a "watt":0 would claim the grid
	// is balanced and contradict the meter the account already reads.
	fixture, err := os.ReadFile(filepath.Join("..", "..", "testdata", "growatt-124-no-battery.json"))
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}

	inverter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(fixture)
	}))
	defer inverter.Close()

	var gotBody string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer api.Close()

	src := growatt.New(config.Device{Name: "e2e", URL: inverter.URL, Battery: "auto"})
	reading, err := src.Read(context.Background())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if err := push.New().Push(context.Background(), api.URL, reading); err != nil {
		t.Fatalf("Push: %v", err)
	}

	if gotBody != `{"producingWatt":782}` {
		t.Errorf("payload = %s, want {\"producingWatt\":782}", gotBody)
	}
}

func TestBuildDevicesBuildsAnAhoyDTUSource(t *testing.T) {
	cfg := &config.Config{Devices: []config.Device{{
		Name: "roof", Type: config.TypeAhoyDTU, Connection: "http",
		URL: "http://10.0.0.197", PushURL: "https://push.example.com/p?code=K",
	}}}
	devices, err := buildDevices(cfg)
	if err != nil {
		t.Fatalf("buildDevices: %v", err)
	}
	if len(devices) != 1 {
		t.Fatalf("got %d devices, want 1", len(devices))
	}
	if devices[0].Source == nil {
		t.Fatal("source was not built")
	}
	if devices[0].Source.Name() != "roof" {
		t.Errorf("source name = %q, want roof", devices[0].Source.Name())
	}
}

func TestEndToEndAhoyDTUPayloadFromRealCapture(t *testing.T) {
	// The captured datalogger response must reach the API as production only.
	// A microinverter has no battery and no grid meter, so a "soc" or a "watt"
	// here would be a claim about hardware that does not exist.
	fixture, err := os.ReadFile(filepath.Join("..", "..", "testdata", "ahoydtu-index.json"))
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}

	dtu := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(fixture)
	}))
	defer dtu.Close()

	var gotBody string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer api.Close()

	src := ahoydtu.New(config.Device{Name: "e2e", URL: dtu.URL})
	reading, err := src.Read(context.Background())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if err := push.New().Push(context.Background(), api.URL, reading); err != nil {
		t.Fatalf("Push: %v", err)
	}

	if gotBody != `{"producingWatt":5}` {
		t.Errorf("payload = %s, want {\"producingWatt\":5}", gotBody)
	}
}

func TestBuildDevicesBuildsAnAPsystemsSource(t *testing.T) {
	cfg := &config.Config{Devices: []config.Device{{
		Name: "balcony", Type: config.TypeAPsystems, Connection: "http",
		URL: "http://10.0.0.207:8050", PushURL: "https://push.example.com/p?code=K",
	}}}
	devices, err := buildDevices(cfg)
	if err != nil {
		t.Fatalf("buildDevices: %v", err)
	}
	if len(devices) != 1 || devices[0].Source == nil {
		t.Fatalf("source was not built: %+v", devices)
	}
	if devices[0].Source.Name() != "balcony" {
		t.Errorf("source name = %q, want balcony", devices[0].Source.Name())
	}
}

func TestEndToEndAPsystemsPayloadFromRealCapture(t *testing.T) {
	// The captured EZ1 response must reach the API as production only, with the
	// two panel inputs summed.
	fixture, err := os.ReadFile(filepath.Join("..", "..", "testdata", "apsystems-outputdata.json"))
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}

	ez1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(fixture)
	}))
	defer ez1.Close()

	var gotBody string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer api.Close()

	src := apsystems.New(config.Device{Name: "e2e", URL: ez1.URL})
	reading, err := src.Read(context.Background())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if err := push.New().Push(context.Background(), api.URL, reading); err != nil {
		t.Fatalf("Push: %v", err)
	}

	if gotBody != `{"producingWatt":10}` {
		t.Errorf("payload = %s, want {\"producingWatt\":10}", gotBody)
	}
}

func TestWantsVersion(t *testing.T) {
	tests := []struct {
		name string
		flag bool
		args []string
		want bool
	}{
		{"flag set", true, nil, true},
		{"bare command", false, []string{"version"}, true},
		{"nothing", false, nil, false},
		{"other argument", false, []string{"serve"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := wantsVersion(tt.flag, tt.args); got != tt.want {
				t.Errorf("wantsVersion(%v, %v) = %v, want %v", tt.flag, tt.args, got, tt.want)
			}
		})
	}
}

func TestDetailsPrefersTheBuildTimeVersion(t *testing.T) {
	// A tagged release build stamps the version in with ldflags.
	bi := &debug.BuildInfo{GoVersion: "go1.27.1"}
	bi.Main.Version = "(devel)"

	if got := details("v1.2.3", bi, true).Version; got != "v1.2.3" {
		t.Errorf("Version = %q, want v1.2.3", got)
	}
}

func TestDetailsFallsBackToTheModuleVersion(t *testing.T) {
	// go install module@v0.9.0 records the version without any ldflags.
	bi := &debug.BuildInfo{GoVersion: "go1.27.1"}
	bi.Main.Version = "v0.9.0"

	if got := details("", bi, true).Version; got != "v0.9.0" {
		t.Errorf("Version = %q, want v0.9.0", got)
	}
}

func TestDetailsReportsTheRevisionAndDirtyTree(t *testing.T) {
	bi := &debug.BuildInfo{
		GoVersion: "go1.27.1",
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "d947bc78f2e4214a94dbe9dff11bc49c0b4e1e30"},
			{Key: "vcs.modified", Value: "true"},
		},
	}
	bi.Main.Version = "(devel)"

	got := details("", bi, true)
	if !strings.HasPrefix(got.Revision, "d947bc7") {
		t.Errorf("Revision = %q, want it to start with the short revision", got.Revision)
	}
	if !strings.Contains(got.Revision, "modified") {
		t.Errorf("Revision = %q, want it to say the tree was modified", got.Revision)
	}
}

func TestDetailsOnACleanTreeDoesNotSayModified(t *testing.T) {
	bi := &debug.BuildInfo{
		GoVersion: "go1.27.1",
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "d947bc78f2e4214a94dbe9dff11bc49c0b4e1e30"},
			{Key: "vcs.modified", Value: "false"},
		},
	}
	if got := details("", bi, true).Revision; strings.Contains(got, "modified") {
		t.Errorf("Revision = %q, want no mention of modification on a clean tree", got)
	}
}

func TestDetailsWithoutBuildInfoDoesNotPanic(t *testing.T) {
	// debug.ReadBuildInfo can fail, and a version command must still answer.
	got := details("", nil, false)
	if got.Version == "" {
		t.Error("Version is empty; want something rather than nothing")
	}
	if got.String() == "" {
		t.Error("String() is empty")
	}
}

func TestVersionTextIsUsableInABugReport(t *testing.T) {
	bi := &debug.BuildInfo{
		GoVersion: "go1.27.1",
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "d947bc78f2e4214a94dbe9dff11bc49c0b4e1e30"},
		},
	}
	bi.Main.Version = "(devel)"

	out := details("", bi, true).String()
	for _, want := range []string{"solistromgateway", "d947bc7", "go1.27.1"} {
		if !strings.Contains(out, want) {
			t.Errorf("version text is missing %q:\n%s", want, out)
		}
	}
}

func TestDetailsShortensAPseudoVersion(t *testing.T) {
	// An untagged local build gets a pseudo-version like
	// v0.0.0-20261008095553-7acb3f4eb6b4+dirty. It is accurate but it repeats
	// the revision line and would clutter every startup log line, so an
	// untagged build just says so and lets the revision line carry the detail.
	bi := &debug.BuildInfo{GoVersion: "go1.27.1"}
	bi.Main.Version = "v0.0.0-20261008095553-7acb3f4eb6b4+dirty"

	if got := details("", bi, true).Version; got != "(devel)" {
		t.Errorf("Version = %q, want (devel) for an untagged build", got)
	}
}

func TestDetailsKeepsARealTag(t *testing.T) {
	bi := &debug.BuildInfo{GoVersion: "go1.27.1"}
	bi.Main.Version = "v1.4.0"

	if got := details("", bi, true).Version; got != "v1.4.0" {
		t.Errorf("Version = %q, want the tag kept as is", got)
	}
}

func TestDetailsReportsThePlatform(t *testing.T) {
	// Worth having in a bug report: the same binary behaves differently on a
	// Raspberry Pi than on a desktop, and the CPU count shapes the Go runtime.
	got := details("", nil, false)

	if got.OS != runtime.GOOS {
		t.Errorf("OS = %q, want %q", got.OS, runtime.GOOS)
	}
	if got.Arch != runtime.GOARCH {
		t.Errorf("Arch = %q, want %q", got.Arch, runtime.GOARCH)
	}
	if got.CPUs < 1 {
		t.Errorf("CPUs = %d, want at least 1", got.CPUs)
	}
}

func TestVersionTextIncludesThePlatform(t *testing.T) {
	bi := &debug.BuildInfo{GoVersion: "go1.27.1"}
	bi.Main.Version = "v1.0.0"

	out := details("", bi, true).String()
	for _, want := range []string{runtime.GOOS, runtime.GOARCH, "go1.27.1"} {
		if !strings.Contains(out, want) {
			t.Errorf("version text is missing %q:\n%s", want, out)
		}
	}
}

func TestStartupFieldsCarryTheBuildAndPlatform(t *testing.T) {
	// These are the first line of every log, so they need to be present and
	// compact rather than spread over several entries.
	d := buildDetails{
		Version: "v1.0.0", Revision: "abc1234", GoVersion: "go1.27.1",
		OS: "linux", Arch: "arm64", CPUs: 4,
	}
	fields := d.startupFields()

	flat := fmt.Sprint(fields...)
	for _, want := range []string{"v1.0.0", "abc1234", "go1.27.1", "linux/arm64", "4"} {
		if !strings.Contains(flat, want) {
			t.Errorf("startup fields are missing %q: %v", want, fields)
		}
	}
}

func TestStartupFieldsOmitAnUnknownRevision(t *testing.T) {
	// An empty revision would print as revision="" and clutter the line.
	d := buildDetails{Version: "v1.0.0", GoVersion: "go1.27.1", OS: "linux", Arch: "arm64", CPUs: 1}
	if flat := fmt.Sprint(d.startupFields()...); strings.Contains(flat, "revision") {
		t.Errorf("startup fields should leave out an unknown revision: %s", flat)
	}
}

func TestStartupLineShapeTheDockerWorkflowReliesOn(t *testing.T) {
	// .github/workflows/docker.yml greps the container log for these, to prove
	// the image actually runs. Renaming the message or the attribute would break
	// that workflow with a confusing failure, so the shape is pinned here where
	// the breakage is obvious.
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))

	d := buildDetails{Version: "v1.0.0", GoVersion: "go1.27.1", OS: "linux", Arch: "arm64", CPUs: 2}
	log.Info("starting", append(d.startupFields(), "devices", 3)...)

	out := buf.String()
	for _, want := range []string{"msg=starting", "devices=3"} {
		if !strings.Contains(out, want) {
			t.Errorf("startup line is missing %q, which the Docker workflow greps for:\n%s", want, out)
		}
	}
}

// probeSource is a stand-in device for the startup reachability tests. It
// records how many probes overlap, so the concurrency can be asserted without a
// timing bound.
type probeSource struct {
	name    string
	reading source.Reading
	err     error
	delay   time.Duration
	inUse   atomic.Int32
	peak    atomic.Int32
}

func (p *probeSource) Name() string { return p.name }

func (p *probeSource) Read(ctx context.Context) (source.Reading, error) {
	n := p.inUse.Add(1)
	for {
		peak := p.peak.Load()
		if n <= peak || p.peak.CompareAndSwap(peak, n) {
			break
		}
	}
	defer p.inUse.Add(-1)

	if p.delay > 0 {
		select {
		case <-ctx.Done():
			return source.Reading{}, ctx.Err()
		case <-time.After(p.delay):
		}
	}
	return p.reading, p.err
}

func probeDevice(name string, s source.Source) runner.Device {
	return runner.Device{Name: name, Source: s, PushURL: "https://push.example.com/p?code=K",
		Interval: time.Minute, Timeout: 5 * time.Second, Attempts: 1, Backoff: time.Second}
}

func TestProbeReportsEveryDeviceAsReachable(t *testing.T) {
	devices := []runner.Device{
		probeDevice("a", &probeSource{name: "a", reading: source.Reading{ProducingWatt: source.Int(5)}}),
		probeDevice("b", &probeSource{name: "b", reading: source.Reading{ProducingWatt: source.Int(0)}}),
	}
	got := probeDevices(context.Background(), devices)

	if len(got) != 2 {
		t.Fatalf("got %d results, want 2", len(got))
	}
	for _, r := range got {
		if r.Err != nil {
			t.Errorf("device %q reported %v, want no error", r.Name, r.Err)
		}
		if r.Empty {
			t.Errorf("device %q was marked as having no data", r.Name)
		}
	}
}

func TestProbeNamesTheDeviceThatCouldNotBeReached(t *testing.T) {
	devices := []runner.Device{
		probeDevice("good", &probeSource{name: "good", reading: source.Reading{ProducingWatt: source.Int(5)}}),
		probeDevice("broken", &probeSource{name: "broken", err: errors.New("connection refused")}),
	}
	got := probeDevices(context.Background(), devices)

	byName := map[string]probeResult{}
	for _, r := range got {
		byName[r.Name] = r
	}
	if byName["good"].Err != nil {
		t.Errorf("good reported %v, want no error", byName["good"].Err)
	}
	if byName["broken"].Err == nil {
		t.Error("broken reported no error, want one")
	} else if !strings.Contains(byName["broken"].Err.Error(), "connection refused") {
		t.Errorf("broken reported %v, want the underlying reason", byName["broken"].Err)
	}
}

func TestProbeSeparatesReachableWithNoDataFromUnreachable(t *testing.T) {
	// An AhoyDTU inverter marked unavailable, or a null reading, answers fine
	// but has nothing to report. That is not a fault and must not read as one.
	devices := []runner.Device{
		probeDevice("quiet", &probeSource{name: "quiet"}), // zero Reading, no error
	}
	got := probeDevices(context.Background(), devices)

	if got[0].Err != nil {
		t.Errorf("Err = %v, want nil: the device answered", got[0].Err)
	}
	if !got[0].Empty {
		t.Error("Empty = false, want true: the device had nothing to report")
	}
}

func TestProbeReadsDevicesConcurrently(t *testing.T) {
	// Startup should take about as long as the slowest device, not the sum, or a
	// handful of unreachable devices would stall it one timeout at a time.
	shared := &probeSource{name: "shared", delay: 60 * time.Millisecond}
	devices := []runner.Device{
		probeDevice("a", shared), probeDevice("b", shared), probeDevice("c", shared),
	}
	probeDevices(context.Background(), devices)

	if peak := shared.peak.Load(); peak < 2 {
		t.Errorf("peak concurrent probes = %d, want at least 2", peak)
	}
}

func TestProbeHonoursItsDeadline(t *testing.T) {
	devices := []runner.Device{
		probeDevice("slow", &probeSource{name: "slow", delay: 10 * time.Second}),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	got := probeDevices(ctx, devices)

	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("probe took %v, want it bounded by the deadline", elapsed)
	}
	if got[0].Err == nil {
		t.Error("a device that never answered should report an error")
	}
}

func TestReachabilitySummaryWhenEverythingAnswers(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	logReachability(log, []probeResult{{Name: "a"}, {Name: "b"}})

	out := buf.String()
	if !strings.Contains(out, "level=INFO") {
		t.Errorf("want an informational summary when all devices answer:\n%s", out)
	}
	if strings.Contains(out, "level=WARN") || strings.Contains(out, "level=ERROR") {
		t.Errorf("nothing is wrong, so nothing should be raised:\n%s", out)
	}
	if !strings.Contains(out, "reachable=2") {
		t.Errorf("summary should count the devices that answered:\n%s", out)
	}
}

func TestReachabilitySummaryNamesWhatFailed(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	logReachability(log, []probeResult{
		{Name: "growatt-pv"},
		{Name: "roof", Err: errors.New("connection refused")},
		{Name: "balcony", Err: errors.New("no route to host")},
	})

	out := buf.String()
	if !strings.Contains(out, "level=WARN") {
		t.Errorf("want a warning when a device cannot be reached:\n%s", out)
	}
	for _, want := range []string{"roof", "balcony", "connection refused", "no route to host", "reachable=1", "unreachable=2"} {
		if !strings.Contains(out, want) {
			t.Errorf("summary is missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "growatt-pv") {
		t.Errorf("the working device should not be listed among the failures:\n%s", out)
	}
}

func TestReachabilitySummaryMentionsDevicesWithNoData(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	logReachability(log, []probeResult{{Name: "roof", Empty: true}})

	out := buf.String()
	if !strings.Contains(out, "roof") {
		t.Errorf("a device with nothing to report should still be named:\n%s", out)
	}
	if strings.Contains(out, "unreachable=1") {
		t.Errorf("answering with no data is not the same as being unreachable:\n%s", out)
	}
}

func TestReachabilitySummaryLineItselfNamesTheFailures(t *testing.T) {
	// Asserting the names appear somewhere in the output is too weak: the
	// per-device lines would satisfy it on their own. The one-line summary has
	// to carry them too, so a long log can be scanned without reading further.
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	logReachability(log, []probeResult{
		{Name: "growatt-pv"},
		{Name: "roof", Err: errors.New("connection refused")},
	})

	var summary string
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.Contains(line, "some devices could not be reached") {
			summary = line
			break
		}
	}
	if summary == "" {
		t.Fatalf("no summary line found:\n%s", buf.String())
	}
	if !strings.Contains(summary, "roof") {
		t.Errorf("the summary line does not name the failing device:\n%s", summary)
	}
}
