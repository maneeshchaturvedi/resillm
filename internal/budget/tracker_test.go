package budget

import (
	"testing"
	"time"
)

func TestTracker_Disabled(t *testing.T) {
	tracker := NewTracker(Config{
		Enabled: false,
	})

	result := tracker.Check(100.0)

	if !result.Allowed {
		t.Error("expected request to be allowed when budget is disabled")
	}
}

func TestTracker_UnderBudget(t *testing.T) {
	tracker := NewTracker(Config{
		Enabled:          true,
		MaxCostPerHour:   100.0,
		MaxCostPerDay:    1000.0,
		AlertThreshold:   0.8,
		ActionOnExceeded: "reject",
	})

	// Record some costs
	tracker.Record(10.0)
	tracker.Record(20.0)

	result := tracker.Check(10.0)

	if !result.Allowed {
		t.Error("expected request to be allowed when under budget")
	}

	if result.Warning {
		t.Error("expected no warning when under budget")
	}

	if result.RemainingHour != 70.0 {
		t.Errorf("expected remaining hour 70.0, got %f", result.RemainingHour)
	}
}

func TestTracker_HourlyBudgetExceeded_Reject(t *testing.T) {
	tracker := NewTracker(Config{
		Enabled:          true,
		MaxCostPerHour:   100.0,
		MaxCostPerDay:    1000.0,
		AlertThreshold:   0.8,
		ActionOnExceeded: "reject",
	})

	tracker.Record(90.0)

	result := tracker.Check(20.0) // Would exceed hourly budget

	if result.Allowed {
		t.Error("expected request to be rejected when hourly budget exceeded")
	}

	if result.Message != "Hourly budget exceeded" {
		t.Errorf("expected 'Hourly budget exceeded', got '%s'", result.Message)
	}
}

func TestTracker_DailyBudgetExceeded_Reject(t *testing.T) {
	tracker := NewTracker(Config{
		Enabled:          true,
		MaxCostPerHour:   1000.0, // High hourly limit
		MaxCostPerDay:    100.0,  // Low daily limit
		AlertThreshold:   0.8,
		ActionOnExceeded: "reject",
	})

	tracker.Record(90.0)

	result := tracker.Check(20.0) // Would exceed daily budget

	if result.Allowed {
		t.Error("expected request to be rejected when daily budget exceeded")
	}

	if result.Message != "Daily budget exceeded" {
		t.Errorf("expected 'Daily budget exceeded', got '%s'", result.Message)
	}
}

func TestTracker_BudgetExceeded_AllowWithWarning(t *testing.T) {
	tracker := NewTracker(Config{
		Enabled:          true,
		MaxCostPerHour:   100.0,
		MaxCostPerDay:    1000.0,
		AlertThreshold:   0.8,
		ActionOnExceeded: "allow_with_warning",
	})

	tracker.Record(90.0)

	result := tracker.Check(20.0) // Would exceed hourly budget

	if !result.Allowed {
		t.Error("expected request to be allowed with warning")
	}

	if !result.Warning {
		t.Error("expected warning to be set")
	}

	if result.Message != "Warning: Hourly budget exceeded" {
		t.Errorf("expected 'Warning: Hourly budget exceeded', got '%s'", result.Message)
	}
}

func TestTracker_AlertThreshold(t *testing.T) {
	tracker := NewTracker(Config{
		Enabled:          true,
		MaxCostPerHour:   100.0,
		MaxCostPerDay:    1000.0,
		AlertThreshold:   0.8, // Alert at 80%
		ActionOnExceeded: "reject",
	})

	tracker.Record(79.0)
	result := tracker.Check(0)

	if result.AlertTriggered {
		t.Error("expected no alert at 79%")
	}

	tracker.Record(2.0) // Now at 81%
	result = tracker.Check(0)

	if !result.AlertTriggered {
		t.Error("expected alert at 81%")
	}
}

func TestTracker_Status(t *testing.T) {
	tracker := NewTracker(Config{
		Enabled:          true,
		MaxCostPerHour:   100.0,
		MaxCostPerDay:    1000.0,
		AlertThreshold:   0.8,
		ActionOnExceeded: "reject",
	})

	tracker.Record(50.0)

	status := tracker.Status()

	if status.HourlySpent != 50.0 {
		t.Errorf("expected hourly spent 50.0, got %f", status.HourlySpent)
	}

	if status.HourlyLimit != 100.0 {
		t.Errorf("expected hourly limit 100.0, got %f", status.HourlyLimit)
	}

	if status.HourlyRemaining != 50.0 {
		t.Errorf("expected hourly remaining 50.0, got %f", status.HourlyRemaining)
	}

	if status.HourlyPercent != 50.0 {
		t.Errorf("expected hourly percent 50.0, got %f", status.HourlyPercent)
	}

	if status.DailySpent != 50.0 {
		t.Errorf("expected daily spent 50.0, got %f", status.DailySpent)
	}

	if status.DailyLimit != 1000.0 {
		t.Errorf("expected daily limit 1000.0, got %f", status.DailyLimit)
	}
}

