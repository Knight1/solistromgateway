// Command solistromgateway reads solar hardware on the local network and
// pushes its readings to Solistrom.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Knight1/solistromgateway/internal/config"
	"github.com/Knight1/solistromgateway/internal/push"
	"github.com/Knight1/solistromgateway/internal/runner"
	"github.com/Knight1/solistromgateway/internal/source"
	"github.com/Knight1/solistromgateway/internal/source/ahoydtu"
	"github.com/Knight1/solistromgateway/internal/source/apsystems"
	"github.com/Knight1/solistromgateway/internal/source/growatt"
)

// version is empty in an ordinary build. A tagged release sets it with
//
//	go build -ldflags "-X main.version=v1.2.3"
var version = ""

// programName is how the program introduces itself.
const programName = "solistromgateway"

// buildDetails describes which build is running and where, in a form somebody
// can paste into a bug report.
type buildDetails struct {
	Version   string
	Revision  string
	GoVersion string
	OS        string
	Arch      string
	CPUs      int
}

// details works out what to report about this build.
//
// Go stamps the revision, and whether the working tree was modified, into any
// binary built inside a version-controlled directory, and records the module
// version when the program was installed from a tag. Neither needs maintaining
// by hand, so the ldflags value only exists to override them.
func details(ldflagsVersion string, bi *debug.BuildInfo, ok bool) buildDetails {
	d := buildDetails{
		Version: ldflagsVersion,
		// Taken from the runtime rather than from the build settings, because
		// these describe the process that is actually running.
		OS:   runtime.GOOS,
		Arch: runtime.GOARCH,
		CPUs: runtime.NumCPU(),
	}

	if ok && bi != nil {
		d.GoVersion = bi.GoVersion
		if d.Version == "" {
			d.Version = bi.Main.Version
			// An untagged build gets a pseudo-version that repeats the
			// revision and the dirty flag. Saying "(devel)" instead keeps the
			// startup log line readable; the revision line below carries
			// everything that version string would have told us.
			if strings.HasPrefix(d.Version, "v0.0.0-") {
				d.Version = "(devel)"
			}
		}

		var revision string
		var modified bool
		for _, setting := range bi.Settings {
			switch setting.Key {
			case "vcs.revision":
				revision = setting.Value
			case "vcs.modified":
				modified = setting.Value == "true"
			}
		}
		if revision != "" {
			if len(revision) > 7 {
				revision = revision[:7]
			}
			d.Revision = revision
			if modified {
				d.Revision += " (modified)"
			}
		}
	}

	if d.Version == "" {
		d.Version = "unknown"
	}
	return d
}

// String renders the details over a few lines for the version command.
func (d buildDetails) String() string {
	out := programName + " " + d.Version
	if d.Revision != "" {
		out += "\nrevision " + d.Revision
	}
	if d.GoVersion != "" {
		out += "\nbuilt with " + d.GoVersion
	}
	out += fmt.Sprintf("\nrunning on %s/%s, %d CPU", d.OS, d.Arch, d.CPUs)
	if d.CPUs != 1 {
		out += "s"
	}
	return out
}

// startupFields are the log attributes for the first line of every run. The
// platform and CPU count matter because the same binary behaves differently on
// a single-core Raspberry Pi than on a desktop.
func (d buildDetails) startupFields() []any {
	fields := []any{"version", d.Version}
	if d.Revision != "" {
		fields = append(fields, "revision", d.Revision)
	}
	if d.GoVersion != "" {
		fields = append(fields, "go", d.GoVersion)
	}
	return append(fields,
		"platform", d.OS+"/"+d.Arch,
		"cpus", d.CPUs,
	)
}

// currentBuild describes the running binary.
func currentBuild() buildDetails {
	bi, ok := debug.ReadBuildInfo()
	return details(version, bi, ok)
}

// wantsVersion reports whether the version was asked for, either as a flag or
// as a bare command.
func wantsVersion(flagSet bool, args []string) bool {
	if flagSet {
		return true
	}
	return len(args) > 0 && args[0] == "version"
}

