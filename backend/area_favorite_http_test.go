// area_favorite_http_test.go exercises the focused browser boundary for adding,
// removing, and restoring Thoughtful Suggestions Favorites. It uses the
// in-memory area_content driver from storage_test.go so HTTP status, response
// shape, and revision-safe persistence are verified without a live database.
package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestThoughtfulFavoritePUTCopiesSuggestion verifies the complete public add
// path. Revision 8 proves storage performed the conditional write, while the
// returned title proves the browser did not provide saved Favorite content.
func TestThoughtfulFavoritePUTCopiesSuggestion(t *testing.T) {
	state := thoughtfulFavoriteStorageState(false)
	handler := newAreaFavoriteAPIHandler(t, state, fixedThoughtfulFavoriteHTTPTime)
	request := httptest.NewRequest(
		http.MethodPut,
		"/api/areas/thoughtful/weekly_thoughtful_suggestions/suggestions/leave-note/favorite",
		strings.NewReader(`{"expected_revision":7}`),
	)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", response.Code, http.StatusOK, response.Body.String())
	}
	assertThoughtfulFavoriteResponseHeaders(t, response)

	responseBody := append([]byte(nil), response.Body.Bytes()...)
	var mutation ThoughtfulFavoriteMutation
	if err := json.NewDecoder(bytes.NewReader(responseBody)).Decode(&mutation); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if mutation.AreaID != "thoughtful" ||
		mutation.ContentType != weeklyThoughtfulSuggestionsContentType ||
		mutation.Revision != 8 {
		t.Fatalf("mutation identity/revision = %#v, want thoughtful document revision 8", mutation)
	}
	if mutation.Favorite.SourceSuggestionID != "leave-note" ||
		mutation.Favorite.Title != "Leave a note by her coffee" ||
		mutation.Favorite.SavedAt != "2026-08-20" {
		t.Fatalf("favorite = %#v, want copied suggestion and injected local date", mutation.Favorite)
	}

	// The raw shape protects the snake_case contract consumed by the future data
	// adapter, including the nested source link needed to fill the weekly heart.
	var responseFields map[string]json.RawMessage
	if err := json.Unmarshal(responseBody, &responseFields); err != nil {
		t.Fatalf("decode response fields: %v", err)
	}
	for _, requiredField := range []string{"area_id", "content_type", "favorite", "revision"} {
		if _, found := responseFields[requiredField]; !found {
			t.Fatalf("response = %s, want field %q", responseBody, requiredField)
		}
	}
	if len(responseFields) != 4 {
		t.Fatalf("response field count = %d, want exactly 4: %s", len(responseFields), responseBody)
	}
	var favoriteFields map[string]json.RawMessage
	if err := json.Unmarshal(responseFields["favorite"], &favoriteFields); err != nil {
		t.Fatalf("decode favorite fields: %v", err)
	}
	for _, requiredField := range []string{"id", "source_suggestion_id", "title", "details", "category", "saved_at"} {
		if _, found := favoriteFields[requiredField]; !found {
			t.Fatalf("favorite = %s, want field %q", responseFields["favorite"], requiredField)
		}
	}
	if len(favoriteFields) != 6 {
		t.Fatalf("favorite field count = %d, want exactly 6", len(favoriteFields))
	}
}

// TestThoughtfulFavoriteDELETEReturnsRemovedRecord proves DELETE addresses the
// durable Favorite rather than requiring its source content from the browser.
// The complete removed record is the payload a later Undo interaction retains.
func TestThoughtfulFavoriteDELETEReturnsRemovedRecord(t *testing.T) {
	state := thoughtfulFavoriteStorageState(false)
	handler := newAreaFavoriteAPIHandler(t, state, fixedThoughtfulFavoriteHTTPTime)
	request := httptest.NewRequest(
		http.MethodDelete,
		"/api/areas/thoughtful/weekly_thoughtful_suggestions/favorites/favorite-walk",
		strings.NewReader(`{"expected_revision":7}`),
	)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", response.Code, http.StatusOK, response.Body.String())
	}
	assertThoughtfulFavoriteResponseHeaders(t, response)
	var mutation ThoughtfulFavoriteMutation
	if err := json.NewDecoder(response.Body).Decode(&mutation); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if mutation.Revision != 8 ||
		mutation.Favorite.ID != "favorite-walk" ||
		mutation.Favorite.SourceSuggestionID != "plan-walk" ||
		mutation.Favorite.Title != "Plan a short walk" {
		t.Fatalf("removed mutation = %#v, want complete favorite at revision 8", mutation)
	}

	stored := state.documents[entryStateStorageKey("thoughtful", weeklyThoughtfulSuggestionsContentType)]
	var document weeklyThoughtfulSuggestionsDocument
	if err := json.Unmarshal([]byte(stored.content), &document); err != nil {
		t.Fatalf("decode stored document: %v", err)
	}
	if len(document.Favorites) != 0 {
		t.Fatalf("stored favorites = %#v, want selected favorite removed", document.Favorites)
	}
}

