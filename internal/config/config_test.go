package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

const validConfig = `{
  "log_level": "info",
  "devices": [
    {
      "name": "growatt-pv",
      "type": "growatt-openinvertergateway",
      "connection": "http",
      "url": "http://10.0.0.240",
      "push_url": "https://push.example.com/api/v2/ACCOUNT/generic-push/DEVICE?code=KEY",
      "interval": "10s",
      "timeout": "5s",
      "battery": "auto",
      "report_grid": false,
      "retry": { "attempts": 3, "backoff": "2s" }
    }
  ]
}`

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func TestLoadValid(t *testing.T) {
	cfg, err := Load(writeConfig(t, validConfig))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Devices) != 1 {
		t.Fatalf("got %d devices, want 1", len(cfg.Devices))
	}
	d := cfg.Devices[0]
	if d.Name != "growatt-pv" {
		t.Errorf("name = %q", d.Name)
	}
	if d.Interval.Duration() != 10*time.Second {
		t.Errorf("interval = %v", d.Interval.Duration())
	}
	if d.Retry.Backoff.Duration() != 2*time.Second {
		t.Errorf("backoff = %v", d.Retry.Backoff.Duration())
	}
}

func TestLoadDefaults(t *testing.T) {
	// Only the fields a person must supply; everything else has a default.
	body := `{"devices":[{"name":"d","type":"growatt-openinvertergateway",
	  "url":"http://10.0.0.240",
	  "push_url":"https://push.example.com/p?code=KEY"}]}`
	cfg, err := Load(writeConfig(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	d := cfg.Devices[0]
	if d.Connection != "http" {
		t.Errorf("connection default = %q, want http", d.Connection)
	}
	if d.Interval.Duration() != 10*time.Second {
		t.Errorf("interval default = %v, want 10s", d.Interval.Duration())
	}
	if d.Timeout.Duration() != 5*time.Second {
		t.Errorf("timeout default = %v, want 5s", d.Timeout.Duration())
	}
	if d.Battery != "auto" {
		t.Errorf("battery default = %q, want auto", d.Battery)
	}
	if d.Retry.AttemptCount() != 3 {
		t.Errorf("attempts default = %d, want 3", d.Retry.AttemptCount())
	}
	if cfg.LogLevel != "info" {
		t.Errorf("log_level default = %q, want info", cfg.LogLevel)
	}
}

func TestAttemptsAbsentVersusExplicitZero(t *testing.T) {
	// Absent means "use the default"; an explicit 0 is a mistake worth
	// reporting, because zero attempts would never push anything at all.
	body := `{"devices":[{"name":"d","type":"growatt-openinvertergateway",
	  "url":"http://10.0.0.240","push_url":"https://push.example.com/p?code=KEY",
	  "retry":{"backoff":"2s"}}]}`
	cfg, err := Load(writeConfig(t, body))
	if err != nil {
		t.Fatalf("absent attempts should be valid: %v", err)
	}
	if got := cfg.Devices[0].Retry.AttemptCount(); got != 3 {
		t.Errorf("AttemptCount() = %d, want the default 3", got)
	}

	withZero := `{"devices":[{"name":"d","type":"growatt-openinvertergateway",
	  "url":"http://10.0.0.240","push_url":"https://push.example.com/p?code=KEY",
	  "retry":{"attempts":0,"backoff":"2s"}}]}`
	if _, err := Load(writeConfig(t, withZero)); err == nil {
		t.Error("an explicit attempts of 0 should be rejected")
	}
}

func TestExplicitZeroDurationsAreRejected(t *testing.T) {
	// A duration somebody deliberately wrote as "0s" is a mistake worth
	// reporting. Defaulting it silently would discard what they asked for.
	tests := []struct {
		name string
		body string
		want string
	}{
		{"interval", strings.Replace(validConfig, `"interval": "10s"`, `"interval": "0s"`, 1), "at least 5s"},
		{"timeout", strings.Replace(validConfig, `"timeout": "5s"`, `"timeout": "0s"`, 1), "timeout must be greater than zero"},
		{"backoff", strings.Replace(validConfig, `"backoff": "2s"`, `"backoff": "0s"`, 1), "retry.backoff must be greater than zero"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tt.body))
			if err == nil {
				t.Fatalf("an explicit 0s %s should be rejected", tt.name)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not contain %q", err, tt.want)
			}
		})
	}
}

