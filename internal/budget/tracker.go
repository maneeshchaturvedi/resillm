package budget

import (
	"sync"
	"time"
)

// Tracker monitors and enforces cost budgets
type Tracker struct {
	cfg Config

	// Cost tracking
	hourlyWindow *RollingWindow
	dailyWindow  *RollingWindow

	mu sync.RWMutex
}

// Config contains budget tracking configuration
type Config struct {
	Enabled          bool
	MaxCostPerHour   float64
	MaxCostPerDay    float64
	AlertThreshold   float64
	ActionOnExceeded string // "reject" or "allow_with_warning"
}

// Status represents the current budget status
type Status struct {
	HourlySpent     float64 `json:"hourly_spent"`
	HourlyLimit     float64 `json:"hourly_limit"`
	HourlyRemaining float64 `json:"hourly_remaining"`
	HourlyPercent   float64 `json:"hourly_percent"`

	DailySpent     float64 `json:"daily_spent"`
	DailyLimit     float64 `json:"daily_limit"`
	DailyRemaining float64 `json:"daily_remaining"`
	DailyPercent   float64 `json:"daily_percent"`

	AlertTriggered bool   `json:"alert_triggered"`
	Action         string `json:"action,omitempty"`
}

// CheckResult represents the result of a budget check
type CheckResult struct {
	Allowed        bool
	Warning        bool
	Message        string
	RemainingHour  float64
	RemainingDay   float64
	AlertTriggered bool
}

// NewTracker creates a new budget tracker
func NewTracker(cfg Config) *Tracker {
	return &Tracker{
		cfg:          cfg,
		hourlyWindow: NewRollingWindow(time.Hour, time.Minute),
		dailyWindow:  NewRollingWindow(24*time.Hour, time.Hour),
	}
}

// Check verifies if a request should be allowed based on current budget
func (t *Tracker) Check(estimatedCost float64) CheckResult {
	if !t.cfg.Enabled {
		return CheckResult{Allowed: true}
	}

	t.mu.RLock()
	defer t.mu.RUnlock()

	hourlySpent := t.hourlyWindow.Sum()
	dailySpent := t.dailyWindow.Sum()

	hourlyRemaining := t.cfg.MaxCostPerHour - hourlySpent
	dailyRemaining := t.cfg.MaxCostPerDay - dailySpent

	result := CheckResult{
		Allowed:       true,
		RemainingHour: hourlyRemaining,
		RemainingDay:  dailyRemaining,
	}

	// Check if we're over budget (use >= when cost is 0 to reject at limit)
	wouldExceedHourly := hourlySpent+estimatedCost >= t.cfg.MaxCostPerHour && t.cfg.MaxCostPerHour > 0
	wouldExceedDaily := dailySpent+estimatedCost >= t.cfg.MaxCostPerDay && t.cfg.MaxCostPerDay > 0

	// Check alert threshold (avoid division by zero)
	var hourlyPercent, dailyPercent float64
	if t.cfg.MaxCostPerHour > 0 {
		hourlyPercent = hourlySpent / t.cfg.MaxCostPerHour
	}
	if t.cfg.MaxCostPerDay > 0 {
		dailyPercent = dailySpent / t.cfg.MaxCostPerDay
	}
	result.AlertTriggered = hourlyPercent >= t.cfg.AlertThreshold || dailyPercent >= t.cfg.AlertThreshold

	if wouldExceedHourly || wouldExceedDaily {
		switch t.cfg.ActionOnExceeded {
		case "reject":
			result.Allowed = false
			if wouldExceedHourly {
				result.Message = "Hourly budget exceeded"
			} else {
				result.Message = "Daily budget exceeded"
			}
		case "allow_with_warning":
			result.Allowed = true
			result.Warning = true
			if wouldExceedHourly {
				result.Message = "Warning: Hourly budget exceeded"
			} else {
				result.Message = "Warning: Daily budget exceeded"
			}
		default:
			// Default to allow with warning
			result.Allowed = true
			result.Warning = true
			result.Message = "Budget exceeded (action not configured)"
		}
	}

	return result
}