// TestThoughtfulFavoritePOSTRestoresDeletedSnapshot verifies the complete Undo
// contract. DELETE supplies the exact browser-retained record and revision;
// POST returns the established mutation representation after storage restores
// that durable record without looking up the current weekly suggestions.
func TestThoughtfulFavoritePOSTRestoresDeletedSnapshot(t *testing.T) {
	state := thoughtfulFavoriteStorageState(false)
	handler := newAreaFavoriteAPIHandler(t, state, fixedThoughtfulFavoriteHTTPTime)
	deleteRequest := httptest.NewRequest(
		http.MethodDelete,
		"/api/areas/thoughtful/weekly_thoughtful_suggestions/favorites/favorite-walk",
		strings.NewReader(`{"expected_revision":7}`),
	)
	deleteResponse := httptest.NewRecorder()
	handler.ServeHTTP(deleteResponse, deleteRequest)
	if deleteResponse.Code != http.StatusOK {
		t.Fatalf("delete status = %d, want %d: %s", deleteResponse.Code, http.StatusOK, deleteResponse.Body.String())
	}

	var deleted ThoughtfulFavoriteMutation
	if err := json.NewDecoder(deleteResponse.Body).Decode(&deleted); err != nil {
		t.Fatalf("decode deleted favorite: %v", err)
	}
	restoreBody, err := json.Marshal(thoughtfulFavoriteRestoreRequest{
		Favorite:         deleted.Favorite,
		ExpectedRevision: deleted.Revision,
	})
	if err != nil {
		t.Fatalf("encode restore request: %v", err)
	}
	restoreRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/areas/thoughtful/weekly_thoughtful_suggestions/favorites/restore",
		bytes.NewReader(restoreBody),
	)
	restoreResponse := httptest.NewRecorder()
	handler.ServeHTTP(restoreResponse, restoreRequest)

	if restoreResponse.Code != http.StatusOK {
		t.Fatalf("restore status = %d, want %d: %s", restoreResponse.Code, http.StatusOK, restoreResponse.Body.String())
	}
	assertThoughtfulFavoriteResponseHeaders(t, restoreResponse)
	var restored ThoughtfulFavoriteMutation
	if err := json.NewDecoder(restoreResponse.Body).Decode(&restored); err != nil {
		t.Fatalf("decode restored favorite: %v", err)
	}
	if restored.AreaID != "thoughtful" ||
		restored.ContentType != weeklyThoughtfulSuggestionsContentType ||
		restored.Revision != 9 ||
		restored.Favorite != deleted.Favorite {
		t.Fatalf("restored mutation = %#v, want deleted record at revision 9", restored)
	}

	stored := state.documents[entryStateStorageKey("thoughtful", weeklyThoughtfulSuggestionsContentType)]
	var document weeklyThoughtfulSuggestionsDocument
	if err := json.Unmarshal([]byte(stored.content), &document); err != nil {
		t.Fatalf("decode stored document: %v", err)
	}
	if stored.revision != restored.Revision || len(document.Favorites) != 1 || document.Favorites[0] != deleted.Favorite {
		t.Fatalf("stored restore = revision %d favorites %#v, want restored deleted record", stored.revision, document.Favorites)
	}
}

