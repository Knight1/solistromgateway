// Package config reads and validates config.json.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// Device type names. Only TypeGrowatt is implemented; the others are
// recognised so an unimplemented device fails with a useful message rather
// than "unknown type".
const (
	TypeGrowatt   = "growatt-openinvertergateway"
	TypeAhoyDTU   = "hoymiles-ahoydtu"
	TypeOpenDTU   = "hoymiles-opendtu"
	TypeAPsystems = "apsystems-local"
)

// supportedTypes are the device types that are actually implemented.
var supportedTypes = []string{TypeGrowatt, TypeAhoyDTU, TypeAPsystems}

var plannedTypes = []string{TypeOpenDTU}

// Duration is a time.Duration that reads from JSON as a string such as "10s".
//
// It records whether it was present in the file at all, so an explicit "0s"
// stays distinguishable from a field nobody wrote. Without that, defaulting
// would overwrite a deliberate zero before validation could reject it.
type Duration struct {
	d   time.Duration
	set bool
}

// Duration returns the wrapped value.
func (x Duration) Duration() time.Duration { return x.d }

// IsSet reports whether the field was present in the file.
func (x Duration) IsSet() bool { return x.set }

func (x *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("invalid duration: expected a string such as \"10s\"")
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: expected a value such as \"10s\"", s)
	}
	x.d = d
	x.set = true
	return nil
}

// Retry controls how often a failed push is retried within one tick.
type Retry struct {
	// Attempts is a pointer so an explicit 0 in the file is distinguishable
	// from the field being absent: 0 is a configuration mistake worth
	// reporting, absent just means "use the default".
	Attempts *int     `json:"attempts"`
	Backoff  Duration `json:"backoff"`
}

// defaultAttempts is used when retry.attempts is absent from the file.
const defaultAttempts = 3

// AttemptCount returns how many times a push should be tried, falling back to
// the default when the file did not say. It is safe on a zero-valued Retry, so
// a Device built in code rather than parsed from JSON still works.
func (r Retry) AttemptCount() int {
	if r.Attempts == nil {
		return defaultAttempts
	}
	return *r.Attempts
}

// Device is one piece of hardware to read and push.
type Device struct {
	Name       string   `json:"name"`
	Type       string   `json:"type"`
	Connection string   `json:"connection"`
	URL        string   `json:"url"`
	Username   string   `json:"username"`
	Password   string   `json:"password"`
	PushURL    string   `json:"push_url"`
	Interval   Duration `json:"interval"`
	Timeout    Duration `json:"timeout"`
	Battery    string   `json:"battery"`
	// InverterID picks one inverter from a datalogger that serves several.
	// A pointer so an absent field is distinguishable from an explicit 0,
	// which matters for telling apart "did not say" from "said inverter 0".
	InverterID *int  `json:"inverter_id"`
	ReportGrid bool  `json:"report_grid"`
	Retry      Retry `json:"retry"`
}

// Inverter returns which inverter on the device to read, defaulting to the
// first. Safe on a Device built in code rather than parsed from JSON.
func (d Device) Inverter() int {
	if d.InverterID == nil {
		return 0
	}
	return *d.InverterID
}

// InapplicableSettings lists settings that are set on this device but do
// nothing for its type. They are not errors — a no-op setting is not wrong
// data — but somebody who set one is expecting an effect they will not get.
func (d Device) InapplicableSettings() []string {
	var out []string
	switch d.Type {
	case TypeGrowatt:
		if d.InverterID != nil {
			out = append(out, "inverter_id")
		}
	case TypeAhoyDTU:
		// A microinverter datalogger has no battery and no grid meter.
		if d.ReportGrid {
			out = append(out, "report_grid")
		}
		if d.Battery != "" && d.Battery != "auto" {
			out = append(out, "battery")
		}
	case TypeAPsystems:
		// One EZ1 is a single inverter with two panel inputs, which are summed,
		// so there is nothing to select. It has no battery and no grid meter.
		if d.InverterID != nil {
			out = append(out, "inverter_id")
		}
		if d.ReportGrid {
			out = append(out, "report_grid")
		}
		if d.Battery != "" && d.Battery != "auto" {
			out = append(out, "battery")
		}
	}
	return out
}

// Config is the whole file.
type Config struct {
	LogLevel string   `json:"log_level"`
	Devices  []Device `json:"devices"`
}

// PermissionConcerns reports anything worrying about who can read or write the
// config file.
//
// The file holds the push URL, and that URL is a credential: anyone with it can
// write readings into the owner's energy account. These are concerns rather
// than errors, and deliberately never stop the program. A container mounting
// this file has to make it readable by the uid inside the container, so a wider
// mode is sometimes the only workable choice.
//
// A file that cannot be inspected produces no concerns: Load already reports a
// missing or unreadable file properly.
func PermissionConcerns(path string) []string {
	info, err := os.Stat(path)
	if err != nil {
		return nil
	}

	mode := info.Mode().Perm()
	var concerns []string

	if mode&0o022 != 0 {
		concerns = append(concerns, fmt.Sprintf(
			"%s can be written by other users (mode %#o); somebody else could change where your readings are sent. Consider chmod 600.",
			path, mode))
	}
	if mode&0o044 != 0 {
		concerns = append(concerns, fmt.Sprintf(
			"%s can be read by other users (mode %#o); it holds your push URL, which is a credential. Consider chmod 600.",
			path, mode))
	}
	return concerns
}

// Load reads, defaults and validates the config file at path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config %s: %w", path, err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("config %s is not valid JSON: %w", path, err)
	}

	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("config %s:\n%w", path, err)
	}
	return &cfg, nil
}

func (c *Config) applyDefaults() {
	if c.LogLevel == "" {
		c.LogLevel = "info"
	}
	for i := range c.Devices {
		d := &c.Devices[i]
		if d.Connection == "" {
			d.Connection = "http"
		}
		if d.Battery == "" {
			d.Battery = "auto"
		}
		if !d.Interval.IsSet() {
			d.Interval.d = 10 * time.Second
		}
		if !d.Timeout.IsSet() {
			d.Timeout.d = 5 * time.Second
		}
		if !d.Retry.Backoff.IsSet() {
			d.Retry.Backoff.d = 2 * time.Second
		}
		if d.PushURL == "" {
			d.PushURL = os.Getenv(envVarName(d.Name))
		}
	}
}

// envVarName maps a device name to the environment variable that can supply
// its push URL, so the credential need not be written to the file.
func envVarName(device string) string {
	var b strings.Builder
	b.WriteString("SOLISTROM_PUSH_URL_")
	for _, r := range strings.ToUpper(device) {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}