func TestTracker_Reset(t *testing.T) {
	tracker := NewTracker(Config{
		Enabled:          true,
		MaxCostPerHour:   100.0,
		MaxCostPerDay:    1000.0,
		AlertThreshold:   0.8,
		ActionOnExceeded: "reject",
	})

	tracker.Record(50.0)

	status := tracker.Status()
	if status.HourlySpent != 50.0 {
		t.Errorf("expected hourly spent 50.0 before reset, got %f", status.HourlySpent)
	}

	tracker.Reset()

	status = tracker.Status()
	if status.HourlySpent != 0.0 {
		t.Errorf("expected hourly spent 0.0 after reset, got %f", status.HourlySpent)
	}
}

func TestTracker_RecordDisabled(t *testing.T) {
	tracker := NewTracker(Config{
		Enabled: false,
	})

	// Should not panic or cause issues when disabled
	tracker.Record(100.0)

	status := tracker.Status()
	if status.HourlySpent != 0.0 {
		t.Errorf("expected no tracking when disabled")
	}
}

// Rolling window tests

func TestRollingWindow_Add(t *testing.T) {
	window := NewRollingWindow(time.Hour, time.Minute)

	now := time.Now()
	window.Add(now, 10.0)
	window.Add(now, 20.0)

	sum := window.Sum()
	if sum != 30.0 {
		t.Errorf("expected sum 30.0, got %f", sum)
	}
}

func TestRollingWindow_MultipleBuckets(t *testing.T) {
	window := NewRollingWindow(time.Hour, time.Minute)

	now := time.Now()
	window.Add(now, 10.0)
	window.Add(now.Add(-10*time.Minute), 20.0)
	window.Add(now.Add(-30*time.Minute), 30.0)

	sum := window.Sum()
	if sum != 60.0 {
		t.Errorf("expected sum 60.0, got %f", sum)
	}
}

func TestRollingWindow_ExpiredBuckets(t *testing.T) {
	window := NewRollingWindow(time.Hour, time.Minute)

	now := time.Now()
	window.Add(now, 10.0)
	window.Add(now.Add(-2*time.Hour), 20.0) // Should be expired

	sum := window.Sum()
	if sum != 10.0 {
		t.Errorf("expected sum 10.0 (expired bucket removed), got %f", sum)
	}
}

func TestRollingWindow_Count(t *testing.T) {
	window := NewRollingWindow(time.Hour, time.Minute)

	now := time.Now()
	window.Add(now, 10.0)
	window.Add(now.Add(-10*time.Minute), 20.0)

	// Force cleanup by calling Sum
	window.Sum()

	count := window.Count()
	if count != 2 {
		t.Errorf("expected 2 buckets, got %d", count)
	}
}

func TestTracker_ConcurrentAccess(t *testing.T) {
	tracker := NewTracker(Config{
		Enabled:          true,
		MaxCostPerHour:   1000.0,
		MaxCostPerDay:    10000.0,
		AlertThreshold:   0.8,
		ActionOnExceeded: "reject",
	})

	done := make(chan bool)

	// Concurrent writers
	for i := 0; i < 10; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				tracker.Record(1.0)
			}
			done <- true
		}()
	}

	// Concurrent readers
	for i := 0; i < 10; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				tracker.Check(1.0)
				tracker.Status()
			}
			done <- true
		}()
	}

	// Wait for all goroutines
	for i := 0; i < 20; i++ {
		<-done
	}

	// Should have recorded 1000 total
	status := tracker.Status()
	if status.HourlySpent != 1000.0 {
		t.Errorf("expected hourly spent 1000.0, got %f", status.HourlySpent)
	}
}

func TestTracker_DefaultAction(t *testing.T) {
	tracker := NewTracker(Config{
		Enabled:          true,
		MaxCostPerHour:   100.0,
		MaxCostPerDay:    1000.0,
		AlertThreshold:   0.8,
		ActionOnExceeded: "unknown", // Invalid action
	})

	tracker.Record(90.0)

	result := tracker.Check(20.0) // Would exceed budget

	// Should default to allow with warning
	if !result.Allowed {
		t.Error("expected request to be allowed with default action")
	}

	if !result.Warning {
		t.Error("expected warning with default action")
	}
}

func TestTracker_StatusWithAlertThreshold(t *testing.T) {
	tracker := NewTracker(Config{
		Enabled:          true,
		MaxCostPerHour:   100.0,
		MaxCostPerDay:    1000.0,
		AlertThreshold:   0.9,
		ActionOnExceeded: "reject",
	})

	// Below alert threshold
	tracker.Record(89.0)
	status := tracker.Status()
	if status.AlertTriggered {
		t.Error("expected no alert below threshold")
	}

	// Above alert threshold
	tracker.Record(2.0)
	status = tracker.Status()
	if !status.AlertTriggered {
		t.Error("expected alert above threshold")
	}
}

func TestStatus_ZeroLimits(t *testing.T) {
	tracker := NewTracker(Config{
		Enabled:          true,
		MaxCostPerHour:   0, // Zero limit
		MaxCostPerDay:    0,
		AlertThreshold:   0.8,
		ActionOnExceeded: "reject",
	})

	status := tracker.Status()

	// Should handle zero division gracefully
	if status.HourlyPercent != 0.0 {
		t.Errorf("expected hourly percent 0.0, got %f", status.HourlyPercent)
	}

	if status.DailyPercent != 0.0 {
		t.Errorf("expected daily percent 0.0, got %f", status.DailyPercent)
	}
}
