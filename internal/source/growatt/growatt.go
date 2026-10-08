// Package growatt reads a Growatt inverter through an OpenInverterGateway
// datalogger: https://github.com/OpenInverterGateway/OpenInverterGateway
package growatt

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"sync"

	"github.com/Knight1/solistromgateway/internal/config"
	"github.com/Knight1/solistromgateway/internal/source"
)

// Source must satisfy the interface the runner calls.
var _ source.Source = (*Source)(nil)

// maxWatts is a sanity bound. Domestic inverters are orders of magnitude below
// this, so anything larger is a corrupt register rather than a reading.
const maxWatts = 1_000_000

// maxBodyBytes caps how much of a response we will read. The real document is
// around 1.5 kB; anything far larger is not a /status response.
const maxBodyBytes = 1 << 20

// Source reads one Growatt gateway over HTTP.
type Source struct {
	name       string
	statusURL  string
	username   string
	password   string
	battery    string
	reportGrid bool
	client     *http.Client

	warnBatteryOnce sync.Once
	warnGridOnce    sync.Once
}

// New builds a Source from a validated device config.
func New(d config.Device) *Source {
	return &Source{
		name:       d.Name,
		statusURL:  strings.TrimRight(d.URL, "/") + "/status",
		username:   d.Username,
		password:   d.Password,
		battery:    d.Battery,
		reportGrid: d.ReportGrid,
		client:     &http.Client{},
	}
}

// Name returns the configured device name.
func (s *Source) Name() string { return s.name }

// Read fetches /status and maps it to a Reading.
func (s *Source) Read(ctx context.Context) (source.Reading, error) {
	doc, err := s.fetch(ctx)
	if err != nil {
		return source.Reading{}, err
	}
	return s.mapReading(doc)
}

func (s *Source) fetch(ctx context.Context) (statusDoc, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.statusURL, nil)
	if err != nil {
		return nil, fmt.Errorf("building request for %s: %w", s.statusURL, err)
	}
	req.Header.Set("Accept", "application/json")
	if s.username != "" {
		req.SetBasicAuth(s.username, s.password)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("requesting %s: %w", s.statusURL, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("reading response from %s: %w", s.statusURL, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s returned %d %s", s.statusURL, resp.StatusCode, http.StatusText(resp.StatusCode))
	}

	var doc statusDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("response from %s is not valid JSON (%s): %w", s.statusURL, snippet(body), err)
	}
	return doc, nil
}

// snippet gives a short, single-line excerpt of a body for error messages, so
// a captive portal or reboot page is recognisable in the log.
func snippet(b []byte) string {
	const limit = 60
	s := strings.Join(strings.Fields(string(b)), " ")
	if len(s) > limit {
		s = s[:limit] + "..."
	}
	return s
}

func (s *Source) mapReading(doc statusDoc) (source.Reading, error) {
	p, err := detect(doc)
	if err != nil {
		return source.Reading{}, err
	}

	var r source.Reading

	if v, ok := doc.number(p.production); ok {
		w, err := watts(v)
		if err != nil {
			return source.Reading{}, fmt.Errorf("%s: %w", p.production, err)
		}
		// Inverters report small negatives overnight; that is zero production.
		r.ProducingWatt = source.Int(max(w, 0))
	}

	if s.batteryWanted(doc, p) {
		s.mapBattery(doc, p, &r)
	}

	if s.reportGrid {
		s.mapGrid(doc, p, &r)
	}

	return r, nil
}

// batteryWanted decides whether the battery fields describe real hardware.
func (s *Source) batteryWanted(doc statusDoc, p protocol) bool {
	switch s.battery {
	case "off":
		return false
	case "on":
		if p.soc == "" {
			s.warnBatteryOnce.Do(func() {
				slog.Warn("battery is set to \"on\" but this protocol has no battery registers; battery fields stay empty",
					"device", s.name, "protocol", p.name)
			})
			return false
		}
		return true
	default: // "auto"
		v, ok := doc.number(p.batteryVoltage)
		return ok && v > 0
	}
}

