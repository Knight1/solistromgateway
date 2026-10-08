package growatt

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

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", name))
	if err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
	return b
}

// newSource builds a Source pointed at a server serving body, with the
// defaults a validated config would carry.
func newSource(t *testing.T, body []byte, tweak func(*config.Device)) *Source {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/status" {
			t.Errorf("requested %s, want /status", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
	t.Cleanup(srv.Close)

	d := config.Device{Name: "test", Type: config.TypeGrowatt, Connection: "http", URL: srv.URL, Battery: "auto"}
	if tweak != nil {
		tweak(&d)
	}
	return New(d)
}

func read(t *testing.T, s *Source) (r source.Reading) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := s.Read(ctx)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	return got
}

func TestReadProtocol124NoBattery(t *testing.T) {
	// The case that matters most: the inverter reports 0 for every battery and
	// meter register because none of that hardware exists. Those must stay nil.
	got := read(t, newSource(t, fixture(t, "growatt-124-no-battery.json"), nil))

	if got.ProducingWatt == nil || *got.ProducingWatt != 782 {
		t.Errorf("producingWatt = %v, want 782 (781.8 rounded)", deref(got.ProducingWatt))
	}
	if got.Watt != nil {
		t.Errorf("watt = %d, want nil: there is no meter, and report_grid is off", *got.Watt)
	}
	if got.SOC != nil {
		t.Errorf("soc = %d, want nil: there is no battery", *got.SOC)
	}
	if got.ChargingPower != nil {
		t.Errorf("chargingPower = %d, want nil", *got.ChargingPower)
	}
	if got.PowerStorageState != nil {
		t.Errorf("powerStorageState = %d, want nil", *got.PowerStorageState)
	}
}

func TestReadProtocol124Charging(t *testing.T) {
	got := read(t, newSource(t, fixture(t, "growatt-124-charging.json"), nil))

	if got.ProducingWatt == nil || *got.ProducingWatt != 1900 {
		t.Errorf("producingWatt = %v, want 1900", deref(got.ProducingWatt))
	}
	if got.SOC == nil || *got.SOC != 64 {
		t.Errorf("soc = %v, want 64", deref(got.SOC))
	}
	if got.ChargingPower == nil || *got.ChargingPower != 1200 {
		t.Errorf("chargingPower = %v, want 1200", deref(got.ChargingPower))
	}
	if got.PowerStorageState == nil || *got.PowerStorageState != 1 {
		t.Errorf("powerStorageState = %v, want 1 (charging)", deref(got.PowerStorageState))
	}
	if got.Watt != nil {
		t.Error("watt should stay nil while report_grid is off")
	}
}

func TestDischargingDerivesStateThree(t *testing.T) {
	body := []byte(`{"OutputPower":900,"ChargePower":0,"DischargePower":750,"BatteryVoltage":50.9,"SOC":41}`)
	got := read(t, newSource(t, body, nil))

	if got.PowerStorageState == nil || *got.PowerStorageState != 3 {
		t.Errorf("powerStorageState = %v, want 3 (discharging)", deref(got.PowerStorageState))
	}
	if got.ChargingPower == nil || *got.ChargingPower != 750 {
		t.Errorf("chargingPower = %v, want 750 as a positive magnitude", deref(got.ChargingPower))
	}
}

func TestIdleBatteryReportsStateZero(t *testing.T) {
	body := []byte(`{"OutputPower":900,"ChargePower":0,"DischargePower":0,"BatteryVoltage":50.9,"SOC":41}`)
	got := read(t, newSource(t, body, nil))

	if got.PowerStorageState == nil || *got.PowerStorageState != 0 {
		t.Errorf("powerStorageState = %v, want 0 (idle)", deref(got.PowerStorageState))
	}
	if got.ChargingPower == nil || *got.ChargingPower != 0 {
		t.Errorf("chargingPower = %v, want 0", deref(got.ChargingPower))
	}
}