// TestThoughtfulFavoritePOSTRestoreRejectsChangedOrCollidingDocument verifies
// that Undo never overwrites an intervening area revision and maps a storage
// identity collision to the same public 409 response. Both rejections occur
// before a second conditional UPDATE can change the document.
func TestThoughtfulFavoritePOSTRestoreRejectsChangedOrCollidingDocument(t *testing.T) {
	t.Run("intervening area revision", func(t *testing.T) {
		state := thoughtfulFavoriteStorageState(false)
		handler := newAreaFavoriteAPIHandler(t, state, fixedThoughtfulFavoriteHTTPTime)
		deleteRequest := httptest.NewRequest(
			http.MethodDelete,
			"/api/areas/thoughtful/weekly_thoughtful_suggestions/favorites/favorite-walk",
			strings.NewReader(`{"expected_revision":7}`),
		)
		deleteResponse := httptest.NewRecorder()
		handler.ServeHTTP(deleteResponse, deleteRequest)
		if deleteResponse.Code != http.StatusOK {
			t.Fatalf("delete status = %d, want %d: %s", deleteResponse.Code, http.StatusOK, deleteResponse.Body.String())
		}
		var deleted ThoughtfulFavoriteMutation
		if err := json.NewDecoder(deleteResponse.Body).Decode(&deleted); err != nil {
			t.Fatalf("decode deleted favorite: %v", err)
		}

		// Model a weekly refresh winning after DELETE. Reset updateQuery so this
		// assertion observes only the attempted stale restore, not the delete.
		key := entryStateStorageKey("thoughtful", weeklyThoughtfulSuggestionsContentType)
		stored := state.documents[key]
		stored.revision = deleted.Revision + 1
		state.documents[key] = stored
		state.updateQuery = ""
		restoreBody, err := json.Marshal(thoughtfulFavoriteRestoreRequest{
			Favorite:         deleted.Favorite,
			ExpectedRevision: deleted.Revision,
		})
		if err != nil {
			t.Fatalf("encode restore request: %v", err)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(
			http.MethodPost,
			"/api/areas/thoughtful/weekly_thoughtful_suggestions/favorites/restore",
			bytes.NewReader(restoreBody),
		))

		if response.Code != http.StatusConflict {
			t.Fatalf("status = %d, want %d: %s", response.Code, http.StatusConflict, response.Body.String())
		}
		if state.updateQuery != "" {
			t.Fatalf("stale restore issued an update: %q", state.updateQuery)
		}
		var after weeklyThoughtfulSuggestionsDocument
		if err := json.Unmarshal([]byte(state.documents[key].content), &after); err != nil {
			t.Fatalf("decode unchanged document: %v", err)
		}
		if len(after.Favorites) != 0 {
			t.Fatalf("favorites after stale restore = %#v, want deleted record to remain absent", after.Favorites)
		}
	})

	t.Run("source identity collision", func(t *testing.T) {
		state := thoughtfulFavoriteStorageState(false)
		handler := newAreaFavoriteAPIHandler(t, state, fixedThoughtfulFavoriteHTTPTime)
		body, err := json.Marshal(thoughtfulFavoriteRestoreRequest{
			Favorite: thoughtfulFavorite{
				ID:                 "favorite-restored-walk",
				SourceSuggestionID: "plan-walk",
				Title:              "Plan a short walk",
				Details:            "Choose an easy route after dinner.",
				Category:           "quality time",
				SavedAt:            "2026-08-20",
			},
			ExpectedRevision: 7,
		})
		if err != nil {
			t.Fatalf("encode collision request: %v", err)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(
			http.MethodPost,
			"/api/areas/thoughtful/weekly_thoughtful_suggestions/favorites/restore",
			bytes.NewReader(body),
		))

		if response.Code != http.StatusConflict {
			t.Fatalf("status = %d, want %d: %s", response.Code, http.StatusConflict, response.Body.String())
		}
		if state.updateQuery != "" {
			t.Fatalf("colliding restore issued an update: %q", state.updateQuery)
		}
	})
}

// TestThoughtfulFavoritePOSTRestoreRejectsInvalidBodies verifies that strict
// decoding and snapshot validation reject malformed Undo data before storage
// reads the area document. The stable 400 keeps decoder and validation details
// out of the browser response.
func TestThoughtfulFavoritePOSTRestoreRejectsInvalidBodies(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
	}{
		{name: "unknown favorite field", body: `{"expected_revision":7,"favorite":{"id":"favorite-undo","source_suggestion_id":"expired","title":"Keep planning","details":"A complete durable record.","category":"care","saved_at":"2026-08-20","extra":true}}`},
		{name: "invalid saved date", body: `{"expected_revision":7,"favorite":{"id":"favorite-undo","source_suggestion_id":"expired","title":"Keep planning","details":"A complete durable record.","category":"care","saved_at":"August 20"}}`},
		{name: "trailing JSON", body: `{"expected_revision":7,"favorite":{"id":"favorite-undo","source_suggestion_id":"expired","title":"Keep planning","details":"A complete durable record.","category":"care","saved_at":"2026-08-20"}} {}`},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			state := thoughtfulFavoriteStorageState(false)
			handler := newAreaFavoriteAPIHandler(t, state, fixedThoughtfulFavoriteHTTPTime)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(
				http.MethodPost,
				"/api/areas/thoughtful/weekly_thoughtful_suggestions/favorites/restore",
				strings.NewReader(test.body),
			))

			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
			}
			if !strings.Contains(response.Body.String(), "invalid thoughtful favorite request") {
				t.Fatalf("body = %q, want stable validation message", response.Body.String())
			}
			if state.readQuery != "" || state.updateQuery != "" {
				t.Fatalf("invalid restore reached storage: read=%q update=%q", state.readQuery, state.updateQuery)
			}
		})
	}
}

