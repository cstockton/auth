// Package reloader provides support for live configuration reloading.
package reloader

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/sirupsen/logrus"
	"github.com/supabase/auth/internal/conf"
)

const (
	// reloadInterval is the interval between configuration reloading. At most
	// one configuration change may be made between this duration.
	reloadInterval = time.Second * 10

	// tickerInterval is the maximum latency between configuration reloads.
	tickerInterval = reloadInterval / 10
)

type ConfigFunc func(*conf.GlobalConfiguration)

type Reloader struct {
	watchDir   string
	reloadIval time.Duration
	tickerIval time.Duration
}

func NewReloader(watchDir string) *Reloader {
	return &Reloader{
		watchDir:   watchDir,
		reloadIval: reloadInterval,
		tickerIval: tickerInterval,
	}
}

// reloadConfig will reload the configuration files located in the watchDir. It
// uses ReadDir which sorts by filename and then filters out items without the
// .env suffix before calling conf.LoadGlobalFiles.
func (rl *Reloader) reloadConfig() (*conf.GlobalConfiguration, error) {

	// Returns entries sorted by filename
	ents, err := os.ReadDir(rl.watchDir)
	if err != nil {
		return nil, err
	}

	var paths []string
	for _, ent := range ents {
		if ent.IsDir() {
			continue // ignore directories
		}

		// We only read files ending in .env
		name := ent.Name()
		if !strings.HasSuffix(name, ".env") {
			continue
		}

		// ent.Name() does not include the watch dir.
		paths = append(paths, filepath.Join(rl.watchDir, name))
	}

	// Parse the configuration files in the directory together.
	cfg, err := conf.LoadGlobalFiles(paths...)
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

// reloadCheckAt checks if reloadConfig should be called, returns true if config
// should be reloaded or false otherwise.
func (rl *Reloader) reloadCheckAt(at, lastUpdate time.Time) bool {
	if lastUpdate.IsZero() {
		return false // no pending updates
	}
	if at.Sub(lastUpdate) < rl.reloadIval {
		return false // waiting for reload interval
	}

	// Update is pending.
	return true
}

func (rl *Reloader) Watch(ctx context.Context, fn ConfigFunc) error {
	wr, err := fsnotify.NewWatcher()
	if err != nil {
		log.Fatal(err)
	}
	defer wr.Close()

	tr := time.NewTicker(rl.tickerIval)
	defer tr.Stop()

	// Ignore errors, if watch dir doesn't exist we can add it later.
	if err := wr.Add(rl.watchDir); err != nil {
		logrus.WithError(err).Error("watch dir failed")
	}

	var lastUpdate time.Time
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case <-tr.C:
			// This is a simple way to solve watch dir being added later or
			// being moved and then recreated. I've tested all of these basic
			// scenarios and wr.WatchList() does not grow which aligns with
			// the documented behavior.
			if err := wr.Add(rl.watchDir); err != nil {
				logrus.WithError(err).Error("watch dir failed")
			}

			// Check to see if the config is ready to be relaoded.
			if !rl.reloadCheckAt(time.Now(), lastUpdate) {
				continue
			}

			// Reset the last update time before we try to reload the config.
			lastUpdate = time.Time{}

			cfg, err := rl.reloadConfig()
			if err != nil {
				logrus.WithError(err).Error("config reload failed")
				continue
			}

			// Call the callback function with the latest cfg.
			fn(cfg)

		case evt, ok := <-wr.Events:
			if !ok {
				logrus.WithError(err).Error("fsnotify has exited")
				return nil
			}

			// We only read files ending in .env
			if !strings.HasSuffix(evt.Name, ".env") {
				continue
			}

			switch {
			case evt.Op.Has(fsnotify.Create),
				evt.Op.Has(fsnotify.Remove),
				evt.Op.Has(fsnotify.Rename),
				evt.Op.Has(fsnotify.Write):
				lastUpdate = time.Now()
			}
		case err, ok := <-wr.Errors:
			if !ok {
				logrus.Error("fsnotify has exited")
				return nil
			}
			logrus.WithError(err).Error("fsnotify has reported an error")
		}
	}
}
