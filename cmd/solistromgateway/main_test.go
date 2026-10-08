package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"github.com/Knight1/solistromgateway/internal/config"
	"github.com/Knight1/solistromgateway/internal/push"
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
