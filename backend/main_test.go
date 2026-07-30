package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// TestAreaContentsResponseReturnsStoredContent verifies the HTTP response that
// React consumes. The stored content must remain a nested JSON object rather
// than becoming a quoted JSON string.
func TestAreaContentsResponseReturnsStoredContent(t *testing.T) {
	updatedAt := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	response := httptest.NewRecorder()
	writeAreaContentsResponse(response, []AreaContent{
		{
			AreaID:      "health",
			ContentType: "weekly_workout_routine",
			Content:     json.RawMessage(`{"entries":[{"title":"Monday workout"}]}`),
			Revision:    1,
			UpdatedAt:   updatedAt,
		},
	}, nil)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	contentType := response.Header().Get("Content-Type")
	if contentType != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q, want JSON", contentType)
	}
	cacheControl := response.Header().Get("Cache-Control")
	if cacheControl != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", cacheControl)
	}

	var contents []AreaContent
	decodeError := json.NewDecoder(response.Body).Decode(&contents)
	if decodeError != nil {
		t.Fatalf("decode response: %v", decodeError)
	}
	if len(contents) != 1 {
		t.Fatalf("len(contents) = %d, want 1", len(contents))
	}
	storedContent := string(contents[0].Content)
	if storedContent != `{"entries":[{"title":"Monday workout"}]}` {
		t.Fatalf("content = %s, want nested workout JSON", storedContent)
	}
}

// TestAreaContentsResponseHidesDatabaseErrors guards the separation between
// server diagnostics and the public response. pgx errors can include the host,
// username, table, or query, but the browser receives only stable copy.
func TestAreaContentsResponseHidesDatabaseErrors(t *testing.T) {
	response := httptest.NewRecorder()
	writeAreaContentsResponse(
		response,
		nil,
		errors.New("database detail that should stay on the server"),
	)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusInternalServerError)
	}
	body := response.Body.String()
	if !strings.Contains(body, "unable to load area content") {
		t.Fatalf("body = %q, want safe public error", body)
	}
	if strings.Contains(body, "database detail") {
		t.Fatalf("body exposed internal database error: %q", body)
	}
}

// TestFrontendHandlerServesProductionBundle verifies the visible Cloud Run
// boundary for the built React application. Exact Vite assets receive long-term
// caching, browser routes receive index.html for React Router, and unknown API
// or asset paths remain 404s instead of being disguised as successful HTML.
func TestFrontendHandlerServesProductionBundle(t *testing.T) {
	frontendFiles := fstest.MapFS{
		"index.html": {
			Data: []byte("<!doctype html><title>Personal AI</title>"),
		},
		"assets/application-abc123.js": {
			Data: []byte("console.log('personal-ai');"),
		},
	}
	application := &serverApplication{
		frontendFiles: frontendFiles,
	}
	handler := application.newHTTPHandler()

	tests := []struct {
		name             string
		method           string
		path             string
		accept           string
		wantStatus       int
		wantBody         string
		wantCacheControl string
		wantContentType  string
	}{
		{
			name:             "application root",
			method:           http.MethodGet,
			path:             "/",
			accept:           "text/html",
			wantStatus:       http.StatusOK,
			wantBody:         "<title>Personal AI</title>",
			wantCacheControl: frontendIndexCacheControl,
			wantContentType:  "text/html",
		},
		{
			name:             "hashed Vite asset",
			method:           http.MethodGet,
			path:             "/assets/application-abc123.js",
			accept:           "*/*",
			wantStatus:       http.StatusOK,
			wantBody:         "console.log('personal-ai');",
			wantCacheControl: frontendAssetCacheControl,
			wantContentType:  "javascript",
		},
		{
			name:             "React Router navigation",
			method:           http.MethodGet,
			path:             "/review/health",
			accept:           "text/html,application/xhtml+xml",
			wantStatus:       http.StatusOK,
			wantBody:         "<title>Personal AI</title>",
			wantCacheControl: frontendIndexCacheControl,
			wantContentType:  "text/html",
		},
		{
			name:       "missing asset",
			method:     http.MethodGet,
			path:       "/assets/missing.js",
			accept:     "*/*",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "unknown API path",
			method:     http.MethodGet,
			path:       "/api/not-configured",
			accept:     "text/html",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "unsupported browser route method",
			method:     http.MethodPost,
			path:       "/review/health",
			accept:     "text/html",
			wantStatus: http.StatusNotFound,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, nil)
			request.Header.Set("Accept", test.accept)
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, test.wantStatus)
			}
			body := response.Body.String()
			if test.wantBody != "" && !strings.Contains(body, test.wantBody) {
				t.Fatalf("body = %q, want content %q", body, test.wantBody)
			}
			if test.wantBody == "" && strings.Contains(body, "<title>Personal AI</title>") {
				t.Fatalf("body = %q, did not want the React application shell", body)
			}
			if test.wantCacheControl != "" {
				cacheControl := response.Header().Get("Cache-Control")
				if cacheControl != test.wantCacheControl {
					t.Fatalf(
						"Cache-Control = %q, want %q",
						cacheControl,
						test.wantCacheControl,
					)
				}
			}
			if test.wantContentType != "" {
				contentType := response.Header().Get("Content-Type")
				if !strings.Contains(contentType, test.wantContentType) {
					t.Fatalf(
						"Content-Type = %q, want it to contain %q",
						contentType,
						test.wantContentType,
					)
				}
			}
		})
	}
}

