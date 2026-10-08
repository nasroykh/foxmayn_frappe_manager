package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/config"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/notify"
)

type notifyFile struct {
	Notifiers []notify.Notifier `json:"notifiers"`
}

// LoadNotifiers reads notify.json; a missing file is no notifiers.
func LoadNotifiers() ([]notify.Notifier, error) {
	raw, err := os.ReadFile(config.NotifyFile())
	if os.IsNotExist(err) {
		return []notify.Notifier{}, nil
	}
	if err != nil {
		return nil, err
	}
	var f notifyFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", config.NotifyFile(), err)
	}
	return f.Notifiers, nil
}

func saveNotifiers(ns []notify.Notifier) error {
	sort.Slice(ns, func(i, j int) bool { return ns[i].Name < ns[j].Name })
	raw, err := json.MarshalIndent(notifyFile{Notifiers: ns}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(config.NotifyFile()), 0o700); err != nil {
		return err
	}
	tmp := config.NotifyFile() + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, config.NotifyFile())
}

// AddNotifier saves a notifier; replace overwrites one of the same name.
func AddNotifier(n notify.Notifier, replace bool) error {
	if err := n.Validate(); err != nil {
		return err
	}
	ns, err := LoadNotifiers()
	if err != nil {
		return err
	}
	kept := ns[:0]
	for _, e := range ns {
		if e.Name == n.Name {
			if !replace {
				return fmt.Errorf("notifier %q exists (pass --replace)", n.Name)
			}
			continue
		}
		kept = append(kept, e)
	}
	return saveNotifiers(append(kept, n))
}

// RemoveNotifier forgets a notifier.
func RemoveNotifier(name string) error {
	ns, err := LoadNotifiers()
	if err != nil {
		return err
	}
	kept := ns[:0]
	for _, n := range ns {
		if n.Name != name {
			kept = append(kept, n)
		}
	}
	if len(kept) == len(ns) {
		return fmt.Errorf("no notifier %q", name)
	}
	return saveNotifiers(kept)
}

// TestNotifier sends a test event (as a success, and for healthchecks the
// start ping too) through one notifier.
func TestNotifier(name string) error {
	ns, err := LoadNotifiers()
	if err != nil {
		return err
	}
	for _, n := range ns {
		if n.Name == name {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := notify.Default.Start(ctx, n); err != nil {
				return err
			}
			host, _ := os.Hostname()
			return notify.Default.Send(ctx, n, notify.Event{Kind: "test", OK: true, Host: host, At: time.Now().UTC(),
				Message: "This is a test notification from ffm. Real ones report failed scheduled backups, verifies and doctor checks."})
		}
	}
	return fmt.Errorf("no notifier %q", name)
}

// Notify sends an event to every notifier that wants it. It never fails the
// caller: errors are returned joined, for a warning line.
func Notify(e notify.Event) error {
	ns, err := LoadNotifiers()
	if err != nil || len(ns) == 0 {
		return err
	}
	if e.Host == "" {
		e.Host, _ = os.Hostname()
	}
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	var errs []error
	for _, n := range ns {
		if !n.Wants(e) {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := notify.Default.Send(ctx, n, e); err != nil {
			errs = append(errs, fmt.Errorf("notifier %s: %w", n.Name, err))
		}
		cancel()
	}
	return errors.Join(errs...)
}

// notifyRunStart sends the healthchecks start ping before a scheduled run.
func notifyRunStart(log io.Writer) {
	ns, err := LoadNotifiers()
	if err != nil {
		fmt.Fprintf(log, "warning: notifiers: %v\n", err)
		return
	}
	for _, n := range ns {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		if err := notify.Default.Start(ctx, n); err != nil {
			fmt.Fprintf(log, "warning: notifier %s: %v\n", n.Name, err)
		}
		cancel()
	}
}

// notifyRunResult reports a scheduled run. A bench skipped because it is
// stopped or busy is not a failure: the next run catches up.
func notifyRunResult(bench string, res RunDueResult, log io.Writer) {
	e := notify.Event{Kind: "backup", Bench: bench, OK: res.Err == nil && res.Result != RunFailed}
	switch {
	case res.Result == RunOK:
		e.Message = "Scheduled backup written: " + res.Archive
	case res.Err != nil:
		e.Message = "Scheduled backup failed: " + res.Err.Error()
	default:
		e.Message = "Scheduled backup " + res.Result
	}
	if err := Notify(e); err != nil {
		fmt.Fprintf(log, "warning: %v\n", err)
	}
}