func TestAbsentChargeRegistersStayEmpty(t *testing.T) {
	// A battery is detected by voltage, but this document carries no charge or
	// discharge register at all. Reporting 0 and idle would be an assertion we
	// have no basis for.
	body := []byte(`{"OutputPower":500,"BatteryVoltage":51.2,"SOC":55}`)
	got := read(t, newSource(t, body, nil))

	if got.SOC == nil || *got.SOC != 55 {
		t.Errorf("soc = %v, want 55", deref(got.SOC))
	}
	if got.ChargingPower != nil {
		t.Errorf("chargingPower = %d, want nil: no power register was reported", *got.ChargingPower)
	}
	if got.PowerStorageState != nil {
		t.Errorf("powerStorageState = %d, want nil: direction is unknown", *got.PowerStorageState)
	}
}

func TestNullRegisterIsTreatedAsAbsent(t *testing.T) {
	// A gateway emitting null is saying it has no value. json.Unmarshal accepts
	// null into a float without error, leaving zero, so this has to be caught
	// explicitly or it becomes an asserted zero.
	body := []byte(`{"OutputPower":500,"BatteryVoltage":51.2,"SOC":null,"ChargePower":null,"DischargePower":null}`)
	got := read(t, newSource(t, body, nil))

	if got.SOC != nil {
		t.Errorf("soc = %d, want nil for a null register", *got.SOC)
	}
	if got.ChargingPower != nil {
		t.Errorf("chargingPower = %d, want nil for a null register", *got.ChargingPower)
	}
}

func TestReportGridOptIn(t *testing.T) {
	on := func(d *config.Device) { d.ReportGrid = true }
	got := read(t, newSource(t, fixture(t, "growatt-124-charging.json"), on))
	if got.Watt == nil || *got.Watt != 350 {
		t.Errorf("watt = %v, want 350 (import positive)", deref(got.Watt))
	}

	body := []byte(`{"OutputPower":1500,"ACPowerToUser":0,"ACPowerToGrid":900}`)
	got = read(t, newSource(t, body, on))
	if got.Watt == nil || *got.Watt != -900 {
		t.Errorf("watt = %v, want -900 (export negative)", deref(got.Watt))
	}
}

func TestImplausibleGridValueLeavesWattNil(t *testing.T) {
	// watts() guards producingWatt already; the grid register must go through
	// the same sanity bound instead of saturating int(math.Round(...)) at the
	// edge of the integer range.
	on := func(d *config.Device) { d.ReportGrid = true }
	body := []byte(`{"OutputPower":500,"ACPowerToUser":1e300,"ACPowerToGrid":0,"ACPowerToUserTotal":100,"ACPowerToGridTotal":0}`)
	got := read(t, newSource(t, body, on))

	if got.Watt != nil {
		t.Errorf("watt = %d, want nil for an implausible grid register", *got.Watt)
	}
}

func TestImplausibleChargeValueLeavesChargingPowerNil(t *testing.T) {
	// Same sanity bound applies to the battery charge/discharge magnitude:
	// a corrupt register must not saturate into a huge integer.
	body := []byte(`{"OutputPower":500,"BatteryVoltage":51.2,"SOC":50,"ChargePower":1e300,"DischargePower":0}`)
	got := read(t, newSource(t, body, nil))

	if got.ChargingPower != nil {
		t.Errorf("chargingPower = %d, want nil for an implausible charge register", *got.ChargingPower)
	}
	if got.PowerStorageState != nil {
		t.Errorf("powerStorageState = %d, want nil for an implausible charge register", *got.PowerStorageState)
	}
	// SOC, mapped before the charge register is read, must still stand.
	if got.SOC == nil || *got.SOC != 50 {
		t.Errorf("soc = %v, want 50", deref(got.SOC))
	}
}