func main() {
	configPath := flag.String("config", "config.json", "path to the configuration file")
	showVersion := flag.Bool("version", false, "print version information and exit")
	flag.Parse()

	// Answered before the configuration is read, so it still works when the
	// configuration is broken, which is when it is most needed.
	if wantsVersion(*showVersion, flag.Args()) {
		fmt.Println(currentBuild())
		return
	}

	if err := run(*configPath); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(configPath string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: parseLogLevel(cfg.LogLevel)}))
	slog.SetDefault(log)

	devices, err := buildDevices(cfg)
	if err != nil {
		return err
	}

	// Who else can read the file that holds the push URL. Reported, never
	// fatal: a container has to be able to read it as its own uid.
	for _, concern := range config.PermissionConcerns(configPath) {
		log.Warn("configuration file permissions are wider than they need to be", "detail", concern)
	}

	for _, d := range cfg.Devices {
		for _, setting := range d.InapplicableSettings() {
			log.Warn("setting has no effect for this device type and is being ignored",
				"device", d.Name, "type", d.Type, "setting", setting)
		}
		if d.IntervalIsSlow() {
			log.Warn("interval is long; readings will look stale in the app",
				"device", d.Name, "interval", d.Interval.Duration())
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	startup := append(currentBuild().startupFields(), "devices", len(devices))
	log.Info("starting", startup...)

	// Say up front which devices are actually there. Unreachable ones are
	// reported, never fatal: after dark none of them answer.
	probeCtx, cancelProbe := context.WithTimeout(ctx, startupProbeBudget)
	logReachability(log, probeDevices(probeCtx, devices))
	cancelProbe()

	runner.Run(ctx, devices, push.New(), log)
	log.Info("stopped")
	return nil
}

// buildDevices turns validated config into runnable devices.
// buildDevices turns validated config into runnable devices.
// startupProbeBudget caps how long the reachability check may take, so a device
// configured with a long timeout cannot hold up the whole program.
const startupProbeBudget = 15 * time.Second

// probeResult is what the startup reachability check found for one device.
//
// Err and Empty are different things. Err means the device could not be read at
// all. Empty means it answered perfectly well but had nothing to report, which
// is what an inverter marked unavailable looks like, and is not a fault.
type probeResult struct {
	Name  string
	Err   error
	Empty bool
}

// probeDevices reads every device once so startup can say which ones answered.
//
// It reads them at the same time rather than one after another, so a few
// unreachable devices cost one timeout between them instead of one each. It
// never reports failure upward: every device is unreachable after dark, and
// that must not stop the program from running.
func probeDevices(ctx context.Context, devices []runner.Device) []probeResult {
	results := make([]probeResult, len(devices))

	var wg sync.WaitGroup
	for i, d := range devices {
		wg.Add(1)
		go func(i int, d runner.Device) {
			defer wg.Done()

			readCtx, cancel := context.WithTimeout(ctx, d.Timeout)
			defer cancel()

			reading, err := d.Source.Read(readCtx)
			results[i] = probeResult{Name: d.Name, Err: err, Empty: err == nil && reading.IsEmpty()}
		}(i, d)
	}
	wg.Wait()

	return results
}

// logReachability reports what the startup check found: a single line when
// everything answered, and the name and reason for anything that did not.
func logReachability(log *slog.Logger, results []probeResult) {
	var unreachable, quiet []string
	for _, r := range results {
		switch {
		case r.Err != nil:
			unreachable = append(unreachable, r.Name)
		case r.Empty:
			quiet = append(quiet, r.Name)
		}
	}

	reachable := len(results) - len(unreachable)

	if len(unreachable) == 0 {
		log.Info("all devices answered", "reachable", reachable)
	} else {
		log.Warn("some devices could not be reached",
			"reachable", reachable,
			"unreachable", len(unreachable),
			"not_answering", strings.Join(unreachable, ", "))
	}

	// The reason belongs with the device it applies to, since a wrong address
	// and a wrong inverter number look identical in a summary.
	for _, r := range results {
		if r.Err != nil {
			log.Warn("device did not answer the startup check", "device", r.Name, "error", r.Err)
		}
	}

	if len(quiet) > 0 {
		// Normal after dark, and normal for an inverter the datalogger has lost
		// contact with, so this is not raised as a problem.
		log.Info("devices answered but had nothing to report yet",
			"devices", strings.Join(quiet, ", "))
	}
}

func buildDevices(cfg *config.Config) ([]runner.Device, error) {
	devices := make([]runner.Device, 0, len(cfg.Devices))
	for _, d := range cfg.Devices {
		var src source.Source
		switch d.Type {
		case config.TypeGrowatt:
			src = growatt.New(d)
		case config.TypeAhoyDTU:
			src = ahoydtu.New(d)
		case config.TypeAPsystems:
			src = apsystems.New(d)
		default:
			return nil, fmt.Errorf("device %q: type %q cannot be built", d.Name, d.Type)
		}

		devices = append(devices, runner.Device{
			Name:     d.Name,
			Source:   src,
			PushURL:  d.PushURL,
			Interval: d.Interval.Duration(),
			Timeout:  d.Timeout.Duration(),
			Attempts: d.Retry.AttemptCount(),
			Backoff:  d.Retry.Backoff.Duration(),
		})
	}
	return devices, nil
}

func parseLogLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
