// area_prompt_http_test.go tests the browser-facing area-update-prompt feature
// through serverApplication's real ServeMux and the focused in-memory SQL driver.
// These tests protect the public API's safe errors, route precedence, exact prompt
// text, and the boundary that saving a prompt does not run an area update.
package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// Called by Go's test runner. TestAreaUpdatePromptGETReturnsEnabledPrompt
// verifies the generic read route for an area outside the weekly updater's fixed
// Home, Health, and Meals targets. The stored row is enabled, so the browser
// receives its full identifiers and text.
func TestAreaUpdatePromptGETReturnsEnabledPrompt(t *testing.T) {
	state := &promptStorageState{
		storedPrompts: map[string]promptStorageRow{
			promptStorageKey("mail", "inbox_summary"): {
				prompt:  "Summarize urgent mail without omitting deadlines.",
				enabled: true,
			},
		},
	}
	handler := newPromptAPIHandler(t, state, nil)
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/areas/mail/inbox_summary/prompt",
		nil,
	)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if response.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q, want JSON", response.Header().Get("Content-Type"))
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", response.Header().Get("Cache-Control"))
	}

	var updatePrompt AreaUpdatePrompt
	if err := json.NewDecoder(response.Body).Decode(&updatePrompt); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if updatePrompt.AreaID != "mail" || updatePrompt.ContentType != "inbox_summary" {
		t.Fatalf("response identifiers = %q/%q, want mail/inbox_summary", updatePrompt.AreaID, updatePrompt.ContentType)
	}
	if updatePrompt.Prompt != "Summarize urgent mail without omitting deadlines." {
		t.Fatalf("response prompt = %q, want stored text", updatePrompt.Prompt)
	}
}

// Called by Go's test runner. TestAreaUpdatePromptGETHidesMissingAndDisabledRows
// verifies the public 404 boundary. The interface receives the same safe result
// whether no row exists yet or a row has been disabled, so neither state exposes
// database details.
func TestAreaUpdatePromptGETHidesMissingAndDisabledRows(t *testing.T) {
	tests := []struct {
		name  string
		state *promptStorageState
	}{
		{
			name:  "missing prompt",
			state: &promptStorageState{},
		},
		{
			name: "disabled prompt",
			state: &promptStorageState{
				storedPrompts: map[string]promptStorageRow{
					promptStorageKey("home", "maintenance_tasks"): {
						prompt:  "Private dormant prompt.",
						enabled: false,
					},
				},
			},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			handler := newPromptAPIHandler(t, test.state, nil)
			request := httptest.NewRequest(
				http.MethodGet,
				"/api/areas/home/maintenance_tasks/prompt",
				nil,
			)
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusNotFound)
			}
			body := response.Body.String()
			if !strings.Contains(body, "area update prompt is not configured") {
				t.Fatalf("body = %q, want safe missing-prompt message", body)
			}
			if strings.Contains(body, "Private dormant prompt") {
				t.Fatalf("body exposed stored prompt text: %q", body)
			}
		})
	}
}

// Called by Go's test runner.
// TestAreaUpdatePromptPUTCreatesOrUpdatesWithoutRegeneratingContent verifies both
// persistence paths behind one generic endpoint. The application has no updater
// configured; a successful save therefore proves this route changes only prompt
// storage and never attempts an area-content regeneration.
func TestAreaUpdatePromptPUTCreatesOrUpdatesWithoutRegeneratingContent(t *testing.T) {
	tests := []struct {
		name        string
		state       *promptStorageState
		areaID      string
		contentType string
		prompt      string
		wantCreate  bool
	}{
		{
			name:        "creates a new prompt",
			state:       &promptStorageState{},
			areaID:      "reading",
			contentType: "reading_queue",
			prompt:      "\n  Keep a varied reading queue.\n",
			wantCreate:  true,
		},
		{
			name: "updates and reenables a disabled prompt",
			state: &promptStorageState{
				storedPrompts: map[string]promptStorageRow{
					promptStorageKey("home", "maintenance_tasks"): {
						prompt:  "Old prompt.",
						enabled: false,
					},
				},
			},
			areaID:      "home",
			contentType: "maintenance_tasks",
			prompt:      "Include annual safety checks.",
			wantCreate:  false,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			handler := newPromptAPIHandler(t, test.state, nil)
			body := fmt.Sprintf(`{"prompt":%q}`, test.prompt)
			request := httptest.NewRequest(
				http.MethodPut,
				"/api/areas/"+test.areaID+"/"+test.contentType+"/prompt",
				strings.NewReader(body),
			)
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
			}
			var savedPrompt AreaUpdatePrompt
			if err := json.NewDecoder(response.Body).Decode(&savedPrompt); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if savedPrompt.AreaID != test.areaID || savedPrompt.ContentType != test.contentType {
				t.Fatalf("response identifiers = %q/%q, want %q/%q", savedPrompt.AreaID, savedPrompt.ContentType, test.areaID, test.contentType)
			}
			if savedPrompt.Prompt != test.prompt {
				t.Fatalf("response prompt = %q, want exact text %q", savedPrompt.Prompt, test.prompt)
			}
			if test.state.created != test.wantCreate {
				t.Fatalf("created = %t, want %t", test.state.created, test.wantCreate)
			}
			stored := test.state.storedPrompts[promptStorageKey(test.areaID, test.contentType)]
			if !stored.enabled {
				t.Fatal("saved prompt is disabled, want enabled")
			}
			if stored.prompt != test.prompt {
				t.Fatalf("stored prompt = %q, want exact text %q", stored.prompt, test.prompt)
			}
		})
	}
}

