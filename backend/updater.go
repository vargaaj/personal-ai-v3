package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// updater.go owns the "JSON in, JSON out" LLM update flow and the shared
// orchestration used to refresh weekly area documents.
//
// The lower-level updateJSON function sends one document to OpenRouter and
// validates the response. areaUpdateRunner coordinates storage around that call:
// it checks whether Health or Meals is due, loads the prompt only when it will
// actually be used, supplies calendar context, and saves with revision checking.
//
// A scheduled or manual refresh follows this sequence:
//
//  1. run chooses the Health and Meals documents.
//  2. updateTarget loads the current JSON for one document.
//  3. A scheduled run stops there when that document was already updated this
//     week. This is why its prompt is not fetched during ordinary due checks.
//  4. A due or manually forced run loads the matching database prompt.
//  5. buildCalendarUpdatePrompt adds this week's actual dates to that prompt.
//  6. updateJSON asks OpenRouter for replacement JSON and validates its shape.
//  7. saveAreaContent stores it only if another request has not already changed
//     the document's revision.

// areaUpdateTarget identifies one independently stored document that participates
// in the weekly refresh. Keeping the content type explicit prevents a future
// second Health document from being updated merely because it shares an area id.
type areaUpdateTarget struct {
	AreaID      string
	ContentType string

	// ZDR is true only when this document must use Zero Data Retention. Health
	// and Meals are false; a future Email target will set this field to true.
	ZDR bool
}

// weeklyAreaUpdateTargets is the deliberately small initial schedule. Home and
// Reading remain untouched until their refresh cadence and prompts are defined.
var weeklyAreaUpdateTargets = []areaUpdateTarget{
	{AreaID: "health", ContentType: "weekly_workout_routine", ZDR: false},
	{AreaID: "meals", ContentType: "weekly_meal_recommendations", ZDR: false},
}

// errAreaUpdateTargetNotConfigured lets the HTTP layer distinguish an unknown
// area/content pair from an operational database or OpenRouter failure. The
// browser receives a safe 404 without exposing the internal error text.
var errAreaUpdateTargetNotConfigured = errors.New("area update target is not configured")

// areaUpdateStatus describes the visible outcome of one target. The scheduler
// logs these results, while the future manual endpoint can return the same
// stable vocabulary to the frontend.
type areaUpdateStatus string

const (
	areaUpdateStatusUpdated areaUpdateStatus = "updated"
	areaUpdateStatusSkipped areaUpdateStatus = "skipped"
	areaUpdateStatusFailed  areaUpdateStatus = "failed"
)

// AreaUpdateResult reports what happened to one scheduled document. Error stays
// server-only because database and provider errors can contain operational
// details that should never be serialized into a browser response.
type AreaUpdateResult struct {
	AreaID      string           `json:"area_id"`
	ContentType string           `json:"content_type"`
	Status      areaUpdateStatus `json:"status"`
	Revision    int              `json:"revision,omitempty"`
	UpdatedAt   time.Time        `json:"updated_at,omitempty"`
	Error       error            `json:"-"`
}

// areaUpdateRunner holds the concrete Supabase pool used by the update workflow.
// updateTarget calls the named storage and model functions directly, avoiding
// function-type fields that can hide which implementation performs each step.
type areaUpdateRunner struct {
	// db is the same database pool used by the HTTP server.
	db *sql.DB

	// targets identifies the documents this runner should update. Production
	// leaves it empty to use weeklyAreaUpdateTargets.
	targets []areaUpdateTarget
}

