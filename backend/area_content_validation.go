// area_content_validation.go defines the trusted Go representations and local
// validation boundary for generated Home, Health, and Meals documents. updater.go
// owns calling this boundary after OpenRouter responds; this file deliberately
// contains no scheduling, retries, storage, or network orchestration.
//
// The types below mirror the JSON Schemas in response_formats.go. Decoding into
// named fields, with unknown fields rejected, gives the save path an independent
// check even if a provider returns malformed structured output.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"time"
)

// maintenanceTasksDocument is the complete Home document. Home is deliberately
// validated against both its stored input and the model output: a refresh only
// replaces completed tasks, so it must preserve every unfinished task exactly.
type maintenanceTasksDocument struct {
	Entries []maintenanceTask `json:"entries"`
}

// maintenanceTask is one Home row as it is stored and shown on the Review
// screen. Its optional wrappers distinguish an omitted legacy field from an
// invalid null or wrong-type value. That distinction lets copied open tasks
// retain their exact decoded shape while replacements still require a complete
// task shape.
type maintenanceTask struct {
	ID       string                          `json:"id"`
	Title    string                          `json:"title"`
	Details  optionalMaintenanceTaskString   `json:"details"`
	Href     optionalMaintenanceTaskString   `json:"href"`
	State    string                          `json:"state"`
	Metadata optionalMaintenanceTaskMetadata `json:"metadata"`
}

// maintenanceTaskMetadata is the compact label/value information displayed
// below a Home task, such as its category and next due date.
type maintenanceTaskMetadata struct {
	Label     string                      `json:"label"`
	Value     string                      `json:"value"`
	Attention optionalMaintenanceTaskBool `json:"attention"`
}

// optionalMaintenanceTaskString records whether a legacy task included one of
// its optional text fields. The custom decoder rejects null so only a genuinely
// omitted property receives Present=false; this prevents invalid JSON types
// from being mistaken for an old frontend-compatible shape.
type optionalMaintenanceTaskString struct {
	Present bool
	Value   string
}

// UnmarshalJSON runs only when the property appears in JSON. A string is the
// sole accepted value because the Review UI treats details and href as text.
// Called by encoding/json while decoding a present Home details or href field.
func (field *optionalMaintenanceTaskString) UnmarshalJSON(value []byte) error {
	field.Present = true
	if strings.TrimSpace(string(value)) == "null" {
		return errors.New("must be a string, not null")
	}
	if err := json.Unmarshal(value, &field.Value); err != nil {
		return fmt.Errorf("must be a string: %w", err)
	}
	return nil
}

// optionalMaintenanceTaskBool provides the same omitted-versus-null distinction
// for metadata attention. Old open tasks may omit attention; a present value
// must still be a boolean, and new replacement metadata must include it.
type optionalMaintenanceTaskBool struct {
	Present bool
	Value   bool
}

// UnmarshalJSON accepts only JSON booleans when attention is present.
// Called by encoding/json while decoding a present Home metadata attention field.
func (field *optionalMaintenanceTaskBool) UnmarshalJSON(value []byte) error {
	field.Present = true
	if strings.TrimSpace(string(value)) == "null" {
		return errors.New("must be a boolean, not null")
	}
	if err := json.Unmarshal(value, &field.Value); err != nil {
		return fmt.Errorf("must be a boolean: %w", err)
	}
	return nil
}

// optionalMaintenanceTaskMetadata preserves whether a legacy open task omitted
// metadata entirely. When metadata is present, its custom decoder continues to
// reject unknown nested fields instead of allowing a custom unmarshal method to
// bypass decodeStrictJSONDocument's outer DisallowUnknownFields setting.
type optionalMaintenanceTaskMetadata struct {
	Present bool
	Items   []maintenanceTaskMetadata
}