// TestThoughtfulFavoriteRoutesRejectInvalidBodies verifies the shared decoder
// accepts only one positive integer revision. Every malformed request must stop
// before storage reads the area document.
func TestThoughtfulFavoriteRoutesRejectInvalidBodies(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
	}{
		{name: "unknown field", body: `{"expected_revision":7,"title":"browser text"}`},
		{name: "trailing JSON", body: `{"expected_revision":7} {"expected_revision":8}`},
		{name: "malformed JSON", body: `{"expected_revision":`},
		{name: "not an object", body: `[]`},
		{name: "missing revision", body: `{}`},
		{name: "zero revision", body: `{"expected_revision":0}`},
		{name: "fractional revision", body: `{"expected_revision":7.5}`},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			state := thoughtfulFavoriteStorageState(false)
			handler := newAreaFavoriteAPIHandler(t, state, fixedThoughtfulFavoriteHTTPTime)
			request := httptest.NewRequest(
				http.MethodPut,
				"/api/areas/thoughtful/weekly_thoughtful_suggestions/suggestions/leave-note/favorite",
				strings.NewReader(test.body),
			)
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
			}
			if !strings.Contains(response.Body.String(), "invalid thoughtful favorite request") {
				t.Fatalf("body = %q, want stable validation message", response.Body.String())
			}
			if state.readQuery != "" || state.updateQuery != "" {
				t.Fatalf("invalid request reached storage: read=%q update=%q", state.readQuery, state.updateQuery)
			}
		})
	}
}

// TestThoughtfulFavoriteRoutesRejectInvalidPathIDs confirms blank and padded
// percent-decoded identities receive a safe 400 before reaching storage.
func TestThoughtfulFavoriteRoutesRejectInvalidPathIDs(t *testing.T) {
	for _, test := range []struct {
		name   string
		method string
		path   string
	}{
		{
			name:   "blank source suggestion id",
			method: http.MethodPut,
			path:   "/api/areas/thoughtful/weekly_thoughtful_suggestions/suggestions/%20/favorite",
		},
		{
			name:   "blank durable favorite id",
			method: http.MethodDelete,
			path:   "/api/areas/thoughtful/weekly_thoughtful_suggestions/favorites/%20",
		},
		{
			name:   "padded source suggestion id",
			method: http.MethodPut,
			path:   "/api/areas/thoughtful/weekly_thoughtful_suggestions/suggestions/%20leave-note%20/favorite",
		},
		{
			name:   "padded durable favorite id",
			method: http.MethodDelete,
			path:   "/api/areas/thoughtful/weekly_thoughtful_suggestions/favorites/%20favorite-walk%20",
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			state := thoughtfulFavoriteStorageState(false)
			handler := newAreaFavoriteAPIHandler(t, state, fixedThoughtfulFavoriteHTTPTime)
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(`{"expected_revision":7}`))
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
			}
			if state.readQuery != "" || state.updateQuery != "" {
				t.Fatalf("blank path id reached storage: read=%q update=%q", state.readQuery, state.updateQuery)
			}
		})
	}
}

