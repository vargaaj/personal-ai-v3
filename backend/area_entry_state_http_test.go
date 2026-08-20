// area_entry_state_http_test.go verifies the browser-facing completion-control
// API through serverApplication's real mux and the focused in-memory area
// storage driver. These tests protect strict input, safe status codes, route
// precedence, and the revision returned after an entry-state save.
package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// TestAreaEntryStatePUTSavesSupportedEntry verifies the complete public success
// path. The document starts at revision 3, so returning revision 4 proves the
// handler used the storage function's conditional save rather than echoing URL
// input back to the browser.
func TestAreaEntryStatePUTSavesSupportedEntry(t *testing.T) {
	state := &entryStateStorageState{documents: map[string]entryStateStorageRow{
		entryStateStorageKey("health", "weekly_workout_routine"): entryStateFixtureDocument(),
	}}
	handler := newAreaEntryStateAPIHandler(t, state, nil)
	request := httptest.NewRequest(
		http.MethodPut,
		"/api/areas/health/weekly_workout_routine/entries/monday/state",
		strings.NewReader(`{"state":"done"}`),
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

	// Keep a copy because json.Decoder consumes the recorder's buffer. The typed
	// decode checks values, while the raw-object decode below checks the exact
	// public field set shared with the frontend worker.
	responseBody := append([]byte(nil), response.Body.Bytes()...)
	var saved AreaEntryState
	if err := json.NewDecoder(bytes.NewReader(responseBody)).Decode(&saved); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if saved.AreaID != "health" || saved.ContentType != "weekly_workout_routine" || saved.EntryID != "monday" {
		t.Fatalf("response identifiers = %#v, want health/weekly_workout_routine/monday", saved)
	}
	if saved.State != "done" || saved.Revision != 4 {
		t.Fatalf("response state/revision = %q/%d, want done/4", saved.State, saved.Revision)
	}
	var responseFields map[string]json.RawMessage
	if err := json.Unmarshal(responseBody, &responseFields); err != nil {
		t.Fatalf("decode response fields: %v", err)
	}
	if len(responseFields) != 5 {
		t.Fatalf("response field count = %d, want exactly 5 fields: %s", len(responseFields), responseBody)
	}
	for _, requiredField := range []string{"area_id", "content_type", "entry_id", "state", "revision"} {
		if _, found := responseFields[requiredField]; !found {
			t.Fatalf("response = %s, want field %q", responseBody, requiredField)
		}
	}
}

// TestAreaEntryStatePUTRejectsInvalidBodies confirms the endpoint accepts only
// the exact one-field state object shared with the frontend worker. Invalid
// payloads must fail before the storage driver's SELECT or UPDATE is reached.
func TestAreaEntryStatePUTRejectsInvalidBodies(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
	}{
		{name: "unknown field", body: `{"state":"done","revision":3}`},
		{name: "trailing JSON", body: `{"state":"done"} {"state":"open"}`},
		{name: "malformed JSON", body: `{"state":`},
		{name: "not an object", body: `[]`},
		{name: "missing state", body: `{}`},
		{name: "unsupported state", body: `{"state":"skipped"}`},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			state := &entryStateStorageState{documents: map[string]entryStateStorageRow{
				entryStateStorageKey("home", "maintenance_tasks"): entryStateFixtureDocument(),
			}}
			handler := newAreaEntryStateAPIHandler(t, state, nil)
			request := httptest.NewRequest(
				http.MethodPut,
				"/api/areas/home/maintenance_tasks/entries/monday/state",
				strings.NewReader(test.body),
			)
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
			}
			if !strings.Contains(response.Body.String(), "invalid area entry state") {
				t.Fatalf("body = %q, want safe validation message", response.Body.String())
			}
			if state.readQuery != "" || state.updateQuery != "" {
				t.Fatalf("invalid request reached storage: read=%q update=%q", state.readQuery, state.updateQuery)
			}
		})
	}
}

