// area_favorite_http.go owns the browser boundary for saving, removing, and
// restoring Thoughtful Suggestions Favorites. It validates every JSON request,
// delegates every document mutation to storage.go, and exposes only stable
// public errors. Route registration is composed into the application mux by
// main.go.
package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// thoughtfulFavoriteRevisionRequest identifies the exact document snapshot a
// person acted on. The URL supplies the suggestion or Favorite identity; no
// visible text is accepted from the browser because storage copies it from the
// trusted area document.
type thoughtfulFavoriteRevisionRequest struct {
	ExpectedRevision int `json:"expected_revision"`
}

// thoughtfulFavoriteRestoreRequest carries the complete immutable snapshot
// returned by DELETE plus the revision produced by that deletion. The snapshot
// lets Undo survive a weekly suggestion refresh without trusting a lookup of
// content that may no longer exist.
type thoughtfulFavoriteRestoreRequest struct {
	Favorite         thoughtfulFavorite `json:"favorite"`
	ExpectedRevision int                `json:"expected_revision"`
}

// registerAreaFavoriteRoutes installs the separate add, remove, and restore
// operations.
// now supplies application-local time for saved_at and is injected so focused
// tests can use a stable date. Production will pass a New York-local clock when
// main.go composes this registrar into the shared application mux.
func registerAreaFavoriteRoutes(
	mux *http.ServeMux,
	database *sql.DB,
	now func() time.Time,
) {
	// PUT saves a durable copy of one currently stored weekly suggestion. Storage
	// creates its independent Favorite ID and copies every visible content field.
	mux.HandleFunc("PUT /api/areas/{areaID}/{contentType}/suggestions/{sourceID}/favorite", func(w http.ResponseWriter, r *http.Request) {
		sourceID := r.PathValue("sourceID")
		if sourceID != strings.TrimSpace(sourceID) || strings.TrimSpace(sourceID) == "" {
			http.Error(w, "invalid thoughtful favorite request", http.StatusBadRequest)
			return
		}

		expectedRevision, err := decodeThoughtfulFavoriteRevisionRequest(r)
		if err != nil {
			http.Error(w, "invalid thoughtful favorite request", http.StatusBadRequest)
			return
		}
		if now == nil {
			// A missing clock is server configuration, not malformed browser input.
			// Keep the implementation detail private while returning a stable failure.
			writeThoughtfulFavoriteResponse(
				w,
				ThoughtfulFavoriteMutation{},
				errors.New("thoughtful favorite clock is required"),
			)
			return
		}

		mutation, err := addThoughtfulFavorite(
			r.Context(),
			database,
			r.PathValue("areaID"),
			r.PathValue("contentType"),
			sourceID,
			expectedRevision,
			now(),
		)
		writeThoughtfulFavoriteResponse(w, mutation, err)
	})

	// DELETE addresses a durable Favorite directly. Its original weekly source
	// may already have disappeared, so removal never depends on that source still
	// being present in the current Suggestions array.
	mux.HandleFunc("DELETE /api/areas/{areaID}/{contentType}/favorites/{favoriteID}", func(w http.ResponseWriter, r *http.Request) {
		favoriteID := r.PathValue("favoriteID")
		if favoriteID != strings.TrimSpace(favoriteID) || strings.TrimSpace(favoriteID) == "" {
			http.Error(w, "invalid thoughtful favorite request", http.StatusBadRequest)
			return
		}

		expectedRevision, err := decodeThoughtfulFavoriteRevisionRequest(r)
		if err != nil {
			http.Error(w, "invalid thoughtful favorite request", http.StatusBadRequest)
			return
		}

		mutation, err := removeThoughtfulFavorite(
			r.Context(),
			database,
			r.PathValue("areaID"),
			r.PathValue("contentType"),
			favoriteID,
			expectedRevision,
		)
		writeThoughtfulFavoriteResponse(w, mutation, err)
	})

	// POST restores the exact record DELETE returned. expected_revision must be
	// the post-delete revision: a refresh or another Favorite interaction during
	// the Undo window returns 409 rather than allowing Undo to overwrite it.
	mux.HandleFunc("POST /api/areas/{areaID}/{contentType}/favorites/restore", func(w http.ResponseWriter, r *http.Request) {
		favorite, expectedRevision, err := decodeThoughtfulFavoriteRestoreRequest(r)
		if err != nil {
			// Both the revision and deleted snapshot come from the browser. Reject an
			// incomplete or altered record before storage reads the area document.
			http.Error(w, "invalid thoughtful favorite request", http.StatusBadRequest)
			return
		}

		mutation, err := restoreThoughtfulFavorite(
			r.Context(),
			database,
			r.PathValue("areaID"),
			r.PathValue("contentType"),
			favorite,
			expectedRevision,
		)
		writeThoughtfulFavoriteResponse(w, mutation, err)
	})

	// Methodless fallbacks are more specific than the production React catch-all.
	// They therefore keep unsupported API calls from receiving index.html and
	// advertise the one method accepted by each distinct resource shape.
	mux.HandleFunc("/api/areas/{areaID}/{contentType}/suggestions/{sourceID}/favorite", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", http.MethodPut)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	})
	mux.HandleFunc("/api/areas/{areaID}/{contentType}/favorites/{favoriteID}", func(w http.ResponseWriter, r *http.Request) {
		// Go rejects a methodless literal /restore fallback because it overlaps
		// DELETE /favorites/{favoriteID}. This shared fallback handles every
		// non-DELETE method for that literal while retaining DELETE support for a
		// genuine Favorite whose durable ID happens to be "restore".
		if r.PathValue("favoriteID") == "restore" {
			w.Header().Set("Allow", http.MethodPost)
		} else {
			w.Header().Set("Allow", http.MethodDelete)
		}
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	})
}

// decodeThoughtfulFavoriteRevisionRequest accepts exactly one object containing
// a positive integer expected_revision. Strict decoding catches misspellings,
// additional browser-controlled fields, fractional numbers, and trailing JSON
// before any database read occurs.
func decodeThoughtfulFavoriteRevisionRequest(r *http.Request) (int, error) {
	if r == nil || r.Body == nil {
		return 0, errors.New("thoughtful favorite request body is required")
	}

	var request thoughtfulFavoriteRevisionRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return 0, fmt.Errorf("decode thoughtful favorite request: %w", err)
	}

	var trailingValue any
	if err := decoder.Decode(&trailingValue); !errors.Is(err, io.EOF) {
		if err == nil {
			return 0, errors.New("thoughtful favorite request contains trailing JSON")
		}
		return 0, fmt.Errorf("decode trailing thoughtful favorite request data: %w", err)
	}
	if request.ExpectedRevision < 1 {
		return 0, errors.New("thoughtful favorite expected revision must be positive")
	}
	return request.ExpectedRevision, nil
}

