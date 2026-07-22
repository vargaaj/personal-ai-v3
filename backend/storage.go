package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

// storage.go contains small filesystem helpers for JSON-backed app state.
// The backend sends the loaded JSON text to an LLM, so this file returns the raw
// JSON string instead of decoding the document into a Go struct.

// loadJSONFile reads a JSON document from disk and returns it as a string. This
// keeps the storage boundary simple: updater.go can send the exact JSON text to
// the LLM, while this function handles file access and basic JSON validation.
func loadJSONFile(path string) (string, error) {
	// Trim the path before using it so a string containing only spaces is treated
	// the same as an empty path.
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("json path is required")
	}

	// ReadFile loads the whole JSON file into memory. These storage files are
	// expected to be small enough that reading them all at once is simpler than
	// streaming line by line.
	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read json file %q: %w", path, err)
	}

	// Trim outer whitespace before validation so a normal trailing newline does
	// not become part of the prompt sent to the LLM.
	jsonText := strings.TrimSpace(string(content))
	if jsonText == "" {
		return "", fmt.Errorf("json file %q is empty", path)
	}

	// Validate before sending the text to the model. The LLM prompt can then
	// assume it is receiving one valid JSON document from storage.
	if !json.Valid([]byte(jsonText)) {
		return "", fmt.Errorf("json file %q is not valid JSON", path)
	}

	return jsonText, nil
}

// loadPromptFile reads a plain-text prompt from disk and returns the trimmed
// prompt text. Prompts are kept in storage so they can be edited without changing
// Go source code.
func loadPromptFile(path string) (string, error) {
	// Trim the path for the same reason loadJSONFile does: a whitespace-only path
	// should fail with a clear validation message instead of a filesystem error.
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("prompt path is required")
	}

	// Read the entire prompt file. Prompt files are small text files, so one
	// ReadFile call is easier to follow than opening and scanning the file.
	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read prompt file %q: %w", path, err)
	}

	// Convert the bytes from disk into a Go string, then trim outer whitespace.
	// This removes incidental blank lines before or after the prompt while keeping
	// the meaningful line breaks inside the prompt.
	promptText := strings.TrimSpace(string(content))
	if promptText == "" {
		return "", fmt.Errorf("prompt file %q is empty", path)
	}

	return promptText, nil
}
