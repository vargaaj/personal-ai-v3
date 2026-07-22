package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// updater.go owns the generic "JSON in, JSON out" LLM update flow.
//
// Callers are responsible for loading a JSON file from storage and writing the
// final updated text back to disk. This file stays focused on the middle of that
// workflow: send the original JSON plus update instructions to the LLM, then
// reject the response if it no longer matches the original document shape.

// updateJSON sends one JSON document to the LLM with instructions for how to
// change it. The returned string is still JSON text because the next storage
// step will save that text back to a file.
func updateJSON(ctx context.Context, prompt string, originalJSON string) (string, error) {
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
	chat, err := sendChatMessageWithRetry(ctx, prompt, originalJSON)
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
func sendChatMessageWithRetry(ctx context.Context, prompt string, originalJSON string) (chatResponse, error) {
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
		chat, err := sendChatMessage(ctx, prompt, originalJSON)
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