// TestFrontendHandlerSupportsHead verifies that infrastructure can inspect a
// production page without downloading its body. The response still carries the
// content length and cache policy that a GET request would receive.
func TestFrontendHandlerSupportsHead(t *testing.T) {
	indexContent := []byte("<!doctype html><title>Personal AI</title>")
	handler := newFrontendHandler(fstest.MapFS{
		"index.html": {
			Data: indexContent,
		},
	})
	request := httptest.NewRequest(http.MethodHead, "/", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if response.Body.Len() != 0 {
		t.Fatalf("body length = %d, want 0 for HEAD", response.Body.Len())
	}
	if response.Header().Get("Content-Length") != fmt.Sprintf("%d", len(indexContent)) {
		t.Fatalf(
			"Content-Length = %q, want %d",
			response.Header().Get("Content-Length"),
			len(indexContent),
		)
	}
}

// TestAreaUpdateResultsResponseReturnsSafeResults verifies the public JSON shape
// returned after the concrete updater completes both sections.
func TestAreaUpdateResultsResponseReturnsSafeResults(t *testing.T) {
	updatedAt := time.Date(2026, time.July, 24, 12, 0, 0, 0, time.UTC)
	results := []AreaUpdateResult{
		{
			AreaID:      "health",
			ContentType: "weekly_workout_routine",
			Status:      areaUpdateStatusUpdated,
			Revision:    2,
			UpdatedAt:   updatedAt,
		},
		{
			AreaID:      "meals",
			ContentType: "weekly_meal_recommendations",
			Status:      areaUpdateStatusUpdated,
			Revision:    3,
			UpdatedAt:   updatedAt,
		},
	}

	response := httptest.NewRecorder()
	writeAreaUpdateResultsResponse(
		response,
		"manual",
		results,
		manualAreaUpdateFailureHTTPStatus,
		nil,
	)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}

	var decodedResults []AreaUpdateResult
	decodeError := json.NewDecoder(response.Body).Decode(&decodedResults)
	if decodeError != nil {
		t.Fatalf("decode response: %v", decodeError)
	}
	if len(decodedResults) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(decodedResults))
	}
	if decodedResults[0].Status != areaUpdateStatusUpdated || decodedResults[1].Status != areaUpdateStatusUpdated {
		t.Fatalf(
			"statuses = %q/%q, want updated/updated",
			decodedResults[0].Status,
			decodedResults[1].Status,
		)
	}
}