func TestAbsentDurationsStillDefault(t *testing.T) {
	body := `{"devices":[{"name":"d","type":"growatt-openinvertergateway",
	  "url":"http://10.0.0.240","push_url":"https://push.example.com/p?code=KEY"}]}`
	cfg, err := Load(writeConfig(t, body))
	if err != nil {
		t.Fatalf("absent durations should be valid: %v", err)
	}
	d := cfg.Devices[0]
	if d.Interval.Duration() != 10*time.Second {
		t.Errorf("interval = %v, want the 10s default", d.Interval.Duration())
	}
	if d.Timeout.Duration() != 5*time.Second {
		t.Errorf("timeout = %v, want the 5s default", d.Timeout.Duration())
	}
	if d.Retry.Backoff.Duration() != 2*time.Second {
		t.Errorf("backoff = %v, want the 2s default", d.Retry.Backoff.Duration())
	}
}

func TestLoadErrors(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"no devices", `{"devices":[]}`, "at least one device"},
		{"missing name", strings.Replace(validConfig, `"name": "growatt-pv",`, "", 1), "name is required"},
		{"unknown type", strings.Replace(validConfig, "growatt-openinvertergateway", "nonsense-box", 1), "unknown device type"},
		{"planned type", strings.Replace(validConfig, "growatt-openinvertergateway", "hoymiles-opendtu", 1), "not implemented yet"},
		{"mqtt", strings.Replace(validConfig, `"connection": "http"`, `"connection": "mqtt"`, 1), "not implemented yet"},
		{"interval too short", strings.Replace(validConfig, `"interval": "10s"`, `"interval": "2s"`, 1), "at least 5s"},
		{"timeout not less than interval", strings.Replace(validConfig, `"timeout": "5s"`, `"timeout": "30s"`, 1), "less than interval"},
		{"bad duration", strings.Replace(validConfig, `"interval": "10s"`, `"interval": "ten"`, 1), "invalid duration"},
		{"http push url", strings.Replace(validConfig, "https://push.example.com", "http://push.example.com", 1), "must use https"},
		{"mqtt device url", strings.Replace(validConfig, `"url": "http://10.0.0.240",`, `"url": "mqtt://10.0.0.240",`, 1), "must start with http"},
		{"bad battery", strings.Replace(validConfig, `"battery": "auto"`, `"battery": "maybe"`, 1), "battery must be"},
		{"zero attempts", strings.Replace(validConfig, `"attempts": 3`, `"attempts": 0`, 1), "attempts must be at least 1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tt.body))
			if err == nil {
				t.Fatal("want an error, got nil")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not contain %q", err, tt.want)
			}
		})
	}
}

func TestLoadDuplicateNames(t *testing.T) {
	body := `{"devices":[
	  {"name":"same","type":"growatt-openinvertergateway","url":"http://a","push_url":"https://p.example.com/x?code=K"},
	  {"name":"same","type":"growatt-openinvertergateway","url":"http://b","push_url":"https://p.example.com/y?code=K"}]}`
	_, err := Load(writeConfig(t, body))
	if err == nil || !strings.Contains(err.Error(), "duplicate device name") {
		t.Fatalf("want duplicate name error, got %v", err)
	}
}

func TestLoadReportsEveryProblemAtOnce(t *testing.T) {
	// A person fixing their config should see all of it, not one error per run.
	body := `{"devices":[{"name":"","type":"nope","url":"","push_url":""}]}`
	_, err := Load(writeConfig(t, body))
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{"name is required", "unknown device type", "url is required", "push_url is required"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error is missing %q:\n%s", want, err)
		}
	}
}

// Review Focus: a missing or malformed config file must be diagnosable.
func TestLoadMissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "absent.json"))
	if err == nil || !strings.Contains(err.Error(), "absent.json") {
		t.Fatalf("error should name the file, got %v", err)
	}
}