// decodeThoughtfulFavoriteRestoreRequest accepts one complete deleted record
// and a positive post-delete revision. It reuses storage's record validator so
// HTTP cannot accept a snapshot that storage would later reject, while leaving
// collision and conditional-write rules solely to restoreThoughtfulFavorite.
func decodeThoughtfulFavoriteRestoreRequest(r *http.Request) (thoughtfulFavorite, int, error) {
	if r == nil || r.Body == nil {
		return thoughtfulFavorite{}, 0, errors.New("thoughtful favorite request body is required")
	}

	var request thoughtfulFavoriteRestoreRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return thoughtfulFavorite{}, 0, fmt.Errorf("decode thoughtful favorite restore request: %w", err)
	}

	var trailingValue any
	if err := decoder.Decode(&trailingValue); !errors.Is(err, io.EOF) {
		if err == nil {
			return thoughtfulFavorite{}, 0, errors.New("thoughtful favorite restore request contains trailing JSON")
		}
		return thoughtfulFavorite{}, 0, fmt.Errorf("decode trailing thoughtful favorite restore request data: %w", err)
	}
	if request.ExpectedRevision < 1 {
		return thoughtfulFavorite{}, 0, errors.New("thoughtful favorite expected revision must be positive")
	}
	if err := validateThoughtfulFavoriteRestore(request.Favorite); err != nil {
		return thoughtfulFavorite{}, 0, fmt.Errorf("validate thoughtful favorite restore: %w", err)
	}
	return request.Favorite, request.ExpectedRevision, nil
}

// writeThoughtfulFavoriteResponse translates storage sentinels into the public
// contract. Missing configuration or identity receives 404; duplicate saves and
// stale document revisions receive 409 so React can reload the latest snapshot.
// Unexpected SQL, clock, and encoding details remain only in server logs.
func writeThoughtfulFavoriteResponse(
	w http.ResponseWriter,
	mutation ThoughtfulFavoriteMutation,
	err error,
) {
	if errors.Is(err, errThoughtfulFavoriteNotConfigured) ||
		errors.Is(err, errThoughtfulSuggestionNotFound) ||
		errors.Is(err, errThoughtfulFavoriteNotFound) {
		http.Error(w, "thoughtful favorite was not found", http.StatusNotFound)
		return
	}
	if errors.Is(err, errThoughtfulFavoriteAlreadyExists) ||
		errors.Is(err, errThoughtfulFavoriteRevisionConflict) {
		http.Error(w, "thoughtful favorites changed before they could be saved", http.StatusConflict)
		return
	}
	if err != nil {
		log.Printf("save thoughtful favorite: %v", err)
		http.Error(w, "unable to save thoughtful favorite", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if encodeError := json.NewEncoder(w).Encode(mutation); encodeError != nil {
		// The response may have started before a disconnect or encoding failure, so
		// logging is safer than attempting to replace it with another HTTP body.
		log.Printf("encode thoughtful favorite: %v", encodeError)
	}
}
