// Package ahoydtu reads a Hoymiles microinverter through an AhoyDTU
// datalogger: https://docs.ahoydtu.de
//
// AhoyDTU is not the same firmware as OpenDTU, which targets the same
// inverters. They have different APIs and are configured as different device
// types.
package ahoydtu

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"

	"github.com/Knight1/solistromgateway/internal/config"
	"github.com/Knight1/solistromgateway/internal/source"
)

// Source must satisfy the interface the runner calls.
var _ source.Source = (*Source)(nil)

// maxWatts is a sanity bound. A domestic microinverter is three orders of
// magnitude below this, so anything larger is a corrupt value.
const maxWatts = 1_000_000

// maxBodyBytes caps how much of a response we will read. The real document is
// under a kilobyte.
const maxBodyBytes = 1 << 20

// Source reads one inverter attached to one AhoyDTU datalogger.
type Source struct {
	name     string
	indexURL string
	inverter int
	client   *http.Client
}

// New builds a Source from a validated device config.
//
// It reads /api/index rather than /api/inverter/id/N for two reasons: the index
// reports power as a named field while the per-inverter endpoint returns
// positional arrays whose names live in a separate request, and the index
// carries no inverter serial number.
func New(d config.Device) *Source {
	return &Source{
		name:     d.Name,
		indexURL: strings.TrimRight(d.URL, "/") + "/api/index",
		inverter: d.Inverter(),
		client:   &http.Client{},
	}
}

// Name returns the configured device name.
func (s *Source) Name() string { return s.name }

// inverterEntry is one entry of the index's inverter list.
//
// CurPwr is a pointer so a null, which is the datalogger saying it has no
// value, stays distinguishable from a real reading of zero.
type inverterEntry struct {
	Enabled     bool     `json:"enabled"`
	ID          int      `json:"id"`
	Name        string   `json:"name"`
	CurPwr      *float64 `json:"cur_pwr"`
	IsAvail     bool     `json:"is_avail"`
	IsProducing bool     `json:"is_producing"`
}

type indexDoc struct {
	Inverters []inverterEntry `json:"inverter"`
}

// Read fetches the index and maps the configured inverter to a Reading.
func (s *Source) Read(ctx context.Context) (source.Reading, error) {
	doc, err := s.fetch(ctx)
	if err != nil {
		return source.Reading{}, err
	}

	iv, err := s.pick(doc)
	if err != nil {
		return source.Reading{}, err
	}

	if !iv.Enabled {
		return source.Reading{}, fmt.Errorf("inverter %d (%s) is disabled in AhoyDTU; enable it there or point this device at another inverter", iv.ID, iv.Name)
	}

	// An unavailable inverter means the datalogger has no current data for it.
	// That is not a reading of zero: the radio link to a microinverter can drop
	// in full daylight. Report nothing and let the caller skip the push.
	if !iv.IsAvail || iv.CurPwr == nil {
		return source.Reading{}, nil
	}

	w, err := watts(*iv.CurPwr)
	if err != nil {
		return source.Reading{}, fmt.Errorf("inverter %d (%s): %w", iv.ID, iv.Name, err)
	}
	// Microinverters report small negatives in near-darkness; that is no
	// production rather than negative production.
	return source.Reading{ProducingWatt: source.Int(max(w, 0))}, nil
}

// pick finds the configured inverter, naming what the datalogger does have if
// it is not there.
func (s *Source) pick(doc indexDoc) (inverterEntry, error) {
	for _, iv := range doc.Inverters {
		if iv.ID == s.inverter {
			return iv, nil
		}
	}

	if len(doc.Inverters) == 0 {
		return inverterEntry{}, fmt.Errorf("%s lists no inverters at all; check the datalogger is set up", s.indexURL)
	}
	available := make([]string, 0, len(doc.Inverters))
	for _, iv := range doc.Inverters {
		available = append(available, fmt.Sprintf("%d (%s)", iv.ID, iv.Name))
	}
	return inverterEntry{}, fmt.Errorf("no inverter with id %d; this datalogger has %s", s.inverter, strings.Join(available, ", "))
}

func (s *Source) fetch(ctx context.Context) (indexDoc, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.indexURL, nil)
	if err != nil {
		return indexDoc{}, fmt.Errorf("building request for %s: %w", s.indexURL, err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return indexDoc{}, fmt.Errorf("requesting %s: %w", s.indexURL, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return indexDoc{}, fmt.Errorf("reading response from %s: %w", s.indexURL, err)
	}
	if resp.StatusCode != http.StatusOK {
		return indexDoc{}, fmt.Errorf("%s returned %d %s", s.indexURL, resp.StatusCode, http.StatusText(resp.StatusCode))
	}

	var doc indexDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		return indexDoc{}, fmt.Errorf("response from %s is not valid JSON (%s): %w", s.indexURL, snippet(body), err)
	}
	return doc, nil
}

// snippet gives a short single-line excerpt of a body for error messages, so a
// reboot or login page is recognisable in the log.
func snippet(b []byte) string {
	const limit = 60
	s := strings.Join(strings.Fields(string(b)), " ")
	if len(s) > limit {
		s = s[:limit] + "..."
	}
	return s
}

// watts converts a reported power value to whole watts, rejecting anything that
// cannot be a real reading.
func watts(v float64) (int, error) {
	if math.IsNaN(v) || math.IsInf(v, 0) || math.Abs(v) > maxWatts {
		return 0, fmt.Errorf("implausible value %g", v)
	}
	return int(math.Round(v)), nil
}
