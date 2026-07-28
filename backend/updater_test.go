// updater_test.go covers the scheduling decisions that can be verified without
// a live Supabase database or an OpenRouter request. The production runner now
// calls concrete storage/model functions directly, so these focused tests avoid
// recreating those services behind function callbacks.
package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestAreaUpdateRunnerRequiresDatabase(t *testing.T) {
	t.Parallel()

	runner := areaUpdateRunner{}
	now := time.Date(2026, time.July, 24, 10, 0, 0, 0, time.UTC)

	_, err := runner.run(context.Background(), now, false)
	if err == nil {
		t.Fatal("run() returned nil, expected a missing database error")
	}
	if !strings.Contains(err.Error(), "database is required") {
		t.Fatalf("run() error = %q, want it to mention the database", err)
	}
}

func TestWeeklyAreaUpdateDueUsesSundayBoundary(t *testing.T) {
	t.Parallel()

	location := time.FixedZone("America/New_York", -4*60*60)
	now := time.Date(2026, time.July, 24, 10, 0, 0, 0, location)

	tests := []struct {
		name      string
		updatedAt time.Time
		wantDue   bool
	}{
		{
			name:      "never updated",
			updatedAt: time.Time{},
			wantDue:   true,
		},
		{
			name:      "updated before current Sunday",
			updatedAt: time.Date(2026, time.July, 18, 23, 59, 59, 0, location),
			wantDue:   true,
		},
		{
			name:      "updated at current Sunday midnight",
			updatedAt: time.Date(2026, time.July, 19, 0, 0, 0, 0, location),
			wantDue:   false,
		},
		{
			name:      "updated later this week",
			updatedAt: time.Date(2026, time.July, 22, 12, 0, 0, 0, location),
			wantDue:   false,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			gotDue := weeklyAreaUpdateDue(test.updatedAt, now)
			if gotDue != test.wantDue {
				t.Fatalf("weeklyAreaUpdateDue() = %t, want %t", gotDue, test.wantDue)
			}
		})
	}
}

func TestBuildCalendarUpdatePromptAddsCurrentWeek(t *testing.T) {
	t.Parallel()

	location := time.FixedZone("America/New_York", -4*60*60)
	now := time.Date(2026, time.July, 24, 10, 0, 0, 0, location)

	prompt, err := buildCalendarUpdatePrompt(
		"Refresh the meals for the supplied target week.",
		now,
	)
	if err != nil {
		t.Fatalf("buildCalendarUpdatePrompt() returned an unexpected error: %v", err)
	}
	if !strings.Contains(prompt, "Current date: 2026-07-24") {
		t.Fatalf("prompt omitted current date: %q", prompt)
	}
	if !strings.Contains(prompt, "Target week starts Sunday: 2026-07-19") {
		t.Fatalf("prompt omitted target week: %q", prompt)
	}
	if !strings.Contains(prompt, "Sunday through Saturday, including the starting Sunday") {
		t.Fatalf("prompt omitted the full-week instruction: %q", prompt)
	}
}

func TestBuildCalendarUpdatePromptRejectsBlankStoredPrompt(t *testing.T) {
	t.Parallel()

	_, err := buildCalendarUpdatePrompt("   ", time.Now())
	if err == nil {
		t.Fatal("buildCalendarUpdatePrompt() returned nil, expected an error")
	}
}
