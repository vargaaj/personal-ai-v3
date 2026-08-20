// area_content_validation_test.go exercises the trusted local boundary for
// generated Home, Health, and Meals documents. Its fixtures preserve legacy
// Home shapes and structured weekly plans so validation changes cannot silently
// alter what the Review interface accepts or saves.
package main

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// validationNow is a Sunday so Health uses its exact current-date rule while
// Meals uses the same day's Sunday week boundary. Tests use this fixed local
// clock instead of time.Now so their calendar expectations remain durable.
var validationNow = time.Date(2026, time.July, 26, 10, 0, 0, 0, time.UTC)

// structuredWeeklyPlanJSONs builds complete Health and Meals fixtures whose
// only caller-controlled fields are their document dates. Keeping all seven
// entries valid lets date-focused tests prove the local boundary rejects a
// stale but otherwise schema-shaped model response.
func structuredWeeklyPlanJSONs(lastUpdated string, weekStarting string) (string, string) {
	workoutEntries := make([]string, 0, 7)
	mealRecommendations := make([]string, 0, 7)
	weekdays := []string{
		"Sunday",
		"Monday",
		"Tuesday",
		"Wednesday",
		"Thursday",
		"Friday",
		"Saturday",
	}
	for index, weekday := range weekdays {
		workoutEntries = append(workoutEntries, fmt.Sprintf(
			`{"id":"day-%d","title":"%s workout","details":"Complete the planned session.","href":"","state":"open","metadata":[{"label":"Plan","value":"Workout routine","attention":false},{"label":"Duration","value":"30 min","attention":false}]}`,
			index+1,
			weekday,
		))
		mealRecommendations = append(mealRecommendations, fmt.Sprintf(
			`{"day":"%s","title":"%s dinner","description":"Serve a practical balanced meal.","tags":["balanced","weeknight"]}`,
			weekday,
			weekday,
		))
	}

	return `{"last_updated":"` + lastUpdated + `","entries":[` +
			strings.Join(workoutEntries, ",") + `]}`,
		`{"week_starting":"` + weekStarting + `","recommendations":[` +
			strings.Join(mealRecommendations, ",") + `]}`
}

func TestValidateGeneratedJSONAcceptsHomeCompletedTaskReplacements(t *testing.T) {
	t.Parallel()

	// The first entry is an unfinished task that must survive unchanged. The
	// second is completed and therefore receives a new unfinished task in that
	// same second position; the surrounding test assertions make the queue order
	// and task identities clear to a future reader.
	originalJSON := `{"entries":[` +
		`{"id":"replace-filter","title":"Replace the HVAC filter","details":"Use the filter in the utility cabinet.","href":"","state":"open","metadata":[{"label":"Category","value":"Maintenance","attention":false}]},` +
		`{"id":"check-batteries","title":"Check smoke detector batteries","details":"Test every unit.","href":"","state":"done","metadata":[{"label":"Completed","value":"Yesterday","attention":false}]}` +
		`]}`
	updatedJSON := `{"entries":[` +
		`{"id":"replace-filter","title":"Replace the HVAC filter","details":"Use the filter in the utility cabinet.","href":"","state":"open","metadata":[{"label":"Category","value":"Maintenance","attention":false}]},` +
		`{"id":"clean-gutters-2026-07","title":"Clear the front gutters","details":"Remove leaves before the next rain.","href":"","state":"open","metadata":[{"label":"Category","value":"Seasonal","attention":false},{"label":"Due","value":"This week","attention":true}]}` +
		`]}`

	if err := validateGeneratedJSON(maintenanceTasksContentType, originalJSON, updatedJSON, validationNow); err != nil {
		t.Fatalf("validateGeneratedJSON() returned an unexpected error: %v", err)
	}

	var original, updated maintenanceTasksDocument
	if err := json.Unmarshal([]byte(originalJSON), &original); err != nil {
		t.Fatalf("decode original Home fixture: %v", err)
	}
	if err := json.Unmarshal([]byte(updatedJSON), &updated); err != nil {
		t.Fatalf("decode updated Home fixture: %v", err)
	}
	if !reflect.DeepEqual(updated.Entries[0], original.Entries[0]) {
		t.Fatalf("updated open task = %#v, want exact original %#v", updated.Entries[0], original.Entries[0])
	}
	if gotID := updated.Entries[1].ID; gotID != "clean-gutters-2026-07" {
		t.Fatalf("replacement task id at position 2 = %q, want the new gutter task", gotID)
	}
	if updated.Entries[1].State != "open" {
		t.Fatalf("replacement task state = %q, want open", updated.Entries[1].State)
	}
}