func TestSOCClampsToValidRange(t *testing.T) {
	// A register that briefly overshoots must clamp rather than report an
	// impossible state of charge.
	over := []byte(`{"OutputPower":500,"BatteryVoltage":51.2,"SOC":120,"ChargePower":0,"DischargePower":0}`)
	got := read(t, newSource(t, over, nil))
	if got.SOC == nil || *got.SOC != 100 {
		t.Errorf("soc = %v, want 100 (clamped from 120)", deref(got.SOC))
	}

	under := []byte(`{"OutputPower":500,"BatteryVoltage":51.2,"SOC":-5,"ChargePower":0,"DischargePower":0}`)
	got = read(t, newSource(t, under, nil))
	if got.SOC == nil || *got.SOC != 0 {
		t.Errorf("soc = %v, want 0 (clamped from -5)", deref(got.SOC))
	}
}

func TestBatteryOffSuppressesDetectedBattery(t *testing.T) {
	off := func(d *config.Device) { d.Battery = "off" }
	got := read(t, newSource(t, fixture(t, "growatt-124-charging.json"), off))
	if got.SOC != nil || got.ChargingPower != nil || got.PowerStorageState != nil {
		t.Error("battery \"off\" must suppress all battery fields")
	}
}

func TestBatteryOnForcesZeroVoltagePack(t *testing.T) {
	on := func(d *config.Device) { d.Battery = "on" }
	got := read(t, newSource(t, fixture(t, "growatt-124-no-battery.json"), on))
	if got.SOC == nil || *got.SOC != 0 {
		t.Errorf("battery \"on\" should map soc even at zero voltage, got %v", deref(got.SOC))
	}
}

func TestProtocol305HasNoBatteryFields(t *testing.T) {
	got := read(t, newSource(t, fixture(t, "growatt-305.json"), nil))
	if got.ProducingWatt == nil || *got.ProducingWatt != 513 {
		t.Errorf("producingWatt = %v, want 513 (512.7 rounded)", deref(got.ProducingWatt))
	}
	if got.SOC != nil || got.ChargingPower != nil {
		t.Error("protocol 3.05 has no battery registers; fields must be nil")
	}
}

func TestDetectPrefersMostDistinctiveMarker(t *testing.T) {
	// TL-XH also uses OutputPower, so the BDC marker has to win.
	body := []byte(`{"OutputPower":2400,"BDCStateOfCharge":77,"BDCChargePower":500,"BDCDischargePower":0,"BDCBatteryVoltage":102.4}`)
	got := read(t, newSource(t, body, nil))
	if got.SOC == nil || *got.SOC != 77 {
		t.Errorf("soc = %v, want 77 from the TL-XH mapping", deref(got.SOC))
	}
}

func TestSPFMapping(t *testing.T) {
	body := []byte(`{"OutActivePwr":1430.6,"BattSOC":88,"BattPwr":430,"BattVoltage":53.1}`)
	got := read(t, newSource(t, body, nil))
	if got.ProducingWatt == nil || *got.ProducingWatt != 1431 {
		t.Errorf("producingWatt = %v, want 1431", deref(got.ProducingWatt))
	}
	if got.SOC == nil || *got.SOC != 88 {
		t.Errorf("soc = %v, want 88", deref(got.SOC))
	}
}

func TestSPFSignedBatteryRegisterDischarging(t *testing.T) {
	// SPF reports one signed register: negative means the battery is supplying
	// the house. Reporting that as idle would hide a discharge.
	body := []byte(`{"OutActivePwr":800,"BattSOC":55,"BattPwr":-430,"BattVoltage":52.4}`)
	got := read(t, newSource(t, body, nil))

	if got.PowerStorageState == nil || *got.PowerStorageState != 3 {
		t.Errorf("powerStorageState = %v, want 3 (discharging)", deref(got.PowerStorageState))
	}
	if got.ChargingPower == nil || *got.ChargingPower != 430 {
		t.Errorf("chargingPower = %v, want 430 as a positive magnitude", deref(got.ChargingPower))
	}
}

func TestReportGridSuppressedWhenNoMeterAttached(t *testing.T) {
	// The real no-battery capture has the meter registers present but at zero,
	// and lifetime totals at zero, because no meter is wired in. Switching
	// report_grid on must not invent a balanced grid.
	on := func(d *config.Device) { d.ReportGrid = true }
	got := read(t, newSource(t, fixture(t, "growatt-124-no-battery.json"), on))

	if got.Watt != nil {
		t.Errorf("watt = %d, want nil: the registers exist but no meter is attached", *got.Watt)
	}
}

