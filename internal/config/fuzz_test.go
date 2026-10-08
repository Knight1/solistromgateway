package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// FuzzLoad drives the whole configuration pipeline: JSON decoding, duration
// parsing, defaulting and every validation rule.
func FuzzLoad(f *testing.F) {
	path := filepath.Join(f.TempDir(), "config.json")

	f.Add([]byte(validConfig))
	f.Add([]byte(validAhoy))
	f.Add([]byte(`{"devices":[]}`))
	f.Add([]byte(`{"devices":[{"retry":{"attempts":0}}]}`))
	f.Add([]byte(`{"devices":[{"interval":"0s","timeout":"-1s"}]}`))
	f.Add([]byte(`{"devices":[{"inverter_id":-9223372036854775808}]}`))
	f.Add([]byte(`{`))

	f.Fuzz(func(t *testing.T, data []byte) {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Skip()
		}

		cfg, err := Load(path)
		if err != nil {
			return
		}

		// Anything Load accepts has to satisfy what the rest of the program
		// assumes about it, or a later package will misbehave on valid input.
		for _, d := range cfg.Devices {
			if d.Name == "" {
				t.Fatal("accepted a device with no name")
			}
			if d.Interval.Duration() < minInterval {
				t.Fatalf("accepted interval %v, below the %v minimum", d.Interval.Duration(), minInterval)
			}
			if d.Timeout.Duration() <= 0 || d.Timeout.Duration() >= d.Interval.Duration() {
				t.Fatalf("accepted timeout %v against interval %v", d.Timeout.Duration(), d.Interval.Duration())
			}
			if d.Retry.AttemptCount() < 1 {
				t.Fatalf("accepted %d attempts", d.Retry.AttemptCount())
			}
			if d.Retry.Backoff.Duration() <= 0 {
				t.Fatalf("accepted backoff %v", d.Retry.Backoff.Duration())
			}
			if d.Inverter() < 0 {
				t.Fatalf("accepted inverter_id %d", d.Inverter())
			}
			if d.PushURL == "" {
				t.Fatal("accepted a device with no push URL")
			}
		}
	})
}

// FuzzDuration covers the custom duration unmarshaller on its own.
func FuzzDuration(f *testing.F) {
	f.Add([]byte(`"10s"`))
	f.Add([]byte(`"0s"`))
	f.Add([]byte(`"-1h"`))
	f.Add([]byte(`123`))
	f.Add([]byte(`"9223372036854775807ns"`))

	f.Fuzz(func(t *testing.T, data []byte) {
		var d Duration
		if d.UnmarshalJSON(data) != nil {
			// A failed parse must leave the value untouched, or a rejected field
			// could still influence defaulting.
			if d.IsSet() {
				t.Fatalf("UnmarshalJSON(%q) failed but marked the value as set", data)
			}
			return
		}
		if !d.IsSet() {
			t.Fatalf("UnmarshalJSON(%q) succeeded without marking the value as set", data)
		}
		_ = d.Duration() == time.Duration(0)
	})
}