func TestValidateGeneratedJSONPreservesLegacyOpenHomeTaskShapes(t *testing.T) {
	t.Parallel()

	// The frontend accepts old open entries that omit display details. The first
	// task omits all three legacy optional fields; the second retains metadata
	// without attention. Both must survive exactly, while the third done position
	// is replaced by a complete current-format task.
	originalJSON := `{"entries":[` +
		`{"id":"legacy-minimal","title":"Review attic boxes","state":"open"},` +
		`{"id":"legacy-metadata","title":"Label pantry jars","state":"open","metadata":[{"label":"Room","value":"Kitchen"}]},` +
		`{"id":"done-lightbulbs","title":"Replace porch bulb","state":"done"}` +
		`]}`
	updatedJSON := `{"entries":[` +
		`{"id":"legacy-minimal","title":"Review attic boxes","state":"open"},` +
		`{"id":"legacy-metadata","title":"Label pantry jars","state":"open","metadata":[{"label":"Room","value":"Kitchen"}]},` +
		`{"id":"clean-dryer-vent","title":"Clean the dryer vent","details":"Remove lint from the exterior outlet.","href":"","state":"open","metadata":[{"label":"Category","value":"Safety","attention":true}]}` +
		`]}`

	if err := validateGeneratedJSON(maintenanceTasksContentType, originalJSON, updatedJSON, validationNow); err != nil {
		t.Fatalf("validateGeneratedJSON() returned an unexpected error: %v", err)
	}

	var original, updated maintenanceTasksDocument
	if err := json.Unmarshal([]byte(originalJSON), &original); err != nil {
		t.Fatalf("decode original legacy Home fixture: %v", err)
	}
	if err := json.Unmarshal([]byte(updatedJSON), &updated); err != nil {
		t.Fatalf("decode updated legacy Home fixture: %v", err)
	}
	for index := 0; index < 2; index++ {
		if !reflect.DeepEqual(updated.Entries[index], original.Entries[index]) {
			t.Fatalf(
				"updated legacy open task %d = %#v, want exact original %#v",
				index+1,
				updated.Entries[index],
				original.Entries[index],
			)
		}
	}
}

func TestValidateGeneratedJSONRejectsInvalidHomeTransformations(t *testing.T) {
	t.Parallel()

	originalJSON := `{"entries":[` +
		`{"id":"open-filter","title":"Replace the HVAC filter","details":"Use the filter in the utility cabinet.","href":"","state":"open","metadata":[{"label":"Category","value":"Maintenance","attention":false}]},` +
		`{"id":"done-batteries","title":"Check smoke detector batteries","details":"Test every unit.","href":"","state":"done","metadata":[{"label":"Completed","value":"Yesterday","attention":false}]}` +
		`]}`
	validUpdatedJSON := `{"entries":[` +
		`{"id":"open-filter","title":"Replace the HVAC filter","details":"Use the filter in the utility cabinet.","href":"","state":"open","metadata":[{"label":"Category","value":"Maintenance","attention":false}]},` +
		`{"id":"new-gutter-task","title":"Clear the gutters","details":"Remove leaves before rain.","href":"","state":"open","metadata":[{"label":"Category","value":"Seasonal","attention":false}]}` +
		`]}`

	tests := []struct {
		name        string
		updatedJSON string
		wantMessage string
	}{
		{
			name: "changes an unfinished task at its original position",
			updatedJSON: strings.Replace(
				validUpdatedJSON,
				`"Replace the HVAC filter"`,
				`"Change the HVAC filter"`,
				1,
			),
			wantMessage: "was open and must remain unchanged",
		},
		{
			name: "keeps the completed task identifier",
			updatedJSON: strings.Replace(
				validUpdatedJSON,
				`"new-gutter-task"`,
				`"done-batteries"`,
				1,
			),
			wantMessage: "must differ from completed task id",
		},
		{
			name: "reuses an existing unfinished identifier",
			updatedJSON: strings.Replace(
				validUpdatedJSON,
				`"new-gutter-task"`,
				`"open-filter"`,
				1,
			),
			wantMessage: "repeats id",
		},
		{
			name: "leaves a returned task completed",
			updatedJSON: strings.Replace(
				validUpdatedJSON,
				`"state":"open"`,
				`"state":"done"`,
				1,
			),
			wantMessage: "state must be open",
		},
		{
			name: "uses whitespace around a replacement identifier",
			updatedJSON: strings.Replace(
				validUpdatedJSON,
				`"new-gutter-task"`,
				`" new-gutter-task "`,
				1,
			),
			wantMessage: "id must not have leading or trailing whitespace",
		},
		{
			name: "omits replacement details",
			updatedJSON: strings.Replace(
				validUpdatedJSON,
				`"details":"Remove leaves before rain.",`,
				"",
				1,
			),
			wantMessage: "replacement details is required",
		},
		{
			name: "omits replacement metadata attention",
			updatedJSON: strings.Replace(
				validUpdatedJSON,
				`"value":"Seasonal","attention":false`,
				`"value":"Seasonal"`,
				1,
			),
			wantMessage: "attention is required",
		},
		{
			// Removing the completed source position would also shift all later
			// Review rows, so an update must retain the original queue length.
			name: "removes a task from the queue",
			updatedJSON: `{"entries":[` +
				`{"id":"open-filter","title":"Replace the HVAC filter","details":"Use the filter in the utility cabinet.","href":"","state":"open","metadata":[{"label":"Category","value":"Maintenance","attention":false}]}` +
				`]}`,
			wantMessage: "must keep 2 entries",
		},
		{
			name:        "adds a field outside the Home document schema",
			updatedJSON: validUpdatedJSON[:len(validUpdatedJSON)-1] + `,"version":2}`,
			wantMessage: "unknown field",
		},
		{
			name:        "returns malformed JSON",
			updatedJSON: `{"entries":[`,
			wantMessage: "unexpected EOF",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := validateGeneratedJSON(maintenanceTasksContentType, originalJSON, test.updatedJSON, validationNow)
			if err == nil {
				t.Fatal("validateGeneratedJSON() returned nil, expected an error")
			}
			if !strings.Contains(err.Error(), test.wantMessage) {
				t.Fatalf(
					"validateGeneratedJSON() error = %q, want it to contain %q",
					err,
					test.wantMessage,
				)
			}
		})
	}
}

