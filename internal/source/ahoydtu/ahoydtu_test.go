package ahoydtu

import (
	"context"
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

// The captured fixtures come from a real AhoyDTU running 0.8.156. The inverter
// name is replaced with a neutral one, and /api/index carries no serial number,
// so nothing in them identifies a particular person's hardware.
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", name))
	if err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
	return b
}

func newSource(t *testing.T, body []byte, tweak func(*config.Device)) *Source {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/index" {
			t.Errorf("requested %s, want /api/index", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
	t.Cleanup(srv.Close)

	d := config.Device{Name: "test", Type: config.TypeAhoyDTU, Connection: "http", URL: srv.URL}
	if tweak != nil {
		tweak(&d)
	}
	return New(d)
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

func TestReadRealCapture(t *testing.T) {
	// A microinverter datalogger knows only what it is producing: no battery,
	// no grid meter, so every other field must stay empty.
	got := read(t, newSource(t, fixture(t, "ahoydtu-index.json"), nil))

	if got.ProducingWatt == nil || *got.ProducingWatt != 5 {
		t.Errorf("producingWatt = %v, want 5 (5.1 rounded)", deref(got.ProducingWatt))
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

func TestUnavailableInverterYieldsAnEmptyReading(t *testing.T) {
	// The datalogger stays powered and answers, but says it has no current data
	// for the inverter. That is not the same as zero production: a radio link
	// can drop in daylight too.
	got := read(t, newSource(t, fixture(t, "ahoydtu-index-unavailable.json"), nil))

	if !got.IsEmpty() {
		t.Errorf("reading = %+v, want empty so the push is skipped", got)
	}
}

func TestAvailableZeroIsReported(t *testing.T) {
	// A reachable inverter genuinely producing nothing is a real reading of
	// zero, and must be sent rather than withheld.
	body := []byte(`{"inverter":[{"enabled":true,"id":0,"name":"roof","cur_pwr":0,"is_avail":true,"is_producing":false}]}`)
	got := read(t, newSource(t, body, nil))

	if got.ProducingWatt == nil {
		t.Fatal("producingWatt = nil, want 0: the inverter answered and said zero")
	}
	if *got.ProducingWatt != 0 {
		t.Errorf("producingWatt = %d, want 0", *got.ProducingWatt)
	}
}

func TestSelectsTheConfiguredInverter(t *testing.T) {
	two := func(d *config.Device) { id := 1; d.InverterID = &id }
	got := read(t, newSource(t, fixture(t, "ahoydtu-index-multi.json"), two))

	if got.ProducingWatt == nil || *got.ProducingWatt != 211 {
		t.Errorf("producingWatt = %v, want 211 from inverter 1", deref(got.ProducingWatt))
	}
}

func TestDefaultsToTheFirstInverter(t *testing.T) {
	got := read(t, newSource(t, fixture(t, "ahoydtu-index-multi.json"), nil))
	if got.ProducingWatt == nil || *got.ProducingWatt != 5 {
		t.Errorf("producingWatt = %v, want 5 from inverter 0", deref(got.ProducingWatt))
	}
}

func TestUnknownInverterNamesWhatWasFound(t *testing.T) {
	missing := func(d *config.Device) { id := 7; d.InverterID = &id }
	_, err := newSource(t, fixture(t, "ahoydtu-index-multi.json"), missing).Read(context.Background())
	if err == nil {
		t.Fatal("want an error for an inverter the datalogger does not have")
	}
	for _, want := range []string{"7", "0", "1", "roof", "garage"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q so the config can be fixed: %v", want, err)
		}
	}
}

func TestDisabledInverterIsAnError(t *testing.T) {
	// Configured here but switched off there: an actionable mismatch, not a blip.
	body := []byte(`{"inverter":[{"enabled":false,"id":0,"name":"roof","cur_pwr":0,"is_avail":false}]}`)
	_, err := newSource(t, body, nil).Read(context.Background())
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("want a disabled-inverter error, got %v", err)
	}
}

func TestNoInvertersAtAllIsAnError(t *testing.T) {
	_, err := newSource(t, []byte(`{"inverter":[]}`), nil).Read(context.Background())
	if err == nil {
		t.Fatal("want an error when the datalogger lists no inverters")
	}
}

func TestNullPowerIsTreatedAsAbsent(t *testing.T) {
	// A null is the datalogger saying it has no value, which must not become a
	// claim of zero production.
	body := []byte(`{"inverter":[{"enabled":true,"id":0,"name":"roof","cur_pwr":null,"is_avail":true}]}`)
	got := read(t, newSource(t, body, nil))
	if !got.IsEmpty() {
		t.Errorf("reading = %+v, want empty for a null power value", got)
	}
}

func TestNegativeAndImplausiblePower(t *testing.T) {
	got := read(t, newSource(t, []byte(`{"inverter":[{"enabled":true,"id":0,"cur_pwr":-2.4,"is_avail":true}]}`), nil))
	if got.ProducingWatt == nil || *got.ProducingWatt != 0 {
		t.Errorf("producingWatt = %v, want 0 for a small negative", deref(got.ProducingWatt))
	}

	_, err := newSource(t, []byte(`{"inverter":[{"enabled":true,"id":0,"cur_pwr":1e12,"is_avail":true}]}`), nil).Read(context.Background())
	if err == nil || !strings.Contains(err.Error(), "implausible") {
		t.Errorf("want an implausible-value error, got %v", err)
	}
}

func TestNonJSONBodyIsDiagnosable(t *testing.T) {
	_, err := newSource(t, []byte("<html>rebooting</html>"), nil).Read(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not valid JSON") {
		t.Fatalf("want a readable error for an HTML body, got %v", err)
	}
}

func TestHTTPErrorStatus(t *testing.T) {
	for _, code := range []int{401, 404, 500} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "nope", code)
		}))
		_, err := New(config.Device{Name: "t", URL: srv.URL}).Read(context.Background())
		srv.Close()
		if err == nil || !strings.Contains(err.Error(), http.StatusText(code)) {
			t.Errorf("status %d: want an error naming the status, got %v", code, err)
		}
	}
}

func TestTrailingSlashInURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/index" {
			t.Errorf("path = %q, want /api/index", r.URL.Path)
		}
		w.Write([]byte(`{"inverter":[{"enabled":true,"id":0,"cur_pwr":1,"is_avail":true}]}`))
	}))
	defer srv.Close()

	if _, err := New(config.Device{Name: "t", URL: srv.URL + "/"}).Read(context.Background()); err != nil {
		t.Fatalf("Read: %v", err)
	}
}

func TestNameIsTheConfiguredDeviceName(t *testing.T) {
	s := New(config.Device{Name: "roof-south", URL: "http://10.0.0.197"})
	if s.Name() != "roof-south" {
		t.Errorf("Name() = %q, want roof-south", s.Name())
	}
}
