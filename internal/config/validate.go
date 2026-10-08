package config

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"time"
)

// minInterval comes from Solistrom's guidance to resend values only every
// 5 to 10 seconds.
const minInterval = 5 * time.Second

// warnInterval is the point above which data looks stale in the app. Not an
// error; main logs a warning.
const warnInterval = 60 * time.Second

func (c *Config) validate() error {
	var errs []error

	if len(c.Devices) == 0 {
		errs = append(errs, fmt.Errorf("devices: at least one device is required"))
	}

	seen := make(map[string]bool, len(c.Devices))
	for i, d := range c.Devices {
		// Identify the device by name if it has one, by position if not.
		label := fmt.Sprintf("devices[%d]", i)
		if d.Name != "" {
			label = fmt.Sprintf("device %q", d.Name)
		}

		if d.Name == "" {
			errs = append(errs, fmt.Errorf("%s: name is required", label))
		} else if seen[d.Name] {
			errs = append(errs, fmt.Errorf("%s: duplicate device name", label))
		}
		seen[d.Name] = true

		errs = append(errs, validateType(label, d.Type)...)
		errs = append(errs, validateConnection(label, d.Connection)...)
		errs = append(errs, validateURLs(label, d)...)
		errs = append(errs, validateTiming(label, d)...)

		if d.Battery != "auto" && d.Battery != "on" && d.Battery != "off" {
			errs = append(errs, fmt.Errorf("%s: battery must be \"auto\", \"on\" or \"off\", got %q", label, d.Battery))
		}
		if d.Retry.AttemptCount() < 1 {
			errs = append(errs, fmt.Errorf("%s: retry.attempts must be at least 1", label))
		}
		if d.Retry.Backoff.d <= 0 {
			errs = append(errs, fmt.Errorf("%s: retry.backoff must be greater than zero", label))
		}
	}
	return errors.Join(errs...)
}

func validateType(label, typ string) []error {
	switch {
	case typ == "":
		return []error{fmt.Errorf("%s: type is required", label)}
	case typ == TypeGrowatt:
		return nil
	case slices.Contains(plannedTypes, typ):
		return []error{fmt.Errorf("%s: device type %q is not implemented yet; only %q is supported", label, typ, TypeGrowatt)}
	default:
		return []error{fmt.Errorf("%s: unknown device type %q; supported: %s", label, typ, TypeGrowatt)}
	}
}

func validateConnection(label, conn string) []error {
	switch conn {
	case "http":
		return nil
	case "mqtt":
		return []error{fmt.Errorf("%s: connection \"mqtt\" is not implemented yet; use \"http\"", label)}
	default:
		return []error{fmt.Errorf("%s: unknown connection %q; supported: \"http\"", label, conn)}
	}
}

func validateURLs(label string, d Device) []error {
	var errs []error

	if d.URL == "" {
		errs = append(errs, fmt.Errorf("%s: url is required (the device address, for example http://10.0.0.240)", label))
	} else if u, err := url.Parse(d.URL); err != nil || u.Host == "" {
		errs = append(errs, fmt.Errorf("%s: url %q is not a valid address", label, d.URL))
	} else if u.Scheme != "http" && u.Scheme != "https" {
		errs = append(errs, fmt.Errorf("%s: url %q must start with http:// or https://", label, d.URL))
	}

	switch {
	case d.PushURL == "":
		errs = append(errs, fmt.Errorf("%s: push_url is required (copy it from the Solistrom app, or set %s)", label, envVarName(d.Name)))
	default:
		u, err := url.Parse(d.PushURL)
		switch {
		case err != nil || u.Host == "":
			errs = append(errs, fmt.Errorf("%s: push_url is not a valid URL", label))
		case u.Scheme != "https":
			// The URL carries an API key; sending it in clear text would leak it.
			errs = append(errs, fmt.Errorf("%s: push_url must use https, got %q", label, u.Scheme))
		}
	}
	return errs
}

func validateTiming(label string, d Device) []error {
	var errs []error
	if d.Interval.d < minInterval {
		errs = append(errs, fmt.Errorf("%s: interval must be at least 5s, got %v", label, d.Interval.d))
	}
	if d.Timeout.d <= 0 {
		errs = append(errs, fmt.Errorf("%s: timeout must be greater than zero", label))
	} else if d.Timeout.d >= d.Interval.d {
		errs = append(errs, fmt.Errorf("%s: timeout (%v) must be less than interval (%v)", label, d.Timeout.d, d.Interval.d))
	}
	return errs
}

// IntervalIsSlow reports whether an interval is legal but slower than the
// Solistrom app expects. main logs this rather than refusing to start.
func (d Device) IntervalIsSlow() bool { return d.Interval.d > warnInterval }