func TestValidateGeneratedJSONRejectsWhitespaceAndInvalidLegacyHomeFields(t *testing.T) {
	t.Parallel()

	validUpdatedJSON := `{"entries":[` +
		`{"id":"new-task","title":"Clean the dryer vent","details":"Remove lint.","href":"","state":"open","metadata":[{"label":"Category","value":"Safety","attention":true}]}` +
		`]}`

	tests := []struct {
		name         string
		originalJSON string
		wantMessage  string
	}{
		{
			name: "rejects whitespace disguised completed id",
			originalJSON: `{"entries":[` +
				`{"id":" done-task ","title":"Replace porch bulb","state":"done"}` +
				`]}`,
			wantMessage: "id must not have leading or trailing whitespace",
		},
		{
			// The second id differs only by trailing whitespace. Rejecting the id
			// itself prevents this pair from evading the ordinary duplicate check.
			name: "rejects whitespace equivalent source identifiers",
			originalJSON: `{"entries":[` +
				`{"id":"same-task","title":"Keep this task","state":"open"},` +
				`{"id":"same-task ","title":"Replace this task","state":"done"}` +
				`]}`,
			wantMessage: "id must not have leading or trailing whitespace",
		},
		{
			name: "rejects null instead of omitted legacy details",
			originalJSON: `{"entries":[` +
				`{"id":"done-task","title":"Replace porch bulb","details":null,"state":"done"}` +
				`]}`,
			wantMessage: "must be a string, not null",
		},
		{
			name: "rejects wrong type for legacy metadata attention",
			originalJSON: `{"entries":[` +
				`{"id":"done-task","title":"Replace porch bulb","state":"done","metadata":[{"label":"Room","value":"Porch","attention":"yes"}]}` +
				`]}`,
			wantMessage: "must be a boolean",
		},
		{
			// Metadata uses its own strict decoder so that optional attention does
			// not accidentally allow a model to introduce unknown nested fields.
			name: "rejects unknown nested legacy metadata field",
			originalJSON: `{"entries":[` +
				`{"id":"done-task","title":"Replace porch bulb","state":"done","metadata":[{"label":"Room","value":"Porch","unexpected":true}]}` +
				`]}`,
			wantMessage: "unknown field",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := validateGeneratedJSON(
				maintenanceTasksContentType,
				test.originalJSON,
				validUpdatedJSON,
				validationNow,
			)
			if err == nil {
				t.Fatal("validateGeneratedJSON() returned nil, expected an error")
			}
			if !strings.Contains(err.Error(), test.wantMessage) {
				t.Fatalf(
					"validateGeneratedJSON() error = %q, want it to contain %q",
					err,
					test.wantMessage,
				)
			}
		})
	}
}