// Review Focus: a gateway serving a reboot or captive-portal page.
func TestNonJSONBodyIsDiagnosable(t *testing.T) {
	s := newSource(t, []byte("<html><body>Rebooting</body></html>"), nil)
	_, err := s.Read(context.Background())
	if err == nil {
		t.Fatal("want an error for an HTML body")
	}
	if !strings.Contains(err.Error(), "not valid JSON") {
		t.Errorf("error should say the body was not JSON, got %v", err)
	}
}

// Review Focus: unknown protocol must name what it saw.
func TestUnknownProtocolListsKeys(t *testing.T) {
	s := newSource(t, []byte(`{"SomeOtherPower":12,"Zebra":1}`), nil)
	_, err := s.Read(context.Background())
	if err == nil {
		t.Fatal("want an error for an unrecognised response")
	}
	for _, want := range []string{"SomeOtherPower", "Zebra"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should list the keys found, missing %q: %v", want, err)
		}
	}
}

// Review Focus: HTTP errors must not be read as empty readings.
func TestHTTPErrorStatus(t *testing.T) {
	for _, code := range []int{401, 404, 500} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "nope", code)
		}))
		d := config.Device{Name: "test", URL: srv.URL, Battery: "auto"}
		_, err := New(d).Read(context.Background())
		srv.Close()
		if err == nil || !strings.Contains(err.Error(), http.StatusText(code)) {
			t.Errorf("status %d: want an error naming the status, got %v", code, err)
		}
	}
}

// Review Focus: negative and absurd power values.
func TestImplausibleValuesRejected(t *testing.T) {
	// Small negatives at night clamp to zero rather than erroring.
	got := read(t, newSource(t, []byte(`{"OutputPower":-3.2}`), nil))
	if got.ProducingWatt == nil || *got.ProducingWatt != 0 {
		t.Errorf("producingWatt = %v, want 0 for a small negative", deref(got.ProducingWatt))
	}

	// A corrupt register must not be forwarded as a real reading.
	s := newSource(t, []byte(`{"OutputPower":1e12}`), nil)
	_, err := s.Read(context.Background())
	if err == nil || !strings.Contains(err.Error(), "implausible") {
		t.Errorf("want an implausible-value error, got %v", err)
	}
}

func TestBasicAuthSentWhenConfigured(t *testing.T) {
	var gotUser, gotPass string
	var gotOK bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser, gotPass, gotOK = r.BasicAuth()
		w.Write([]byte(`{"OutputPower":100}`))
	}))
	defer srv.Close()

	d := config.Device{Name: "test", URL: srv.URL, Battery: "auto", Username: "admin", Password: "secret"}
	if _, err := New(d).Read(context.Background()); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !gotOK || gotUser != "admin" || gotPass != "secret" {
		t.Errorf("basic auth = %q/%q ok=%v", gotUser, gotPass, gotOK)
	}
}

func TestNoAuthHeaderWhenUsernameEmpty(t *testing.T) {
	var sawAuth bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _, sawAuth = r.BasicAuth()
		w.Write([]byte(`{"OutputPower":100}`))
	}))
	defer srv.Close()

	if _, err := New(config.Device{Name: "t", URL: srv.URL, Battery: "auto"}).Read(context.Background()); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if sawAuth {
		t.Error("no Authorization header should be sent when no username is configured")
	}
}

func TestTrailingSlashInURL(t *testing.T) {
	// A person will paste "http://10.0.0.240/" sooner or later.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/status" {
			t.Errorf("path = %q, want /status", r.URL.Path)
		}
		w.Write([]byte(`{"OutputPower":100}`))
	}))
	defer srv.Close()

	if _, err := New(config.Device{Name: "t", URL: srv.URL + "/", Battery: "auto"}).Read(context.Background()); err != nil {
		t.Fatalf("Read: %v", err)
	}
}

// Helpers used only by these tests.

func deref(p *int) any {
	if p == nil {
		return "nil"
	}
	return *p
}