// run refreshes every configured target independently. Scheduled calls pass
// force=false so documents already updated during the current Sunday-Saturday week
// are skipped before their prompts are loaded. The manual button will pass
// force=true so a person can intentionally regenerate both sections at any time.
//
// `(runner areaUpdateRunner)` is the method receiver. It means run belongs to an
// areaUpdateRunner value, is called as `runner.run(...)`, and can read the
// dependency fields above through the local name `runner`.
func (runner areaUpdateRunner) run(ctx context.Context, now time.Time, force bool) ([]AreaUpdateResult, error) {
	// Some simple callers and tests may not have a request context. Substitute a
	// context that never cancels so downstream database/model functions still
	// receive the non-nil context required by their APIs.
	if ctx == nil {
		ctx = context.Background()
	}

	// The supplied time decides both whether an update is due and which dates are
	// added to its prompt. Reject the zero value so a missing clock cannot silently
	// produce year-one scheduling context.
	if now.IsZero() {
		return nil, errors.New("update time is required")
	}

	// Every update reads and writes Supabase. Reject a missing pool before any
	// target begins instead of allowing a nil database pointer to reach storage.go.
	if runner.db == nil {
		return nil, errors.New("database is required")
	}

	// Copy the optional runner-specific targets into a local variable. Tests use
	// this field to run one section without also setting up the other section.
	targets := runner.targets

	// An empty target list means "use the application's normal weekly sections,"
	// currently Health and Meals.
	if len(targets) == 0 {
		targets = weeklyAreaUpdateTargets
	}

	// Preallocate enough result capacity for one outcome per target. The length
	// starts at zero because append below adds each completed outcome.
	results := make([]AreaUpdateResult, 0, len(targets))

	// Process Health and Meals one at a time. Sequential model calls avoid sending
	// multiple potentially large personal JSON requests at once.
	for _, target := range targets {
		// A canceled HTTP request or server shutdown should stop before another
		// potentially slow model request begins.
		if ctxError := ctx.Err(); ctxError != nil {
			return results, ctxError
		}

		// updateTarget contains the complete workflow for this one document. It
		// converts document-specific failures into a failed result, allowing the
		// loop to continue to the next independent section.
		results = append(results, runner.updateTarget(ctx, now, force, target))
	}

	// Reaching this point means every configured target produced an updated,
	// skipped, or failed result. A nil top-level error distinguishes those normal
	// per-target outcomes from invalid runner configuration or cancellation.
	return results, nil
}

// runOne updates exactly one configured AreaContent document.
//
// The individual Health and Meals buttons call this method with force=true.
// Looking up the target in weeklyAreaUpdateTargets is important: a browser
// cannot invent an arbitrary area id, content type, or privacy setting and cause
// the server to send that unconfigured document to OpenRouter.
func (runner areaUpdateRunner) runOne(
	ctx context.Context,
	now time.Time,
	force bool,
	areaID string,
	contentType string,
) ([]AreaUpdateResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if now.IsZero() {
		return nil, errors.New("update time is required")
	}

	areaID = strings.TrimSpace(areaID)
	if areaID == "" {
		return nil, errors.New("area id is required")
	}
	contentType = strings.TrimSpace(contentType)
	if contentType == "" {
		return nil, errors.New("content type is required")
	}

	// Tests may provide a smaller target list. Production leaves runner.targets
	// empty and therefore uses the normal Health and Meals configuration.
	targets := runner.targets
	if len(targets) == 0 {
		targets = weeklyAreaUpdateTargets
	}

	for _, target := range targets {
		if target.AreaID != areaID || target.ContentType != contentType {
			continue
		}

		// Validate the database only after finding the target. This allows an
		// unknown URL to return 404 without attempting any database operation.
		if runner.db == nil {
			return nil, errors.New("database is required")
		}

		// Keep the response shape consistent with the existing update-all route:
		// it is an array containing one result instead of a separate object shape.
		return []AreaUpdateResult{
			runner.updateTarget(ctx, now, force, target),
		}, nil
	}

	return nil, fmt.Errorf(
		"%w: %s/%s",
		errAreaUpdateTargetNotConfigured,
		areaID,
		contentType,
	)
}

