// Package apsystems reads an APsystems EZ1 microinverter through its local
// API, which the inverter serves on port 8050 without authentication.
package apsystems

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"strings"

	"github.com/Knight1/solistromgateway/internal/config"
	"github.com/Knight1/solistromgateway/internal/source"
)

// Source must satisfy the interface the runner calls.
var _ source.Source = (*Source)(nil)

// maxWatts is a sanity bound. A balcony microinverter is three orders of
// magnitude below this, so anything larger is a corrupt value.
const maxWatts = 1_000_000

// maxBodyBytes caps how much of a response we will read. The real document is
// around 130 bytes.
const maxBodyBytes = 1 << 20

// powerPort matches the per-input power fields, p1 and p2 on an EZ1. Summing
// whatever ports the device reports, rather than a fixed pair, means a model
// with more inputs cannot be silently understated.
var powerPort = regexp.MustCompile(`^p[0-9]+$`)

// Source reads one EZ1 inverter.
type Source struct {
	name    string
	dataURL string
	client  *http.Client
}

// New builds a Source from a validated device config. The port belongs in the
// configured URL, since the EZ1 serves its local API on 8050.
func New(d config.Device) *Source {
	return &Source{
		name:    d.Name,
		dataURL: strings.TrimRight(d.URL, "/") + "/getOutputData",
		client:  &http.Client{},
	}
}

// Name returns the configured device name.
func (s *Source) Name() string { return s.name }

// outputData is the response envelope. Values stay raw because the document
// mixes numbers with the serial string, and because a null has to stay
// distinguishable from a real zero.
type outputData struct {
	Message string                     `json:"message"`
	Data    map[string]json.RawMessage `json:"data"`
}

// Read fetches the current output and maps it to a Reading.
func (s *Source) Read(ctx context.Context) (source.Reading, error) {
	doc, err := s.fetch(ctx)
	if err != nil {
		return source.Reading{}, err
	}

	// The device says SUCCESS on every healthy response. Its failure wording is
	// not documented, so anything else is treated as a failure rather than
	// guessed at.
	if doc.Message != "SUCCESS" {
		return source.Reading{}, fmt.Errorf("%s reported %q rather than SUCCESS", s.dataURL, doc.Message)
	}

	total, ok := totalPower(doc.Data)
	if !ok {
		// Either no port reported a figure, or one of them reported null, which
		// is the device saying it has no value. Summing the rest would claim a
		// total we do not have.
		return source.Reading{}, nil
	}

	w, err := watts(total)
	if err != nil {
		return source.Reading{}, fmt.Errorf("%s: %w", s.dataURL, err)
	}
	// Microinverters report small negatives in near-darkness; that is no
	// production rather than negative production.
	return source.Reading{ProducingWatt: source.Int(max(w, 0))}, nil
}

// totalPower sums every per-port power field. It reports false if no port was
// present, or if any present port carried a value that is not a number, since
// a partial sum would understate the total without saying so.
func totalPower(data map[string]json.RawMessage) (float64, bool) {
	var total float64
	found := false
	for key, raw := range data {
		if !powerPort.MatchString(key) {
			continue
		}
		// A null is the device saying it has no figure for this port.
		// encoding/json accepts null into a float without error and leaves
		// zero, so it has to be caught explicitly or it becomes a silent
		// contribution of nothing to the total.
		if string(raw) == "null" {
			return 0, false
		}
		var v float64
		if err := json.Unmarshal(raw, &v); err != nil {
			return 0, false
		}
		total += v
		found = true
	}
	return total, found
}

func (s *Source) fetch(ctx context.Context) (outputData, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.dataURL, nil)
	if err != nil {
		return outputData{}, fmt.Errorf("building request for %s: %w", s.dataURL, err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return outputData{}, fmt.Errorf("requesting %s: %w", s.dataURL, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return outputData{}, fmt.Errorf("reading response from %s: %w", s.dataURL, err)
	}
	if resp.StatusCode != http.StatusOK {
		return outputData{}, fmt.Errorf("%s returned %d %s", s.dataURL, resp.StatusCode, http.StatusText(resp.StatusCode))
	}

	var doc outputData
	if err := json.Unmarshal(body, &doc); err != nil {
		return outputData{}, fmt.Errorf("response from %s is not valid JSON (%s): %w", s.dataURL, snippet(body), err)
	}
	return doc, nil
}

// snippet gives a short single-line excerpt of a body for error messages,
// withholding anything that could carry the device serial.
//
// This inverter returns its serial from every endpoint, and error messages end
// up in logs that the project's own troubleshooting notes invite people to paste
// into bug reports. Trying to find and replace the serial does not work: a
// truncated response carries it with no closing quote, and fuzzing found shapes
// where no pattern matches it at all. So rather than guess at removing it, any
// body mentioning the field is withheld outright.
//
// Nothing useful is lost. The bodies worth seeing do not mention it: a reboot
// page, a captive portal, or the plain-text "Nothing matches the given URI" this
// device returns for an unknown path.
//
// One residual case: a body carrying a bare serial with no field name around it
// would not be caught. This device does not produce that shape, since the serial
// only ever appears as the value of deviceId.
func snippet(b []byte) string {
	const limit = 60
	s := strings.Join(strings.Fields(string(b)), " ")

	if strings.Contains(strings.ToLower(s), "deviceid") {
		return fmt.Sprintf("body of %d bytes withheld because it mentions deviceId", len(b))
	}
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