// Called by Go's test runner. TestAreaUpdatePromptPUTRejectsInvalidBodies
// verifies every rejected payload shape before storage is touched. Each body is
// invalid under the single-object {"prompt":"..."} contract, including a
// syntactically valid object followed by extra JSON.
func TestAreaUpdatePromptPUTRejectsInvalidBodies(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "unknown field",
			body: `{"prompt":"Keep this.","extra":true}`,
		},
		{
			name: "trailing JSON",
			body: `{"prompt":"Keep this."} {"prompt":"Ignore this."}`,
		},
		{
			name: "malformed JSON",
			body: `{"prompt":`,
		},
		{
			name: "not an object",
			body: `[]`,
		},
		{
			name: "blank prompt",
			body: `{"prompt":" \n\t "}`,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			handler := newPromptAPIHandler(t, &promptStorageState{}, nil)
			request := httptest.NewRequest(
				http.MethodPut,
				"/api/areas/mail/inbox_summary/prompt",
				strings.NewReader(test.body),
			)
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
			}
			if !strings.Contains(response.Body.String(), "invalid area update prompt") {
				t.Fatalf("body = %q, want safe validation message", response.Body.String())
			}
		})
	}
}

// Called by Go's test runner. TestAreaUpdatePromptRoutesKeepInternalFailuresPrivate
// verifies storage errors stay in server logs for reads and saves. It also
// confirms unsupported methods receive the prompt route's 405 response ahead of
// the production frontend fallback.
func TestAreaUpdatePromptRoutesKeepInternalFailuresPrivate(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		body       string
		state      *promptStorageState
		frontend   fs.FS
		wantStatus int
		private    string
	}{
		{
			name:   "read database failure",
			method: http.MethodGet,
			state: &promptStorageState{
				queryError: errors.New("private database hostname"),
			},
			wantStatus: http.StatusInternalServerError,
			private:    "private database hostname",
		},
		{
			name:   "save database failure",
			method: http.MethodPut,
			body:   `{"prompt":"Keep this."}`,
			state: &promptStorageState{
				queryError: errors.New("private SQL constraint"),
			},
			wantStatus: http.StatusInternalServerError,
			private:    "private SQL constraint",
		},
		{
			name:   "unsupported method with production frontend fallback",
			method: http.MethodPost,
			state:  &promptStorageState{},
			frontend: fstest.MapFS{
				"index.html": {Data: []byte("<!doctype html><title>Personal AI</title>")},
			},
			wantStatus: http.StatusMethodNotAllowed,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			handler := newPromptAPIHandler(t, test.state, test.frontend)
			request := httptest.NewRequest(
				test.method,
				"/api/areas/mail/inbox_summary/prompt",
				strings.NewReader(test.body),
			)
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, test.wantStatus)
			}
			if test.wantStatus == http.StatusMethodNotAllowed {
				if allow := response.Header().Get("Allow"); allow != "GET, PUT" {
					t.Fatalf("Allow = %q, want GET, PUT", allow)
				}
			}
			if test.private != "" && strings.Contains(response.Body.String(), test.private) {
				t.Fatalf("body exposed an internal storage error: %q", response.Body.String())
			}
		})
	}
}

// Called by the area-prompt HTTP tests in this file. newPromptAPIHandler
// connects the real route and storage functions to the focused in-memory SQL
// driver from storage_test.go. Keeping the production serverApplication shape
// proves handlers use their concrete db field rather than a test-only callback.
// frontendFiles lets a route test reproduce Cloud Run's API-and-React
// configuration, while Cleanup releases each pool.
func newPromptAPIHandler(t *testing.T, state *promptStorageState, frontendFiles fs.FS) http.Handler {
	t.Helper()

	database := sql.OpenDB(promptStorageConnector{state: state})
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("close prompt route database: %v", err)
		}
	})
	return (&serverApplication{db: database, frontendFiles: frontendFiles}).newHTTPHandler()
}
