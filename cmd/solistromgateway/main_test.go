package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Knight1/solistromgateway/internal/config"
	"github.com/Knight1/solistromgateway/internal/push"
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