func TestLoadMalformedJSON(t *testing.T) {
	_, err := Load(writeConfig(t, `{"devices": [`))
	if err == nil || !strings.Contains(err.Error(), "not valid JSON") {
		t.Fatalf("want a readable JSON error, got %v", err)
	}
}

func TestPushURLFromEnvironment(t *testing.T) {
	// Lets a person keep the credential out of the file entirely.
	body := strings.Replace(validConfig,
		`"push_url": "https://push.example.com/api/v2/ACCOUNT/generic-push/DEVICE?code=KEY",`, "", 1)
	t.Setenv("SOLISTROM_PUSH_URL_GROWATT_PV", "https://push.example.com/from-env?code=KEY")

	cfg, err := Load(writeConfig(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Devices[0].PushURL != "https://push.example.com/from-env?code=KEY" {
		t.Errorf("push_url = %q", cfg.Devices[0].PushURL)
	}
}

func TestEnvVarName(t *testing.T) {
	if got := envVarName("growatt-pv"); got != "SOLISTROM_PUSH_URL_GROWATT_PV" {
		t.Errorf("got %q", got)
	}
	if got := envVarName("roof.east 2"); got != "SOLISTROM_PUSH_URL_ROOF_EAST_2" {
		t.Errorf("got %q", got)
	}
}

const validAhoy = `{
  "devices": [
    {
      "name": "roof",
      "type": "hoymiles-ahoydtu",
      "url": "http://10.0.0.197",
      "push_url": "https://push.example.com/api/v2/ACCOUNT/generic-push/DEVICE?code=KEY",
      "interval": "15s",
      "timeout": "5s"
    }
  ]
}`

func TestAhoyDTUTypeIsAccepted(t *testing.T) {
	cfg, err := Load(writeConfig(t, validAhoy))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Devices[0].Type != TypeAhoyDTU {
		t.Errorf("type = %q, want %q", cfg.Devices[0].Type, TypeAhoyDTU)
	}
}

func TestInverterDefaultsToZero(t *testing.T) {
	// A single-inverter setup should not have to say which inverter it means.
	cfg, err := Load(writeConfig(t, validAhoy))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Devices[0].Inverter(); got != 0 {
		t.Errorf("Inverter() = %d, want 0 when the field is absent", got)
	}
}

func TestInverterIsReadWhenGiven(t *testing.T) {
	body := strings.Replace(validAhoy, `"interval": "15s"`, `"inverter_id": 2, "interval": "15s"`, 1)
	cfg, err := Load(writeConfig(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Devices[0].Inverter(); got != 2 {
		t.Errorf("Inverter() = %d, want 2", got)
	}
}

func TestNegativeInverterIsRejected(t *testing.T) {
	body := strings.Replace(validAhoy, `"interval": "15s"`, `"inverter_id": -1, "interval": "15s"`, 1)
	if _, err := Load(writeConfig(t, body)); err == nil ||
		!strings.Contains(err.Error(), "inverter_id must be zero or greater") {
		t.Fatalf("want a negative-inverter error, got %v", err)
	}
}

func TestSameInverterTwiceIsRejected(t *testing.T) {
	// Two entries reading the same inverter would report one inverter's
	// production as two devices, so the app would count it twice.
	body := `{"devices":[
	  {"name":"a","type":"hoymiles-ahoydtu","url":"http://10.0.0.197","push_url":"https://push.example.com/a?code=K"},
	  {"name":"b","type":"hoymiles-ahoydtu","url":"http://10.0.0.197","push_url":"https://push.example.com/b?code=K"}]}`
	_, err := Load(writeConfig(t, body))
	if err == nil {
		t.Fatal("want an error for two devices reading the same inverter")
	}
	for _, want := range []string{"\"a\"", "\"b\"", "inverter 0"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name %s:\n%v", want, err)
		}
	}
}

func TestSameInverterTwiceIsRejectedDespiteATrailingSlash(t *testing.T) {
	// The same device addressed two ways is still the same device.
	body := `{"devices":[
	  {"name":"a","type":"hoymiles-ahoydtu","url":"http://10.0.0.197","push_url":"https://push.example.com/a?code=K"},
	  {"name":"b","type":"hoymiles-ahoydtu","url":"http://10.0.0.197/","push_url":"https://push.example.com/b?code=K"}]}`
	if _, err := Load(writeConfig(t, body)); err == nil {
		t.Fatal("a trailing slash should not get past the duplicate check")
	}
}

func TestSameDTUDifferentInvertersIsAllowed(t *testing.T) {
	// The normal multi-inverter setup: one DTU, one entry per inverter.
	body := `{"devices":[
	  {"name":"a","type":"hoymiles-ahoydtu","url":"http://10.0.0.197","inverter_id":0,"push_url":"https://push.example.com/a?code=K"},
	  {"name":"b","type":"hoymiles-ahoydtu","url":"http://10.0.0.197","inverter_id":1,"push_url":"https://push.example.com/b?code=K"}]}`
	if _, err := Load(writeConfig(t, body)); err != nil {
		t.Fatalf("one entry per inverter should be valid: %v", err)
	}
}

func TestDifferentDevicesSameInverterIsAllowed(t *testing.T) {
	// Two separate DTUs each have an inverter 0.
	body := `{"devices":[
	  {"name":"a","type":"hoymiles-ahoydtu","url":"http://10.0.0.197","push_url":"https://push.example.com/a?code=K"},
	  {"name":"b","type":"hoymiles-ahoydtu","url":"http://10.0.0.198","push_url":"https://push.example.com/b?code=K"}]}`
	if _, err := Load(writeConfig(t, body)); err != nil {
		t.Fatalf("two different DTUs should be valid: %v", err)
	}
}

func TestInapplicableSettingsAreReported(t *testing.T) {
	// Settings that quietly do nothing are worth telling somebody about.
	ahoy := Device{Type: TypeAhoyDTU, ReportGrid: true, Battery: "on"}
	got := ahoy.InapplicableSettings()
	for _, want := range []string{"report_grid", "battery"} {
		if !slices.Contains(got, want) {
			t.Errorf("InapplicableSettings() = %v, want it to include %q", got, want)
		}
	}

	id := 1
	growatt := Device{Type: TypeGrowatt, Battery: "auto", InverterID: &id}
	if got := growatt.InapplicableSettings(); !slices.Contains(got, "inverter_id") {
		t.Errorf("InapplicableSettings() = %v, want it to include \"inverter_id\"", got)
	}
}

func TestNoInapplicableSettingsOnACleanDevice(t *testing.T) {
	d := Device{Type: TypeAhoyDTU, Battery: "auto"}
	if got := d.InapplicableSettings(); len(got) != 0 {
		t.Errorf("InapplicableSettings() = %v, want none", got)
	}
}

func TestAPsystemsTypeIsAccepted(t *testing.T) {
	body := strings.Replace(validAhoy, `"type": "hoymiles-ahoydtu"`, `"type": "apsystems-local"`, 1)
	body = strings.Replace(body, "http://10.0.0.197", "http://10.0.0.207:8050", 1)
	cfg, err := Load(writeConfig(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Devices[0].Type != TypeAPsystems {
		t.Errorf("type = %q, want %q", cfg.Devices[0].Type, TypeAPsystems)
	}
}

func TestAPsystemsPortInTheURLIsAccepted(t *testing.T) {
	// The EZ1 serves its local API on port 8050, so the port belongs in the URL
	// rather than in a setting of its own.
	body := `{"devices":[{"name":"balcony","type":"apsystems-local",
	  "url":"http://10.0.0.207:8050","push_url":"https://push.example.com/p?code=K"}]}`
	if _, err := Load(writeConfig(t, body)); err != nil {
		t.Fatalf("a URL with a port should be valid: %v", err)
	}
}

func TestAPsystemsInapplicableSettings(t *testing.T) {
	// One EZ1 is one device with two panel inputs, so there is no inverter to
	// select, and it has neither a battery nor a grid meter.
	id := 1
	d := Device{Type: TypeAPsystems, InverterID: &id, ReportGrid: true, Battery: "on"}
	got := d.InapplicableSettings()
	for _, want := range []string{"inverter_id", "report_grid", "battery"} {
		if !slices.Contains(got, want) {
			t.Errorf("InapplicableSettings() = %v, want it to include %q", got, want)
		}
	}
}