// UnmarshalJSON accepts a present metadata array and applies strict decoding to
// every nested metadata object. JSON null is invalid rather than an omission.
// Called by encoding/json while decoding a present Home metadata field.
func (field *optionalMaintenanceTaskMetadata) UnmarshalJSON(value []byte) error {
	field.Present = true
	if strings.TrimSpace(string(value)) == "null" {
		return errors.New("must be an array, not null")
	}

	decoder := json.NewDecoder(strings.NewReader(string(value)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&field.Items); err != nil {
		return fmt.Errorf("must be an array of metadata items: %w", err)
	}
	if err := ensureJSONDecoderEOF(decoder); err != nil {
		return fmt.Errorf("metadata contains trailing content: %w", err)
	}
	return nil
}

// weeklyWorkoutRoutineDocument is the complete Health weekly plan returned by the model.
type weeklyWorkoutRoutineDocument struct {
	LastUpdated string                      `json:"last_updated"`
	Entries     []weeklyWorkoutRoutineEntry `json:"entries"`
}

// weeklyWorkoutRoutineEntry is one visible day in a Health plan.
type weeklyWorkoutRoutineEntry struct {
	ID       string                      `json:"id"`
	Title    string                      `json:"title"`
	Details  string                      `json:"details"`
	Href     *string                     `json:"href"`
	State    string                      `json:"state"`
	Metadata []weeklyReviewEntryMetadata `json:"metadata"`
}

// weeklyReviewEntryMetadata is one label/value detail displayed below a Health entry.
type weeklyReviewEntryMetadata struct {
	Label     string `json:"label"`
	Value     string `json:"value"`
	Attention *bool  `json:"attention"`
}

// weeklyMealRecommendationsDocument is the complete Sunday-through-Saturday Meals plan.
type weeklyMealRecommendationsDocument struct {
	WeekStarting    string                     `json:"week_starting"`
	Recommendations []weeklyMealRecommendation `json:"recommendations"`
}

// weeklyMealRecommendation is the meal content shown for one weekday.
type weeklyMealRecommendation struct {
	Day         string    `json:"day"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Tags        *[]string `json:"tags"`
}

// validateGeneratedJSON chooses the trusted local validation boundary that
// matches the request sent to OpenRouter. A structured response is checked
// against its stable document contract rather than compared with legacy stored
// JSON, which may omit fields that the new schema now requires. That distinction
// allows the first structured refresh to normalize an older document safely.
//
// now is the runner's already-selected application-local time. Health's visible
// last_updated label must describe that date, while Meals must describe the
// Sunday that starts that same local planning week. Home and generic documents
// deliberately ignore it so their existing validation rules do not change.
// Called by updateJSON and repeated by areaUpdateRunner.updateTarget in updater.go
// immediately before saveAreaContent.
func validateGeneratedJSON(
	contentType string,
	originalJSON string,
	updatedJSON string,
	now time.Time,
) error {
	switch contentType {
	case maintenanceTasksContentType:
		return validateMaintenanceTasksJSON(originalJSON, updatedJSON)
	case weeklyWorkoutRoutineContentType:
		return validateWeeklyWorkoutRoutineJSON(updatedJSON, now)
	case weeklyMealRecommendationsContentType:
		return validateWeeklyMealRecommendationsJSON(updatedJSON, now)
	default:
		return validateJSONStructure(originalJSON, updatedJSON)
	}
}

// validateMaintenanceTasksJSON applies the Home-specific replacement rules
// that JSON Schema cannot express. The model returns a whole document, but only
// completed source positions may change: open tasks stay field-for-field the
// same after JSON decoding, while done tasks receive a new unique open task at
// that exact position.
// Called by validateGeneratedJSON in area_content_validation.go for Home documents.
func validateMaintenanceTasksJSON(originalJSON string, updatedJSON string) error {
	var original maintenanceTasksDocument
	if err := decodeStrictJSONDocument("original maintenance tasks", originalJSON, &original); err != nil {
		return err
	}
	if err := validateMaintenanceTasksDocument("original maintenance tasks", original, true); err != nil {
		return err
	}

	var updated maintenanceTasksDocument
	if err := decodeStrictJSONDocument("maintenance tasks", updatedJSON, &updated); err != nil {
		return err
	}
	// Returned entries may be copied legacy open tasks, so this first pass checks
	// their shared identity and open-state rules without demanding replacement-only
	// fields. The index-aware loop below requires the complete shape only where a
	// completed source task is actually being replaced.
	if err := validateMaintenanceTasksDocument("maintenance tasks", updated, false); err != nil {
		return err
	}

	// A replacement has meaning only when it maps one-for-one to an existing
	// Home row. Checking the array length before inspecting entries also makes
	// every following index comparison safe and easy to read.
	if len(updated.Entries) != len(original.Entries) {
		return fmt.Errorf(
			"maintenance tasks must keep %d entries, got %d",
			len(original.Entries),
			len(updated.Entries),
		)
	}

	// Original identifiers form the existing queue identity. A completed task's
	// replacement must introduce a genuinely new id instead of recycling another
	// task's identifier, which could make the Review UI address the wrong row.
	originalIDs := make(map[string]struct{}, len(original.Entries))
	for _, task := range original.Entries {
		originalIDs[task.ID] = struct{}{}
	}

	for index, originalTask := range original.Entries {
		updatedTask := updated.Entries[index]
		taskNumber := index + 1

		if originalTask.State == "open" {
			// reflect.DeepEqual compares the complete typed value, including each
			// metadata item and its attention value. JSON whitespace and object-key
			// order do not matter, but every stored field value does.
			if !reflect.DeepEqual(updatedTask, originalTask) {
				return fmt.Errorf(
					"maintenance task %d was open and must remain unchanged",
					taskNumber,
				)
			}
			continue
		}

		// Completed tasks are intentionally the only positions that may change.
		// Their returned state was already checked as open above; here we require a
		// complete replacement with a new identifier so the Review screen receives
		// a fully usable new task rather than another partial legacy record.
		if err := validateMaintenanceTaskReplacement(taskNumber, updatedTask); err != nil {
			return err
		}
		if updatedTask.ID == originalTask.ID {
			return fmt.Errorf(
				"maintenance task %d replacement id must differ from completed task id %q",
				taskNumber,
				originalTask.ID,
			)
		}
		if _, alreadyExisted := originalIDs[updatedTask.ID]; alreadyExisted {
			return fmt.Errorf(
				"maintenance task %d replacement id %q must be new",
				taskNumber,
				updatedTask.ID,
			)
		}
	}

	return nil
}

// validateMaintenanceTasksDocument checks the fields shared by stored and
// generated Home documents. allowDone is true only for the original document:
// completed tasks are valid input, but the generated queue must consist solely
// of unfinished open tasks ready to appear in the Review screen. Optional
// fields are intentionally allowed here because an updated open task may be an
// exact copy of a legacy task; replacement-only completeness is enforced later.
// Called by validateMaintenanceTasksJSON in area_content_validation.go for original and generated Home documents.
func validateMaintenanceTasksDocument(
	documentName string,
	document maintenanceTasksDocument,
	allowDone bool,
) error {
	if len(document.Entries) == 0 {
		return fmt.Errorf("%s must contain at least 1 entry", documentName)
	}

	taskIDs := make(map[string]struct{}, len(document.Entries))
	for index, task := range document.Entries {
		taskNumber := index + 1
		if err := validateMaintenanceTaskID(documentName, taskNumber, task.ID); err != nil {
			return err
		}
		if _, duplicate := taskIDs[task.ID]; duplicate {
			return fmt.Errorf("%s entry %d repeats id %q", documentName, taskNumber, task.ID)
		}
		taskIDs[task.ID] = struct{}{}

		if strings.TrimSpace(task.Title) == "" {
			return fmt.Errorf("%s entry %d title is required", documentName, taskNumber)
		}
		if task.State != "open" && (task.State != "done" || !allowDone) {
			return fmt.Errorf(
				"%s entry %d state must be %s",
				documentName,
				taskNumber,
				maintenanceTaskAllowedStates(allowDone),
			)
		}
		if task.Metadata.Present {
			if err := validateMaintenanceTaskMetadata(
				documentName,
				taskNumber,
				task.Metadata.Items,
				false,
			); err != nil {
				return err
			}
		}
	}

	return nil
}

// validateMaintenanceTaskID keeps stored identity literal. Trimming an id
// would hide a model mistake such as turning "filter-1" into " filter-1 ",
// which could otherwise bypass the replacement-id comparison and duplicate
// checks. Internal whitespace remains valid and is preserved exactly.
// Called by validateMaintenanceTasksDocument in area_content_validation.go for each Home task.
func validateMaintenanceTaskID(documentName string, taskNumber int, id string) error {
	trimmedID := strings.TrimSpace(id)
	if trimmedID == "" {
		return fmt.Errorf("%s entry %d id is required", documentName, taskNumber)
	}
	if id != trimmedID {
		return fmt.Errorf(
			"%s entry %d id must not have leading or trailing whitespace",
			documentName,
			taskNumber,
		)
	}
	return nil
}

// validateMaintenanceTaskReplacement applies the complete new-task contract to
// an output position that previously held a done task. Copied open positions do
// not use this function because their legacy optional fields must survive
// untouched.
// Called by validateMaintenanceTasksJSON in area_content_validation.go for positions that replace completed tasks.
func validateMaintenanceTaskReplacement(taskNumber int, task maintenanceTask) error {
	if !task.Details.Present {
		return fmt.Errorf("maintenance task %d replacement details is required", taskNumber)
	}
	if !task.Href.Present {
		return fmt.Errorf("maintenance task %d replacement href is required", taskNumber)
	}
	if !task.Metadata.Present {
		return fmt.Errorf("maintenance task %d replacement metadata is required", taskNumber)
	}
	return validateMaintenanceTaskMetadata(
		"maintenance task replacement",
		taskNumber,
		task.Metadata.Items,
		true,
	)
}

// validateMaintenanceTaskMetadata checks the compact details shown beneath a
// Home task. Attention is optional only for copied legacy metadata; new task
// replacements must provide it because they are created against the current
// complete schema.
// Called by validateMaintenanceTasksDocument and validateMaintenanceTaskReplacement in area_content_validation.go.
func validateMaintenanceTaskMetadata(
	documentName string,
	taskNumber int,
	metadataItems []maintenanceTaskMetadata,
	requireAttention bool,
) error {
	if len(metadataItems) < 1 || len(metadataItems) > 2 {
		return fmt.Errorf(
			"%s entry %d must contain 1 or 2 metadata items, got %d",
			documentName,
			taskNumber,
			len(metadataItems),
		)
	}
	for metadataIndex, metadata := range metadataItems {
		if strings.TrimSpace(metadata.Label) == "" {
			return fmt.Errorf(
				"%s entry %d metadata item %d label is required",
				documentName,
				taskNumber,
				metadataIndex+1,
			)
		}
		if strings.TrimSpace(metadata.Value) == "" {
			return fmt.Errorf(
				"%s entry %d metadata item %d value is required",
				documentName,
				taskNumber,
				metadataIndex+1,
			)
		}
		if requireAttention && !metadata.Attention.Present {
			return fmt.Errorf(
				"%s entry %d metadata item %d attention is required",
				documentName,
				taskNumber,
				metadataIndex+1,
			)
		}
	}
	return nil
}

// maintenanceTaskAllowedStates produces the field-specific error wording for
// the two document roles without duplicating the state checks above.
// Called by validateMaintenanceTasksDocument in area_content_validation.go to describe valid task states.
func maintenanceTaskAllowedStates(allowDone bool) string {
	if allowDone {
		return "open or done"
	}
	return "open"
}

// decodeStrictJSONDocument parses exactly one JSON value and rejects every
// property not represented by the destination struct. The second Decode call
// must reach end-of-file; otherwise text such as two adjacent JSON objects could
// pass the first decode and be saved as an invalid document boundary.
// Called by the document validators in area_content_validation.go.
func decodeStrictJSONDocument(documentName string, documentJSON string, destination any) error {
	decoder := json.NewDecoder(strings.NewReader(strings.TrimSpace(documentJSON)))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("parse %s json: %w", documentName, err)
	}

	if err := ensureJSONDecoderEOF(decoder); err != nil {
		return fmt.Errorf("parse %s json trailing content: %w", documentName, err)
	}
	return nil
}

// ensureJSONDecoderEOF confirms that a decoder consumed one complete JSON
// value. Both document decoding and custom metadata decoding use it so neither
// accepts a valid value followed by unrelated JSON text.
// Called by decodeStrictJSONDocument and optionalMaintenanceTaskMetadata.UnmarshalJSON in area_content_validation.go.
func ensureJSONDecoderEOF(decoder *json.Decoder) error {
	var trailingValue any
	if err := decoder.Decode(&trailingValue); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}

// validateWeeklyWorkoutRoutineJSON mirrors the Health response schema before a
// generated plan reaches Supabase. Each entry becomes one visible day in the
// Review screen, so seven complete entries and two metadata values per entry
// are required even when the previously stored document used a looser shape.
// Called by validateGeneratedJSON in area_content_validation.go for Health documents.
func validateWeeklyWorkoutRoutineJSON(updatedJSON string, now time.Time) error {
	var document weeklyWorkoutRoutineDocument
	if err := decodeStrictJSONDocument("weekly workout routine", updatedJSON, &document); err != nil {
		return err
	}

	if _, err := time.Parse("2006-01-02", document.LastUpdated); err != nil {
		return fmt.Errorf("weekly workout routine last_updated must be a YYYY-MM-DD date: %w", err)
	}
	// The plan is generated for the runner's current local date, not merely any
	// well-formed date. Requiring the exact value prevents a stale model plan
	// from replacing the Health document after prompt generation succeeded.
	expectedLastUpdated := now.Format("2006-01-02")
	if document.LastUpdated != expectedLastUpdated {
		return fmt.Errorf(
			"weekly workout routine last_updated must equal current date %s, got %q",
			expectedLastUpdated,
			document.LastUpdated,
		)
	}
	if len(document.Entries) != 7 {
		return fmt.Errorf("weekly workout routine must contain 7 entries, got %d", len(document.Entries))
	}

	entryIDs := make(map[string]struct{}, len(document.Entries))
	for index, entry := range document.Entries {
		entryNumber := index + 1
		entry.ID = strings.TrimSpace(entry.ID)
		if entry.ID == "" {
			return fmt.Errorf("weekly workout routine entry %d id is required", entryNumber)
		}
		if _, duplicate := entryIDs[entry.ID]; duplicate {
			return fmt.Errorf("weekly workout routine entry %d repeats id %q", entryNumber, entry.ID)
		}
		entryIDs[entry.ID] = struct{}{}

		if strings.TrimSpace(entry.Title) == "" {
			return fmt.Errorf("weekly workout routine entry %d title is required", entryNumber)
		}
		if strings.TrimSpace(entry.Details) == "" {
			return fmt.Errorf("weekly workout routine entry %d details are required", entryNumber)
		}
		if entry.Href == nil {
			return fmt.Errorf("weekly workout routine entry %d href is required", entryNumber)
		}
		if entry.State != "open" && entry.State != "done" {
			return fmt.Errorf(
				"weekly workout routine entry %d state must be open or done",
				entryNumber,
			)
		}
		if len(entry.Metadata) != 2 {
			return fmt.Errorf(
				"weekly workout routine entry %d must contain 2 metadata items, got %d",
				entryNumber,
				len(entry.Metadata),
			)
		}
		for metadataIndex, metadata := range entry.Metadata {
			if strings.TrimSpace(metadata.Label) == "" {
				return fmt.Errorf(
					"weekly workout routine entry %d metadata item %d label is required",
					entryNumber,
					metadataIndex+1,
				)
			}
			if strings.TrimSpace(metadata.Value) == "" {
				return fmt.Errorf(
					"weekly workout routine entry %d metadata item %d value is required",
					entryNumber,
					metadataIndex+1,
				)
			}
			if metadata.Attention == nil {
				return fmt.Errorf(
					"weekly workout routine entry %d metadata item %d attention is required",
					entryNumber,
					metadataIndex+1,
				)
			}
		}
	}

	return nil
}

// validateWeeklyMealRecommendationsJSON mirrors the Meals response schema and
// adds the ordering rule described there. The frontend derives each row id from
// its weekday, so requiring Sunday through Saturday exactly once prevents two
// recommendations from receiving the same React identity.
// Called by validateGeneratedJSON in area_content_validation.go for Meals documents.
func validateWeeklyMealRecommendationsJSON(updatedJSON string, now time.Time) error {
	var document weeklyMealRecommendationsDocument
	if err := decodeStrictJSONDocument("weekly meal recommendations", updatedJSON, &document); err != nil {
		return err
	}

	weekStart, err := time.Parse("2006-01-02", document.WeekStarting)
	if err != nil {
		return fmt.Errorf("weekly meal recommendations week_starting must be a YYYY-MM-DD date: %w", err)
	}
	if weekStart.Weekday() != time.Sunday {
		return fmt.Errorf(
			"weekly meal recommendations week_starting must be a Sunday, got %s",
			weekStart.Weekday(),
		)
	}

	// A Sunday-shaped date can still be from last or next week. The prompt and
	// scheduler both use startOfSundayWeek, so use that same local-calendar rule
	// here before a generated plan is allowed to replace the stored Meals plan.
	expectedWeekStart := startOfSundayWeek(now).Format("2006-01-02")
	if document.WeekStarting != expectedWeekStart {
		return fmt.Errorf(
			"weekly meal recommendations week_starting must equal target week %s, got %q",
			expectedWeekStart,
			document.WeekStarting,
		)
	}

	expectedDays := [...]string{
		"Sunday",
		"Monday",
		"Tuesday",
		"Wednesday",
		"Thursday",
		"Friday",
		"Saturday",
	}
	if len(document.Recommendations) != len(expectedDays) {
		return fmt.Errorf(
			"weekly meal recommendations must contain 7 recommendations, got %d",
			len(document.Recommendations),
		)
	}

	for index, recommendation := range document.Recommendations {
		recommendationNumber := index + 1
		if recommendation.Day != expectedDays[index] {
			return fmt.Errorf(
				"weekly meal recommendation %d day must be %s, got %q",
				recommendationNumber,
				expectedDays[index],
				recommendation.Day,
			)
		}
		if strings.TrimSpace(recommendation.Title) == "" {
			return fmt.Errorf("weekly meal recommendation %d title is required", recommendationNumber)
		}
		if strings.TrimSpace(recommendation.Description) == "" {
			return fmt.Errorf("weekly meal recommendation %d description is required", recommendationNumber)
		}

		if recommendation.Tags == nil {
			return fmt.Errorf("weekly meal recommendation %d tags are required", recommendationNumber)
		}

		tags := make(map[string]struct{}, len(*recommendation.Tags))
		for tagIndex, tag := range *recommendation.Tags {
			tag = strings.TrimSpace(tag)
			if tag == "" {
				return fmt.Errorf(
					"weekly meal recommendation %d tag %d is required",
					recommendationNumber,
					tagIndex+1,
				)
			}
			if _, duplicate := tags[tag]; duplicate {
				return fmt.Errorf(
					"weekly meal recommendation %d repeats tag %q",
					recommendationNumber,
					tag,
				)
			}
			tags[tag] = struct{}{}
		}
	}

	return nil
}

// validateJSONStructure checks that the LLM returned valid JSON with the same
// broad schema as the original document. It allows values to change, and arrays
// may grow or shrink, but object keys and value types must stay consistent.
// Called by validateGeneratedJSON in area_content_validation.go for content types without a dedicated contract.
func validateJSONStructure(originalJSON string, updatedJSON string) error {
	// Unmarshal means "parse JSON bytes into a Go value." Because originalValue is
	// type any, the JSON package uses generic Go containers: objects become
	// map[string]any, arrays become []any, and numbers become float64.
	var originalValue any
	err := json.Unmarshal([]byte(strings.TrimSpace(originalJSON)), &originalValue)
	if err != nil {
		return fmt.Errorf("parse original json: %w", err)
	}

	// Parse the model's response the same way so the two generic JSON trees can
	// be compared without needing a custom Go struct for each JSON file type.
	var updatedValue any
	err = json.Unmarshal([]byte(strings.TrimSpace(updatedJSON)), &updatedValue)
	if err != nil {
		return fmt.Errorf("parse updated json: %w", err)
	}

	return compareJSONStructure("$", originalValue, updatedValue)
}

// jsonKind gives validation errors readable type names instead of Go's
// reflection-heavy wording. For example, callers see "changed from object to
// string" when the model replaces a JSON object with plain text.
// Called by the generic same-structure validators in area_content_validation.go to format JSON type errors.
func jsonKind(value any) string {
	switch value.(type) {
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case string:
		return "string"
	case float64:
		return "number"
	case bool:
		return "boolean"
	case nil:
		return "null"
	default:
		return "unknown"
	}
}

// compareJSONStructure walks both parsed JSON documents at the same time. The
// path string records where a mismatch happened, such as
// "$.week[2].exercises[0].sets".
// Called by validateJSONStructure and recursively by the generic object and array validators in area_content_validation.go.
func compareJSONStructure(path string, original any, updated any) error {
	// This switch looks at the original value first because the original JSON is
	// the schema we trust. Each branch then checks whether the updated value is
	// still the same kind of JSON value.
	switch originalTyped := original.(type) {
	case map[string]any:
		// A JSON object was parsed as map[string]any. Delegate to the object
		// helper so key-by-key validation stays separate from array validation.
		return compareJSONObjectStructure(path, originalTyped, updated)
	case []any:
		// A JSON array was parsed as []any. Delegate to the array helper so item
		// shape validation can allow added or removed entries.
		return compareJSONArrayStructure(path, originalTyped, updated)
	case string:
		// Type assertion asks Go: "is updated also a string?" The blank identifier
		// ignores the actual string value because only the type matters here.
		_, ok := updated.(string)
		if !ok {
			return fmt.Errorf("%s changed from string to %s", path, jsonKind(updated))
		}
	case float64:
		// The encoding/json package parses all generic JSON numbers as float64,
		// so a number in the original document must still be a float64 here.
		_, ok := updated.(float64)
		if !ok {
			return fmt.Errorf("%s changed from number to %s", path, jsonKind(updated))
		}
	case bool:
		// Booleans represent JSON true/false values. The actual true/false value
		// may change, but it must remain a boolean.
		_, ok := updated.(bool)
		if !ok {
			return fmt.Errorf("%s changed from boolean to %s", path, jsonKind(updated))
		}
	case nil:
		// JSON null parses as nil. If the original field was null, the updated
		// field must also stay null for this generic structure check to pass.
		if updated != nil {
			return fmt.Errorf("%s changed from null to %s", path, jsonKind(updated))
		}
	}

	return nil
}

// compareJSONObjectStructure checks a JSON object. Objects are the strictest
// part of the validation because downstream code often depends on exact keys
// like "day", "focus", and "exercises" being present after an LLM update.
// Called by compareJSONStructure in area_content_validation.go for JSON objects.
func compareJSONObjectStructure(path string, original map[string]any, updated any) error {
	// Confirm the updated value is also a JSON object before checking individual
	// keys. If it is not an object, there is no safe way to look up fields on it.
	updatedObject, ok := updated.(map[string]any)
	if !ok {
		return fmt.Errorf("%s changed from object to %s", path, jsonKind(updated))
	}

	// A different number of keys means the LLM added or removed fields. This
	// generic updater rejects that because callers expect the original schema to
	// survive the update.
	if len(original) != len(updatedObject) {
		return fmt.Errorf("%s object keys changed", path)
	}

	// Walk every key from the original object. For each original key, this loop:
	// 1. Looks for the same key in the updated object.
	// 2. Fails immediately if the updated object is missing that key.
	// 3. Recursively compares the original and updated values for that key.
	// For example, if key is "week", the recursive call checks the structure of
	// the updated "week" array.
	for key, originalChild := range original {
		// updatedChild is the value from the LLM output at the same object key.
		// ok is false when the LLM removed or renamed that key.
		updatedChild, ok := updatedObject[key]
		if !ok {
			return fmt.Errorf("%s.%s key is missing", path, key)
		}

		// Recurse into nested objects, arrays, or primitive values. The path adds
		// ".key" so any error message points to the exact nested location.
		err := compareJSONStructure(path+"."+key, originalChild, updatedChild)
		if err != nil {
			return err
		}
	}

	return nil
}

// compareJSONArrayStructure checks a JSON array. Arrays represent repeatable
// data such as days, exercises, or loop items, so their length may change when
// the prompt asks to add or remove entries. Each entry still needs to look like
// the same kind of item.
// Called by compareJSONStructure in area_content_validation.go for JSON arrays.
func compareJSONArrayStructure(path string, original []any, updated any) error {
	// Confirm the updated value is also an array before walking its items.
	// Without this check, code below would panic when treating it like []any.
	updatedArray, ok := updated.([]any)
	if !ok {
		return fmt.Errorf("%s changed from array to %s", path, jsonKind(updated))
	}

	// If either side is empty, there is no item shape to compare. The array type
	// itself was already checked above, so this is acceptable for a generic
	// updater.
	if len(original) == 0 || len(updatedArray) == 0 {
		return nil
	}

	// Walk every item returned by the LLM. The loop uses the updated array length
	// because newly added items also need validation before the JSON is saved.
	for index, updatedChild := range updatedArray {
		// By default, compare a new or extra updated item against the first
		// original item. The first original item acts as the template for what one
		// item in this array should look like.
		originalChild := original[0]

		// If the original array had an item at the same index, use that matching
		// original item instead. This handles arrays where different positions
		// have slightly different shapes.
		if index < len(original) {
			originalChild = original[index]
		}

		// Recurse into the selected original item and this updated item. The path
		// adds "[index]" so an error can identify the exact array element that
		// changed shape.
		err := compareJSONStructure(fmt.Sprintf("%s[%d]", path, index), originalChild, updatedChild)
		if err != nil {
			return err
		}
	}

	return nil
}