// Record adds a cost to the budget tracker
func (t *Tracker) Record(cost float64) {
	if !t.cfg.Enabled {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()
	t.hourlyWindow.Add(now, cost)
	t.dailyWindow.Add(now, cost)
}

// Status returns the current budget status
func (t *Tracker) Status() Status {
	t.mu.RLock()
	defer t.mu.RUnlock()

	hourlySpent := t.hourlyWindow.Sum()
	dailySpent := t.dailyWindow.Sum()

	hourlyPercent := 0.0
	if t.cfg.MaxCostPerHour > 0 {
		hourlyPercent = (hourlySpent / t.cfg.MaxCostPerHour) * 100
	}

	dailyPercent := 0.0
	if t.cfg.MaxCostPerDay > 0 {
		dailyPercent = (dailySpent / t.cfg.MaxCostPerDay) * 100
	}

	alertTriggered := false
	if t.cfg.Enabled && t.cfg.AlertThreshold > 0 {
		hourlyRatio := 0.0
		dailyRatio := 0.0
		if t.cfg.MaxCostPerHour > 0 {
			hourlyRatio = hourlySpent / t.cfg.MaxCostPerHour
		}
		if t.cfg.MaxCostPerDay > 0 {
			dailyRatio = dailySpent / t.cfg.MaxCostPerDay
		}
		alertTriggered = hourlyRatio >= t.cfg.AlertThreshold || dailyRatio >= t.cfg.AlertThreshold
	}

	return Status{
		HourlySpent:     hourlySpent,
		HourlyLimit:     t.cfg.MaxCostPerHour,
		HourlyRemaining: max(0, t.cfg.MaxCostPerHour-hourlySpent),
		HourlyPercent:   hourlyPercent,

		DailySpent:     dailySpent,
		DailyLimit:     t.cfg.MaxCostPerDay,
		DailyRemaining: max(0, t.cfg.MaxCostPerDay-dailySpent),
		DailyPercent:   dailyPercent,

		AlertTriggered: alertTriggered,
	}
}

// Reset clears all budget tracking data
func (t *Tracker) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.hourlyWindow = NewRollingWindow(time.Hour, time.Minute)
	t.dailyWindow = NewRollingWindow(24*time.Hour, time.Hour)
}

// RollingWindow tracks values in a sliding time window
type RollingWindow struct {
	windowSize time.Duration
	bucketSize time.Duration
	buckets    []bucket
	numBuckets int

	mu sync.RWMutex
}

type bucket struct {
	timestamp time.Time
	value     float64
}

// NewRollingWindow creates a new rolling window
func NewRollingWindow(windowSize, bucketSize time.Duration) *RollingWindow {
	numBuckets := int(windowSize / bucketSize)
	if numBuckets < 1 {
		numBuckets = 1
	}

	return &RollingWindow{
		windowSize: windowSize,
		bucketSize: bucketSize,
		buckets:    make([]bucket, 0, numBuckets),
		numBuckets: numBuckets,
	}
}

// Add adds a value to the rolling window
func (w *RollingWindow) Add(t time.Time, value float64) {
	w.mu.Lock()
	defer w.mu.Unlock()

	// Clean old buckets first
	w.cleanExpired(t)

	// Find or create bucket for current time
	bucketTime := t.Truncate(w.bucketSize)

	for i := range w.buckets {
		if w.buckets[i].timestamp.Equal(bucketTime) {
			w.buckets[i].value += value
			return
		}
	}

	// Create new bucket
	w.buckets = append(w.buckets, bucket{
		timestamp: bucketTime,
		value:     value,
	})
}

// Sum returns the sum of all values in the window
func (w *RollingWindow) Sum() float64 {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.cleanExpired(time.Now())

	var sum float64
	for _, b := range w.buckets {
		sum += b.value
	}
	return sum
}

// cleanExpired removes expired buckets
func (w *RollingWindow) cleanExpired(now time.Time) {
	cutoff := now.Add(-w.windowSize)

	// Filter out expired buckets
	valid := w.buckets[:0]
	for _, b := range w.buckets {
		if b.timestamp.After(cutoff) {
			valid = append(valid, b)
		}
	}
	w.buckets = valid
}

// Count returns the number of active buckets
func (w *RollingWindow) Count() int {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return len(w.buckets)
}
