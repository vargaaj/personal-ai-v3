package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// storage_test.go exercises validation at the Supabase storage boundary without
// requiring a live database. Connection and query behavior will be covered by
// the HTTP integration once DATABASE_URL is available locally.

func TestValidateAreaContentAcceptsSeededDocument(t *testing.T) {
	t.Parallel()

	content := AreaContent{
		AreaID:      "health",
		ContentType: "weekly_workout_routine",
		Content:     json.RawMessage(`{"last_updated":"2026-07-22","entries":[]}`),
		Revision:    1,
	}

	validationError := validateAreaContent(content)
	if validationError != nil {
		t.Fatalf("validateAreaContent() returned an unexpected error: %v", validationError)
	}
}

func TestValidateAreaContentRejectsInvalidStoredValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		content     AreaContent
		wantMessage string
	}{
		{
			name: "blank area id",
			content: AreaContent{
				AreaID:      "  ",
				ContentType: "maintenance_tasks",
				Content:     json.RawMessage(`{"entries":[]}`),
				Revision:    1,
			},
			wantMessage: "stored area id is required",
		},
		{
			name: "blank content type",
			content: AreaContent{
				AreaID:      "home",
				ContentType: "",
				Content:     json.RawMessage(`{"entries":[]}`),
				Revision:    1,
			},
			wantMessage: "stored content type is required",
		},
		{
			name: "invalid json",
			content: AreaContent{
				AreaID:      "reading",
				ContentType: "reading_queue",
				Content:     json.RawMessage(`{"entries":`),
				Revision:    1,
			},
			wantMessage: "is not valid JSON",
		},
		{
			name: "non-positive revision",
			content: AreaContent{
				AreaID:      "meals",
				ContentType: "weekly_meal_recommendations",
				Content:     json.RawMessage(`{"recommendations":[]}`),
				Revision:    0,
			},
			wantMessage: "must be positive",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := validateAreaContent(test.content)
			if err == nil {
				t.Fatal("validateAreaContent() returned nil, expected an error")
			}
			if !strings.Contains(err.Error(), test.wantMessage) {
				t.Fatalf("validateAreaContent() error = %q, want it to contain %q", err, test.wantMessage)
			}
		})
	}
}

func TestValidateAreaUpdatePromptAcceptsConfiguredPrompt(t *testing.T) {
	t.Parallel()

	updatePrompt := AreaUpdatePrompt{
		AreaID:      "health",
		ContentType: "weekly_workout_routine",
		Prompt:      "Replace the routine with a balanced plan for the coming week.",
	}

	validationError := validateAreaUpdatePrompt(updatePrompt)
	if validationError != nil {
		t.Fatalf("validateAreaUpdatePrompt() returned an unexpected error: %v", validationError)
	}
}

func TestValidateAreaUpdatePromptRejectsBlankFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		updatePrompt AreaUpdatePrompt
		wantMessage  string
	}{
		{
			name: "blank area id",
			updatePrompt: AreaUpdatePrompt{
				ContentType: "weekly_workout_routine",
				Prompt:      "Create the next workout routine.",
			},
			wantMessage: "area id is required",
		},
		{
			name: "blank content type",
			updatePrompt: AreaUpdatePrompt{
				AreaID: "health",
				Prompt: "Create the next workout routine.",
			},
			wantMessage: "content type is required",
		},
		{
			name: "blank prompt",
			updatePrompt: AreaUpdatePrompt{
				AreaID:      "meals",
				ContentType: "weekly_meal_recommendations",
				Prompt:      "  ",
			},
			wantMessage: "is required",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := validateAreaUpdatePrompt(test.updatePrompt)
			if err == nil {
				t.Fatal("validateAreaUpdatePrompt() returned nil, expected an error")
			}
			if !strings.Contains(err.Error(), test.wantMessage) {
				t.Fatalf("validateAreaUpdatePrompt() error = %q, want it to contain %q", err, test.wantMessage)
			}
		})
	}
}