// TestThoughtfulFavoriteRoutesMapSafeStatuses covers expected missing and stale
// states, private failures, the injected-clock requirement, and method-specific
// fallbacks. No response may expose the database driver's private error text.
func TestThoughtfulFavoriteRoutesMapSafeStatuses(t *testing.T) {
	for _, test := range []struct {
		name       string
		method     string
		path       string
		body       string
		state      *entryStateStorageState
		now        func() time.Time
		wantStatus int
		wantAllow  string
		private    string
	}{
		{
			name:       "unsupported document",
			method:     http.MethodPut,
			path:       "/api/areas/meals/weekly_meal_recommendations/suggestions/dinner/favorite",
			body:       `{"expected_revision":7}`,
			state:      &entryStateStorageState{},
			now:        fixedThoughtfulFavoriteHTTPTime,
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "missing source suggestion",
			method:     http.MethodPut,
			path:       "/api/areas/thoughtful/weekly_thoughtful_suggestions/suggestions/missing/favorite",
			body:       `{"expected_revision":7}`,
			state:      thoughtfulFavoriteStorageState(false),
			now:        fixedThoughtfulFavoriteHTTPTime,
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "duplicate favorite",
			method:     http.MethodPut,
			path:       "/api/areas/thoughtful/weekly_thoughtful_suggestions/suggestions/plan-walk/favorite",
			body:       `{"expected_revision":7}`,
			state:      thoughtfulFavoriteStorageState(false),
			now:        fixedThoughtfulFavoriteHTTPTime,
			wantStatus: http.StatusConflict,
		},
		{
			name:       "stale browser revision",
			method:     http.MethodPut,
			path:       "/api/areas/thoughtful/weekly_thoughtful_suggestions/suggestions/leave-note/favorite",
			body:       `{"expected_revision":6}`,
			state:      thoughtfulFavoriteStorageState(false),
			now:        fixedThoughtfulFavoriteHTTPTime,
			wantStatus: http.StatusConflict,
		},
		{
			name:       "missing durable favorite",
			method:     http.MethodDelete,
			path:       "/api/areas/thoughtful/weekly_thoughtful_suggestions/favorites/favorite-missing",
			body:       `{"expected_revision":7}`,
			state:      thoughtfulFavoriteStorageState(false),
			now:        fixedThoughtfulFavoriteHTTPTime,
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "private storage error",
			method:     http.MethodPut,
			path:       "/api/areas/thoughtful/weekly_thoughtful_suggestions/suggestions/leave-note/favorite",
			body:       `{"expected_revision":7}`,
			state:      &entryStateStorageState{queryError: errors.New("private database hostname")},
			now:        fixedThoughtfulFavoriteHTTPTime,
			wantStatus: http.StatusInternalServerError,
			private:    "private database hostname",
		},
		{
			name:       "missing application clock",
			method:     http.MethodPut,
			path:       "/api/areas/thoughtful/weekly_thoughtful_suggestions/suggestions/leave-note/favorite",
			body:       `{"expected_revision":7}`,
			state:      thoughtfulFavoriteStorageState(false),
			wantStatus: http.StatusInternalServerError,
		},
		{
			name:       "unsupported add method",
			method:     http.MethodPost,
			path:       "/api/areas/thoughtful/weekly_thoughtful_suggestions/suggestions/leave-note/favorite",
			state:      &entryStateStorageState{},
			now:        fixedThoughtfulFavoriteHTTPTime,
			wantStatus: http.StatusMethodNotAllowed,
			wantAllow:  http.MethodPut,
		},
		{
			name:       "unsupported remove method",
			method:     http.MethodPost,
			path:       "/api/areas/thoughtful/weekly_thoughtful_suggestions/favorites/favorite-walk",
			state:      &entryStateStorageState{},
			now:        fixedThoughtfulFavoriteHTTPTime,
			wantStatus: http.StatusMethodNotAllowed,
			wantAllow:  http.MethodDelete,
		},
		{
			name:       "unsupported restore method",
			method:     http.MethodPut,
			path:       "/api/areas/thoughtful/weekly_thoughtful_suggestions/favorites/restore",
			state:      &entryStateStorageState{},
			now:        fixedThoughtfulFavoriteHTTPTime,
			wantStatus: http.StatusMethodNotAllowed,
			wantAllow:  http.MethodPost,
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			handler := newAreaFavoriteAPIHandler(t, test.state, test.now)
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d: %s", response.Code, test.wantStatus, response.Body.String())
			}
			if allow := response.Header().Get("Allow"); allow != test.wantAllow {
				t.Fatalf("Allow = %q, want %q", allow, test.wantAllow)
			}
			if test.private != "" && strings.Contains(response.Body.String(), test.private) {
				t.Fatalf("body exposed internal error: %q", response.Body.String())
			}
		})
	}
}

// newAreaFavoriteAPIHandler connects the focused registrar to the same concrete
// database type used by production. main.go registration is intentionally not
// part of this increment, so these tests use a dedicated mux rather than the
// complete serverApplication handler.
func newAreaFavoriteAPIHandler(
	t *testing.T,
	state *entryStateStorageState,
	now func() time.Time,
) http.Handler {
	t.Helper()

	database := sql.OpenDB(entryStateStorageConnector{state: state})
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("close thoughtful favorite route database: %v", err)
		}
	})
	mux := http.NewServeMux()
	registerAreaFavoriteRoutes(mux, database, now)
	return mux
}

// fixedThoughtfulFavoriteHTTPTime supplies the application-local instant used
// by PUT success tests. Only its local calendar date is stored in saved_at.
func fixedThoughtfulFavoriteHTTPTime() time.Time {
	return time.Date(2026, time.August, 20, 18, 45, 0, 0, time.FixedZone("EDT", -4*60*60))
}

func assertThoughtfulFavoriteResponseHeaders(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()

	if response.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q, want JSON", response.Header().Get("Content-Type"))
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", response.Header().Get("Cache-Control"))
	}
}