// TestAreaUpdateResultsResponseHidesPerAreaErrors verifies that partial failures
// use safe status fields while the internal error remains server-only.
func TestAreaUpdateResultsResponseHidesPerAreaErrors(t *testing.T) {
	results := []AreaUpdateResult{
		{
			AreaID:      "health",
			ContentType: "weekly_workout_routine",
			Status:      areaUpdateStatusFailed,
			Error:       errors.New("private provider and database details"),
		},
		{
			AreaID:      "meals",
			ContentType: "weekly_meal_recommendations",
			Status:      areaUpdateStatusUpdated,
			Revision:    4,
		},
	}

	response := httptest.NewRecorder()
	writeAreaUpdateResultsResponse(
		response,
		"manual",
		results,
		manualAreaUpdateFailureHTTPStatus,
		nil,
	)

	if response.Code != http.StatusMultiStatus {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusMultiStatus)
	}
	body := response.Body.String()
	if strings.Contains(body, "private provider") || strings.Contains(body, "database details") {
		t.Fatalf("body exposed an internal update error: %q", body)
	}
	if !strings.Contains(body, `"status":"failed"`) {
		t.Fatalf("body = %q, want safe failed status", body)
	}
}

// TestScheduledAreaUpdateFailureRequestsRetry verifies the HTTP contract with
// Google Cloud Scheduler. A failed Health result must return a non-2xx response
// so Scheduler applies its configured retry policy, while the JSON body still
// excludes the private error retained for Cloud Run logs.
func TestScheduledAreaUpdateFailureRequestsRetry(t *testing.T) {
	results := []AreaUpdateResult{
		{
			AreaID:      "health",
			ContentType: "weekly_workout_routine",
			Status:      areaUpdateStatusFailed,
			Error:       errors.New("private OpenRouter failure"),
		},
		{
			AreaID:      "meals",
			ContentType: "weekly_meal_recommendations",
			Status:      areaUpdateStatusUpdated,
			Revision:    4,
		},
	}

	response := httptest.NewRecorder()
	writeAreaUpdateResultsResponse(
		response,
		"scheduled",
		results,
		scheduledAreaUpdateFailureHTTPStatus,
		nil,
	)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusInternalServerError)
	}
	body := response.Body.String()
	if strings.Contains(body, "private OpenRouter failure") {
		t.Fatalf("body exposed an internal update error: %q", body)
	}
	if !strings.Contains(body, `"status":"failed"`) {
		t.Fatalf("body = %q, want safe failed status", body)
	}
}

// TestWeeklyUpdateRoutesAreRegisteredSeparately guards the routing boundary
// between a person clicking an update button and Google Cloud Scheduler making
// its Sunday request. With no updater configured, all known POST routes return
// the application's explicit 503 response; an unknown route remains a 404.
func TestWeeklyUpdateRoutesAreRegisteredSeparately(t *testing.T) {
	application := &serverApplication{}
	handler := application.newHTTPHandler()

	tests := []struct {
		name       string
		path       string
		wantStatus int
	}{
		{
			name:       "manual update route",
			path:       "/api/areas/weekly-update",
			wantStatus: http.StatusServiceUnavailable,
		},
		{
			name:       "scheduled due-only route",
			path:       "/api/areas/scheduled-weekly-update",
			wantStatus: http.StatusServiceUnavailable,
		},
		{
			name:       "individual area update route",
			path:       "/api/areas/health/weekly_workout_routine/update",
			wantStatus: http.StatusServiceUnavailable,
		},
		{
			name:       "unknown update route",
			path:       "/api/areas/not-a-real-update",
			wantStatus: http.StatusNotFound,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, test.path, nil)
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, test.wantStatus)
			}
		})
	}
}

// TestIndividualAreaUpdateRouteAllowsOnlyConfiguredTargets verifies that URL
// values cannot select arbitrary stored content. The configured Health route
// reaches the updater and reports its intentionally missing test database,
// while an unknown area/content pair is rejected earlier with a 404.
func TestIndividualAreaUpdateRouteAllowsOnlyConfiguredTargets(t *testing.T) {
	application := &serverApplication{
		updater: &localizedAreaUpdater{
			runner:   areaUpdateRunner{},
			location: time.UTC,
		},
	}
	handler := application.newHTTPHandler()

	tests := []struct {
		name       string
		path       string
		wantStatus int
	}{
		{
			name:       "configured Health target reaches updater",
			path:       "/api/areas/health/weekly_workout_routine/update",
			wantStatus: http.StatusInternalServerError,
		},
		{
			name:       "unknown target is rejected",
			path:       "/api/areas/mail/inbox_summary/update",
			wantStatus: http.StatusNotFound,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, test.path, nil)
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, test.wantStatus)
			}
		})
	}
}