// TestAreaEntryStatePUTRejectsBlankEntryID verifies the URL identifier gets the
// same safe 400 treatment as an invalid body. A percent-encoded space still
// matches ServeMux's segment pattern, so this guard must run in the handler.
func TestAreaEntryStatePUTRejectsBlankEntryID(t *testing.T) {
	state := &entryStateStorageState{}
	handler := newAreaEntryStateAPIHandler(t, state, nil)
	request := httptest.NewRequest(
		http.MethodPut,
		"/api/areas/home/maintenance_tasks/entries/%20/state",
		strings.NewReader(`{"state":"done"}`),
	)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
	}
	if state.readQuery != "" || state.updateQuery != "" {
		t.Fatalf("blank entry id reached storage: read=%q update=%q", state.readQuery, state.updateQuery)
	}
}

// TestAreaEntryStateRoutesReturnSafeExpectedStatuses covers normal missing
// configuration, stale revisions, storage failures, and method precedence when
// the production React fallback is installed on the same mux.
func TestAreaEntryStateRoutesReturnSafeExpectedStatuses(t *testing.T) {
	for _, test := range []struct {
		name       string
		method     string
		path       string
		body       string
		state      *entryStateStorageState
		frontend   fs.FS
		wantStatus int
		private    string
	}{
		{
			name:       "unsupported document",
			method:     http.MethodPut,
			path:       "/api/areas/meals/weekly_meal_recommendations/entries/dinner/state",
			body:       `{"state":"done"}`,
			state:      &entryStateStorageState{},
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "missing entry",
			method:     http.MethodPut,
			path:       "/api/areas/home/maintenance_tasks/entries/missing/state",
			body:       `{"state":"done"}`,
			state:      &entryStateStorageState{documents: map[string]entryStateStorageRow{entryStateStorageKey("home", "maintenance_tasks"): entryStateFixtureDocument()}},
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "revision conflict",
			method:     http.MethodPut,
			path:       "/api/areas/home/maintenance_tasks/entries/monday/state",
			body:       `{"state":"done"}`,
			state:      &entryStateStorageState{documents: map[string]entryStateStorageRow{entryStateStorageKey("home", "maintenance_tasks"): entryStateFixtureDocument()}, forceRevisionConflict: true},
			wantStatus: http.StatusConflict,
		},
		{
			name:       "storage failure remains private",
			method:     http.MethodPut,
			path:       "/api/areas/home/maintenance_tasks/entries/monday/state",
			body:       `{"state":"done"}`,
			state:      &entryStateStorageState{queryError: errors.New("private database hostname")},
			wantStatus: http.StatusInternalServerError,
			private:    "private database hostname",
		},
		{
			name:       "unsupported method with frontend fallback",
			method:     http.MethodPost,
			path:       "/api/areas/home/maintenance_tasks/entries/monday/state",
			state:      &entryStateStorageState{},
			frontend:   fstest.MapFS{"index.html": {Data: []byte("<!doctype html><title>Personal AI</title>")}},
			wantStatus: http.StatusMethodNotAllowed,
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			handler := newAreaEntryStateAPIHandler(t, test.state, test.frontend)
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, test.wantStatus)
			}
			if test.wantStatus == http.StatusMethodNotAllowed {
				if allow := response.Header().Get("Allow"); allow != http.MethodPut {
					t.Fatalf("Allow = %q, want PUT", allow)
				}
			}
			if test.private != "" && strings.Contains(response.Body.String(), test.private) {
				t.Fatalf("body exposed internal storage error: %q", response.Body.String())
			}
		})
	}
}

// newAreaEntryStateAPIHandler connects the production mux and concrete storage
// functions to this test file's in-memory SQL driver. It proves the route is
// registered from main.go instead of testing the handler in isolation.
func newAreaEntryStateAPIHandler(t *testing.T, state *entryStateStorageState, frontendFiles fs.FS) http.Handler {
	t.Helper()

	database := sql.OpenDB(entryStateStorageConnector{state: state})
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("close entry-state route database: %v", err)
		}
	})
	return (&serverApplication{db: database, frontendFiles: frontendFiles}).newHTTPHandler()
}
