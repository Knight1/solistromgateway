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
	TypeOpenDTU   = "hoymiles-opendtu"
	TypeAPsystems = "apsystems-local"
)

var plannedTypes = []string{TypeOpenDTU, TypeAPsystems}

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
	ReportGrid bool     `json:"report_grid"`
	Retry      Retry    `json:"retry"`
}

// Config is the whole file.
type Config struct {
	LogLevel string   `json:"log_level"`
	Devices  []Device `json:"devices"`
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