// updateTarget performs one complete load, generate, and save operation. Errors
// become a failed result instead of stopping the other weekly target; a Health
// provider failure should not prevent Meals from receiving its own update.
func (runner areaUpdateRunner) updateTarget(
	ctx context.Context,
	now time.Time,
	force bool,
	target areaUpdateTarget,
) AreaUpdateResult {
	result := AreaUpdateResult{
		AreaID:      target.AreaID,
		ContentType: target.ContentType,
		Status:      areaUpdateStatusFailed,
	}

	current, err := loadAreaContent(ctx, runner.db, target.AreaID, target.ContentType)
	if err != nil {
		result.Error = fmt.Errorf("load %s/%s content: %w", target.AreaID, target.ContentType, err)
		return result
	}

	// This check intentionally precedes loadPrompt. The hourly scheduler may read
	// lightweight content metadata many times during a week, but it retrieves the
	// potentially long prompt only on the run that will use it.
	if !force && !weeklyAreaUpdateDue(current.UpdatedAt, now) {
		result.Status = areaUpdateStatusSkipped
		result.Revision = current.Revision
		result.UpdatedAt = current.UpdatedAt
		return result
	}

	updatePrompt, err := loadAreaUpdatePrompt(ctx, runner.db, target.AreaID, target.ContentType)
	if err != nil {
		result.Error = fmt.Errorf("load %s/%s update prompt: %w", target.AreaID, target.ContentType, err)
		return result
	}

	promptWithCalendar, err := buildCalendarUpdatePrompt(updatePrompt.Prompt, now)
	if err != nil {
		result.Error = fmt.Errorf("prepare %s/%s update prompt: %w", target.AreaID, target.ContentType, err)
		return result
	}

	// Carry this target's privacy requirement into the model request. Health and
	// Meals use the general free-model fallbacks; a future Email target can use
	// Ling with strict ZDR without changing this orchestration.
	updatedJSON, err := updateJSON(
		ctx,
		promptWithCalendar,
		string(current.Content),
		target.ZDR,
	)
	if err != nil {
		result.Error = fmt.Errorf("generate %s/%s content: %w", target.AreaID, target.ContentType, err)
		return result
	}

	saved, err := saveAreaContent(ctx, runner.db, current, updatedJSON)
	if err != nil {
		result.Error = fmt.Errorf("save %s/%s content: %w", target.AreaID, target.ContentType, err)
		return result
	}

	result.Status = areaUpdateStatusUpdated
	result.Revision = saved.Revision
	result.UpdatedAt = saved.UpdatedAt
	return result
}

// weeklyAreaUpdateDue uses calendar weeks instead of an exact 168-hour duration.
// Sunday begins a new planning week, so content last updated on Saturday becomes
// due on Sunday. Content generated at any point from Sunday through Saturday
// belongs to that current week and is skipped by later scheduled checks.
func weeklyAreaUpdateDue(updatedAt time.Time, now time.Time) bool {
	if updatedAt.IsZero() {
		return true
	}
	return updatedAt.Before(startOfSundayWeek(now))
}

// startOfSundayWeek returns midnight Sunday in now's location. The due check
// uses this boundary to decide whether the stored Health or Meals document came
// from the current Sunday-Saturday planning week. main.go supplies New York
// time, so the boundary follows the application's local calendar.
func startOfSundayWeek(now time.Time) time.Time {
	// Go numbers Sunday as weekday zero, Monday as one, and so on. Converting the
	// weekday directly to an integer therefore also gives the number of calendar
	// days to move backward to reach the most recent Sunday.
	daysSinceSunday := int(now.Weekday())

	// Remove the clock portion before moving backward. For example, a Friday at
	// 10:30 AM becomes Friday at midnight and then moves back five days to Sunday.
	todayAtMidnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	return todayAtMidnight.AddDate(0, 0, -daysSinceSunday)
}

// buildCalendarUpdatePrompt adds changing dates to stable database instructions.
// Prompt rows can therefore describe Health or Meals behavior without being
// edited every week just to replace a date.
func buildCalendarUpdatePrompt(storedPrompt string, now time.Time) (string, error) {
	storedPrompt = strings.TrimSpace(storedPrompt)
	if storedPrompt == "" {
		return "", errors.New("stored update prompt is required")
	}

	weekStart := startOfSundayWeek(now)
	return storedPrompt +
		"\n\nScheduling context:\n" +
		"Current date: " + now.Format("2006-01-02") + "\n" +
		"Target week starts Sunday: " + weekStart.Format("2006-01-02") + "\n" +
		"Generate the complete plan for Sunday through Saturday, including the starting Sunday.", nil
}

