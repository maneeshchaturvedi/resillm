package config

import (
	"context"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/rs/zerolog/log"
)

// Watcher watches a config file for changes and triggers reloads
type Watcher struct {
	path         string
	onChange     func(*Config) error
	watcher      *fsnotify.Watcher
	debounce     time.Duration
	lastModified time.Time

	mu      sync.Mutex
	running bool
}

// WatcherOption configures the watcher
type WatcherOption func(*Watcher)

// WithDebounce sets the debounce duration for rapid file changes
func WithDebounce(d time.Duration) WatcherOption {
	return func(w *Watcher) {
		w.debounce = d
	}
}

// NewWatcher creates a new config file watcher
func NewWatcher(path string, onChange func(*Config) error, opts ...WatcherOption) (*Watcher, error) {
	fsWatcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	w := &Watcher{
		path:     path,
		onChange: onChange,
		watcher:  fsWatcher,
		debounce: 500 * time.Millisecond, // Default debounce
	}

	for _, opt := range opts {
		opt(w)
	}

	return w, nil
}

// Start begins watching the config file for changes
func (w *Watcher) Start(ctx context.Context) error {
	w.mu.Lock()
	if w.running {
		w.mu.Unlock()
		return nil
	}
	w.running = true
	w.mu.Unlock()

	// Watch the directory containing the config file
	// (watching the file directly can miss some changes on some systems)
	dir := filepath.Dir(w.path)
	if err := w.watcher.Add(dir); err != nil {
		return err
	}

	log.Info().
		Str("path", w.path).
		Dur("debounce", w.debounce).
		Msg("Config watcher started")

	go w.watch(ctx)
	return nil
}

// Stop stops watching the config file
func (w *Watcher) Stop() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if !w.running {
		return nil
	}

	w.running = false
	return w.watcher.Close()
}

// Reload manually triggers a config reload
func (w *Watcher) Reload() error {
	return w.reload()
}

func (w *Watcher) watch(ctx context.Context) {
	var debounceTimer *time.Timer
	filename := filepath.Base(w.path)

	for {
		select {
		case <-ctx.Done():
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			return

		case event, ok := <-w.watcher.Events:
			if !ok {
				return
			}

			// Only react to changes to our config file
			if filepath.Base(event.Name) != filename {
				continue
			}

			// Only react to write or create events
			if !event.Has(fsnotify.Write) && !event.Has(fsnotify.Create) {
				continue
			}

			log.Debug().
				Str("event", event.Op.String()).
				Str("file", event.Name).
				Msg("Config file change detected")

			// Debounce rapid changes
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			debounceTimer = time.AfterFunc(w.debounce, func() {
				if err := w.reload(); err != nil {
					log.Error().Err(err).Msg("Config reload failed")
				}
			})

		case err, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			log.Error().Err(err).Msg("Config watcher error")
		}
	}
}

func (w *Watcher) reload() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	log.Info().Str("path", w.path).Msg("Reloading config")

	// Load the new config
	newConfig, err := Load(w.path)
	if err != nil {
		return err
	}

	// Call the onChange callback
	if err := w.onChange(newConfig); err != nil {
		return err
	}

	w.lastModified = time.Now()
	log.Info().Msg("Config reloaded successfully")
	return nil
}

// ReloadResult contains information about what changed during a reload
type ReloadResult struct {
	Success        bool     `json:"success"`
	Changes        []string `json:"changes"`
	Errors         []string `json:"errors,omitempty"`
	PreviousConfig string   `json:"previous_config,omitempty"`
}

// ConfigDiff represents differences between two configs
type ConfigDiff struct {
	ModelsChanged      bool
	ProvidersChanged   bool
	ResilienceChanged  bool
	BudgetChanged      bool
	LoggingChanged     bool
	MetricsChanged     bool
}

// Diff compares two configs and returns what changed
func Diff(old, new *Config) ConfigDiff {
	diff := ConfigDiff{}

	// Compare models
	if len(old.Models) != len(new.Models) {
		diff.ModelsChanged = true
	} else {
		for name, oldModel := range old.Models {
			if newModel, ok := new.Models[name]; !ok {
				diff.ModelsChanged = true
				break
			} else if !modelsEqual(oldModel, newModel) {
				diff.ModelsChanged = true
				break
			}
		}
	}

	// Compare providers (only check if keys changed, not credentials)
	if len(old.Providers) != len(new.Providers) {
		diff.ProvidersChanged = true
	} else {
		for name := range old.Providers {
			if _, ok := new.Providers[name]; !ok {
				diff.ProvidersChanged = true
				break
			}
		}
	}

	// Compare resilience settings
	if old.Resilience.Retry.MaxAttempts != new.Resilience.Retry.MaxAttempts ||
		old.Resilience.Retry.InitialBackoff != new.Resilience.Retry.InitialBackoff ||
		old.Resilience.CircuitBreaker.FailureThreshold != new.Resilience.CircuitBreaker.FailureThreshold {
		diff.ResilienceChanged = true
	}

	// Compare budget settings
	if old.Budget.Enabled != new.Budget.Enabled ||
		old.Budget.MaxCostPerHour != new.Budget.MaxCostPerHour ||
		old.Budget.MaxCostPerDay != new.Budget.MaxCostPerDay {
		diff.BudgetChanged = true
	}

	// Compare logging settings
	if old.Logging.Level != new.Logging.Level ||
		old.Logging.LogRequests != new.Logging.LogRequests {
		diff.LoggingChanged = true
	}

	// Compare metrics settings
	if old.Metrics.Enabled != new.Metrics.Enabled {
		diff.MetricsChanged = true
	}

	return diff
}

// modelsEqual compares two model configs
func modelsEqual(a, b ModelConfig) bool {
	if a.Primary.Provider != b.Primary.Provider || a.Primary.Model != b.Primary.Model {
		return false
	}
	if len(a.Fallbacks) != len(b.Fallbacks) {
		return false
	}
	for i := range a.Fallbacks {
		if a.Fallbacks[i].Provider != b.Fallbacks[i].Provider ||
			a.Fallbacks[i].Model != b.Fallbacks[i].Model {
			return false
		}
	}
	return true
}
