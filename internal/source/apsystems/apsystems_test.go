package apsystems

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Knight1/solistromgateway/internal/config"
	"github.com/Knight1/solistromgateway/internal/source"
)

// The fixture is a real EZ1 response with the device serial replaced. Unlike
// the other dataloggers, this one returns its serial on every endpoint, so it
// cannot be avoided by choosing a different one.
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", name))
	if err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
	return b
}

func newSource(t *testing.T, body []byte, status int) *Source {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/getOutputData" {
			t.Errorf("requested %s, want /getOutputData", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		if status != 0 && status != http.StatusOK {
			w.WriteHeader(status)
		}
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return New(config.Device{Name: "test", Type: config.TypeAPsystems, URL: srv.URL})
}

func read(t *testing.T, s *Source) source.Reading {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := s.Read(ctx)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	return got
}

func deref(p *int) any {
	if p == nil {
		return "nil"
	}
	return *p
}

func TestFixtureCarriesNoRealSerial(t *testing.T) {
	// A mechanical guard on the rule: this repository is public and the device
	// serial identifies a specific person's hardware.
	var doc struct {
		DeviceID string `json:"deviceId"`
	}
	if err := json.Unmarshal(fixture(t, "apsystems-outputdata.json"), &doc); err != nil {
		t.Fatalf("fixture is not valid JSON: %v", err)
	}
	if doc.DeviceID != "E00000000000" {
		t.Errorf("fixture deviceId = %q, want the placeholder E00000000000", doc.DeviceID)
	}
}

func TestReadRealCapture(t *testing.T) {
	// Two PV inputs at 5 W each. A microinverter knows only its own production.
	got := read(t, newSource(t, fixture(t, "apsystems-outputdata.json"), 0))

	if got.ProducingWatt == nil || *got.ProducingWatt != 10 {
		t.Errorf("producingWatt = %v, want 10 (p1 5 + p2 5)", deref(got.ProducingWatt))
	}
	for name, field := range map[string]*int{
		"watt": got.Watt, "soc": got.SOC,
		"chargingPower": got.ChargingPower, "powerStorageState": got.PowerStorageState,
	} {
		if field != nil {
			t.Errorf("%s = %d, want nil: this hardware cannot report it", name, *field)
		}
	}
}

func TestSumsEveryPowerPort(t *testing.T) {
	// Summing whatever ports exist rather than a hardcoded p1 and p2 means a
	// model with more inputs cannot be silently understated.
	body := []byte(`{"data":{"p1":100,"p2":50,"p3":25.4,"e1":1,"te1":2},"message":"SUCCESS","deviceId":"E00000000000"}`)
	got := read(t, newSource(t, body, 0))

	if got.ProducingWatt == nil || *got.ProducingWatt != 175 {
		t.Errorf("producingWatt = %v, want 175 across three ports", deref(got.ProducingWatt))
	}
}

func TestReachableZeroIsReported(t *testing.T) {
	body := []byte(`{"data":{"p1":0,"p2":0},"message":"SUCCESS","deviceId":"E00000000000"}`)
	got := read(t, newSource(t, body, 0))

	if got.ProducingWatt == nil {
		t.Fatal("producingWatt = nil, want 0: the device answered and said zero")
	}
	if *got.ProducingWatt != 0 {
		t.Errorf("producingWatt = %d, want 0", *got.ProducingWatt)
	}
}

func TestNonSuccessMessageIsAnError(t *testing.T) {
	body := []byte(`{"data":{},"message":"FAILURE","deviceId":"E00000000000"}`)
	_, err := newSource(t, body, 0).Read(context.Background())
	if err == nil || !strings.Contains(err.Error(), "FAILURE") {
		t.Fatalf("want an error naming the message, got %v", err)
	}
}

func TestNullPowerYieldsAnEmptyReading(t *testing.T) {
	// A null is the device saying it has no figure for that port. Summing the
	// rest would report a total we do not have.
	body := []byte(`{"data":{"p1":120,"p2":null},"message":"SUCCESS","deviceId":"E00000000000"}`)
	got := read(t, newSource(t, body, 0))

	if !got.IsEmpty() {
		t.Errorf("reading = %+v, want empty: one port had no value so the total is unknown", got)
	}
}

func TestNoPowerPortsYieldsAnEmptyReading(t *testing.T) {
	body := []byte(`{"data":{"e1":1,"te1":2},"message":"SUCCESS","deviceId":"E00000000000"}`)
	got := read(t, newSource(t, body, 0))
	if !got.IsEmpty() {
		t.Errorf("reading = %+v, want empty when no power ports are reported", got)
	}
}

func TestNegativeAndImplausiblePower(t *testing.T) {
	got := read(t, newSource(t, []byte(`{"data":{"p1":-1.5,"p2":0},"message":"SUCCESS"}`), 0))
	if got.ProducingWatt == nil || *got.ProducingWatt != 0 {
		t.Errorf("producingWatt = %v, want 0 for a small negative total", deref(got.ProducingWatt))
	}

	_, err := newSource(t, []byte(`{"data":{"p1":1e12},"message":"SUCCESS"}`), 0).Read(context.Background())
	if err == nil || !strings.Contains(err.Error(), "implausible") {
		t.Errorf("want an implausible-value error, got %v", err)
	}
}

func TestNonJSONBodyIsDiagnosable(t *testing.T) {
	// What this device actually returns for an unknown path.
	_, err := newSource(t, []byte("Nothing matches the given URI"), 0).Read(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not valid JSON") {
		t.Fatalf("want a readable error for a plain-text body, got %v", err)
	}
}

func TestErrorSnippetDoesNotCarryTheSerial(t *testing.T) {
	// The README tells people to paste log lines into an issue, so a serial in
	// an error message could travel into a public bug report.
	// The serial sits near the start deliberately. Put it later and the 60-char
	// snippet limit truncates it away, so the test would pass whether or not
	// redaction actually happened.
	body := []byte(`{"deviceId":"E99999999999","p1":5`)
	_, err := newSource(t, body, 0).Read(context.Background())
	if err == nil {
		t.Fatal("want an error for truncated JSON")
	}
	if strings.Contains(err.Error(), "E99999999999") {
		t.Errorf("the serial leaked into the error message: %v", err)
	}
}

func TestHTTPErrorStatus(t *testing.T) {
	for _, code := range []int{401, 404, 500} {
		_, err := newSource(t, []byte("nope"), code).Read(context.Background())
		if err == nil || !strings.Contains(err.Error(), http.StatusText(code)) {
			t.Errorf("status %d: want an error naming the status, got %v", code, err)
		}
	}
}

func TestTrailingSlashInURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/getOutputData" {
			t.Errorf("path = %q, want /getOutputData", r.URL.Path)
		}
		w.Write([]byte(`{"data":{"p1":1},"message":"SUCCESS"}`))
	}))
	defer srv.Close()

	if _, err := New(config.Device{Name: "t", URL: srv.URL + "/"}).Read(context.Background()); err != nil {
		t.Fatalf("Read: %v", err)
	}
}

func TestNameIsTheConfiguredDeviceName(t *testing.T) {
	if got := New(config.Device{Name: "balcony", URL: "http://10.0.0.207:8050"}).Name(); got != "balcony" {
		t.Errorf("Name() = %q, want balcony", got)
	}
}