func TestValidateGeneratedJSONAcceptsStructuredWeeklyPlans(t *testing.T) {
	t.Parallel()

	// The stored examples intentionally use the older loose shapes. A structured
	// response adds required fields and expands Meals to seven days; validation
	// should use the new stable contract instead of rejecting those improvements
	// merely because they differ from the input document.
	workoutJSON, mealsJSON := structuredWeeklyPlanJSONs("2026-07-26", "2026-07-26")

	tests := []struct {
		name         string
		contentType  string
		originalJSON string
		updatedJSON  string
	}{
		{
			name:         "workout schema normalizes an older entry shape",
			contentType:  weeklyWorkoutRoutineContentType,
			originalJSON: `{"last_updated":"2026-07-19","entries":[{"id":"old-plan"}]}`,
			updatedJSON:  workoutJSON,
		},
		{
			name:         "meals schema expands an older partial week",
			contentType:  weeklyMealRecommendationsContentType,
			originalJSON: `{"week_starting":"2026-07-19","recommendations":[{"day":"Sunday"}]}`,
			updatedJSON:  mealsJSON,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if err := validateGeneratedJSON(
				test.contentType,
				test.originalJSON,
				test.updatedJSON,
				validationNow,
			); err != nil {
				t.Fatalf("validateGeneratedJSON() returned an unexpected error: %v", err)
			}
		})
	}
}

func TestValidateGeneratedJSONRejectsStructuredWeeklyPlanFromAnotherDate(t *testing.T) {
	t.Parallel()

	// Both documents remain completely valid in shape and use Sunday for Meals.
	// Their dates alone are stale, which is the model-output case the runner must
	// reject before it can overwrite the current week's visible plans.
	staleWorkoutJSON, staleMealsJSON := structuredWeeklyPlanJSONs("2026-07-19", "2026-07-19")
	tests := []struct {
		name        string
		contentType string
		updatedJSON string
		wantMessage string
	}{
		{
			name:        "Health last updated is not the runner current date",
			contentType: weeklyWorkoutRoutineContentType,
			updatedJSON: staleWorkoutJSON,
			wantMessage: "last_updated must equal current date 2026-07-26",
		},
		{
			name:        "Meals Sunday belongs to a previous week",
			contentType: weeklyMealRecommendationsContentType,
			updatedJSON: staleMealsJSON,
			wantMessage: "week_starting must equal target week 2026-07-26",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := validateGeneratedJSON(test.contentType, `{}`, test.updatedJSON, validationNow)
			if err == nil {
				t.Fatal("validateGeneratedJSON() returned nil, expected an exact-date error")
			}
			if !strings.Contains(err.Error(), test.wantMessage) {
				t.Fatalf(
					"validateGeneratedJSON() error = %q, want it to contain %q",
					err,
					test.wantMessage,
				)
			}
		})
	}
}

func TestValidateGeneratedJSONRejectsMalformedStructuredWeeklyPlans(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		contentType string
		updatedJSON string
		wantMessage string
	}{
		{
			name:        "workout requires seven entries",
			contentType: weeklyWorkoutRoutineContentType,
			updatedJSON: `{"last_updated":"2026-07-26","entries":[]}`,
			wantMessage: "must contain 7 entries",
		},
		{
			name:        "workout rejects fields outside its schema",
			contentType: weeklyWorkoutRoutineContentType,
			updatedJSON: `{"last_updated":"2026-07-26","entries":[],"notes":"unexpected"}`,
			wantMessage: "unknown field",
		},
		{
			name:        "meals requires a Sunday week start",
			contentType: weeklyMealRecommendationsContentType,
			updatedJSON: `{"week_starting":"2026-07-27","recommendations":[]}`,
			wantMessage: "must be a Sunday",
		},
		{
			name:        "meals requires all seven recommendations",
			contentType: weeklyMealRecommendationsContentType,
			updatedJSON: `{"week_starting":"2026-07-26","recommendations":[]}`,
			wantMessage: "must contain 7 recommendations",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := validateGeneratedJSON(test.contentType, `{}`, test.updatedJSON, validationNow)
			if err == nil {
				t.Fatal("validateGeneratedJSON() returned nil, expected an error")
			}
			if !strings.Contains(err.Error(), test.wantMessage) {
				t.Fatalf(
					"validateGeneratedJSON() error = %q, want it to contain %q",
					err,
					test.wantMessage,
				)
			}
		})
	}
}