// updateJSON sends one JSON document to the LLM with instructions for how to
// change it. The returned string is still JSON text because the next storage
// step will save that text back to a file.
func updateJSON(
	ctx context.Context,
	prompt string,
	originalJSON string,
	zdr bool,
) (string, error) {
	// Trim user-provided inputs at the boundary so the rest of the function can
	// treat blank prompt text or blank JSON content as clear validation errors.
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return "", errors.New("update prompt is required")
	}

	originalJSON = strings.TrimSpace(originalJSON)
	if originalJSON == "" {
		return "", errors.New("original json is required")
	}
	if !json.Valid([]byte(originalJSON)) {
		return "", errors.New("original json is not valid")
	}

	// A nil context is allowed for simple local calls. Converting it to
	// context.Background keeps sendChatMessage from receiving a nil context.
	if ctx == nil {
		ctx = context.Background()
	}

	// sendChatMessageWithRetry builds the final chat request and calls
	// OpenRouter. It retries short-lived network or model-response timeouts
	// before returning an error to main.go.
	chat, err := sendChatMessageWithRetry(ctx, prompt, originalJSON, zdr)
	if err != nil {
		return "", err
	}

	// The LLM response is treated as untrusted text until it parses as JSON and
	// passes the same-shape check against the original document.
	updatedJSON := strings.TrimSpace(chat.Response)
	err = validateJSONStructure(originalJSON, updatedJSON)
	if err != nil {
		return "", err
	}

	return updatedJSON, nil
}

// sendChatMessageWithRetry calls the lower-level chat function and retries
// errors that look temporary. This specifically helps with OpenRouter responses
// that begin successfully but time out while the HTTP client is reading the
// response body.
func sendChatMessageWithRetry(
	ctx context.Context,
	prompt string,
	originalJSON string,
	zdr bool,
) (chatResponse, error) {
	// Keep the retry count small so the command does not hang for a long time.
	// Attempt 1 is the normal call; attempts 2 and 3 are retry calls.
	maxAttempts := 3

	// lastErr stores the most recent failure so we can return the real underlying
	// error if every attempt fails.
	var lastErr error

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		// If the caller's context was canceled, stop immediately. Retrying cannot
		// help when the caller has already said the work should end.
		if ctx.Err() != nil {
			return chatResponse{}, ctx.Err()
		}

		// sendChatMessage makes the actual OpenRouter request. A successful call
		// returns the model name and JSON text response.
		chat, err := sendChatMessage(ctx, prompt, originalJSON, zdr)
		if err == nil {
			fmt.Println("Model used:", chat.Model)
			return chat, nil
		}

		// Save this error in case this is the final attempt or the error is not
		// something retryable.
		lastErr = err

		// Only retry errors that look temporary. For example, a missing API key is
		// not retryable because the same request will fail every time.
		if !isRetryableChatError(err) {
			return chatResponse{}, err
		}

		// If this was the final allowed attempt, leave the loop and return the most
		// recent timeout or network error below.
		if attempt == maxAttempts {
			break
		}

		// Wait briefly before trying again. The wait gets longer each attempt:
		// attempt 1 waits 1 second, attempt 2 waits 2 seconds.
		retryDelay := time.Duration(attempt) * time.Second
		time.Sleep(retryDelay)
	}

	return chatResponse{}, fmt.Errorf("chat request failed after %d attempts: %w", maxAttempts, lastErr)
}

// isRetryableChatError decides whether a failed chat request is worth trying
// again. It returns true for timeout-style errors and false for permanent errors
// such as bad input, missing credentials, or invalid model responses.
func isRetryableChatError(err error) bool {
	// A nil error means there is nothing to retry.
	if err == nil {
		return false
	}

	// context.Canceled means the caller intentionally stopped the work. Retrying
	// would ignore that cancellation request, so it is not retryable.
	if errors.Is(err, context.Canceled) {
		return false
	}

	// context.DeadlineExceeded is Go's standard timeout error. The OpenRouter SDK
	// may wrap it, so errors.Is catches the wrapped form when available.
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	// Some HTTP client timeout errors arrive as plain text instead of a wrapped
	// context.DeadlineExceeded value. Lowercasing makes the checks insensitive to
	// capitalization differences in library error messages.
	errorText := strings.ToLower(err.Error())
	if strings.Contains(errorText, "context deadline exceeded") {
		return true
	}
	if strings.Contains(errorText, "client.timeout") {
		return true
	}
	if strings.Contains(errorText, "timeout") {
		return true
	}

	return false
}

// validateJSONStructure checks that the LLM returned valid JSON with the same
// broad schema as the original document. It allows values to change, and arrays
// may grow or shrink, but object keys and value types must stay consistent.
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