func (s *Source) mapBattery(doc statusDoc, p protocol, r *source.Reading) {
	if v, ok := doc.number(p.soc); ok {
		// Bound the float before converting. An out-of-range float-to-int
		// conversion is implementation-defined in Go, and while it happens to
		// saturate into range on the platforms this runs on, that is luck
		// rather than a rule worth depending on.
		r.SOC = source.Int(int(math.Round(min(max(v, 0), 100))))
	}

	charge, okCharge := doc.number(p.chargePower)
	discharge, okDischarge := doc.number(p.dischargePower)
	if !okCharge && !okDischarge {
		// No power register reported anything, so neither the direction nor the
		// magnitude is known. Claiming 0 and idle would assert a fact we do not
		// have. Any SOC mapped above still stands.
		return
	}

	// SPF exposes one signed register instead of separate charge and discharge
	// registers: negative means discharging. Fold that into the same shape the
	// switch below expects. SPF is not verified against hardware, so if a unit
	// turns out to report discharge differently this is the place to correct.
	if p.dischargePower == "" && charge < 0 {
		charge, discharge = 0, -charge
	}

	// Solistrom wants a positive magnitude plus a separate direction.
	// The raw BatteryState register is ignored: its encoding is undocumented,
	// so the direction is derived from which power register is non-zero.
	switch {
	case charge > 0:
		w, err := watts(charge)
		if err != nil {
			slog.Warn("ignoring implausible charge reading", "device", s.name, "error", err)
			return
		}
		r.ChargingPower = source.Int(w)
		r.PowerStorageState = source.Int(1) // charging
	case discharge > 0:
		w, err := watts(discharge)
		if err != nil {
			slog.Warn("ignoring implausible discharge reading", "device", s.name, "error", err)
			return
		}
		r.ChargingPower = source.Int(w)
		r.PowerStorageState = source.Int(3) // discharging
	default:
		r.ChargingPower = source.Int(0)
		r.PowerStorageState = source.Int(0) // idle
	}
}

func (s *Source) mapGrid(doc statusDoc, p protocol, r *source.Reading) {
	toUser, okUser := doc.number(p.gridToUser)
	toGrid, okGrid := doc.number(p.gridToGrid)
	if !okUser && !okGrid {
		s.warnGridOnce.Do(func() {
			slog.Warn("report_grid is on but this protocol has no meter registers; watt stays empty",
				"device", s.name, "protocol", p.name)
		})
		return
	}

	if !s.meterPresent(doc, p) {
		s.warnGridOnce.Do(func() {
			slog.Warn("report_grid is on but the meter registers have never recorded any flow; no meter appears to be attached, so watt stays empty",
				"device", s.name, "protocol", p.name)
		})
		return
	}

	// Positive means importing, negative means exporting.
	w, err := watts(toUser - toGrid)
	if err != nil {
		slog.Warn("ignoring implausible grid reading", "device", s.name, "error", err)
		return
	}
	r.Watt = source.Int(w)
}

// meterPresent reports whether the grid registers describe a real meter.
//
// A 1.24 inverter exposes the meter registers whether or not a meter is wired
// in, reading 0 for both. The lifetime totals settle it: a real meter
// accumulates them within minutes of operation, so both still at zero means
// there is nothing attached. Emitting watt: 0 in that case would tell the
// account the grid is balanced and contradict the meter it already reads.
func (s *Source) meterPresent(doc statusDoc, p protocol) bool {
	totalUser, okUser := doc.number(p.gridTotalToUser)
	totalGrid, okGrid := doc.number(p.gridTotalToGrid)
	if !okUser && !okGrid {
		// This protocol has no lifetime totals, so there is nothing to check.
		// Trust that switching report_grid on was deliberate.
		return true
	}
	return totalUser > 0 || totalGrid > 0
}

// watts converts a raw register value to whole watts, rejecting values that
// cannot be a real reading.
func watts(v float64) (int, error) {
	if math.IsNaN(v) || math.IsInf(v, 0) || math.Abs(v) > maxWatts {
		return 0, fmt.Errorf("implausible value %g", v)
	}
	return int(math.Round(v)), nil
}
