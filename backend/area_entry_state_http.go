// area_entry_state_http.go owns the narrow HTTP boundary for marking one Home
// maintenance task or Health workout entry open or done. The route delegates all
// document mutation and revision handling to storage.go, keeping browser input,
// safe status codes, and persisted JSON responsibilities separate.
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
)

// areaEntryStateRequest is intentionally limited to state. The URL identifies
// the area document and entry, while storage loads the current revision itself;
// accepting either identifier or a whole content object would let a client
// overwrite fields that are outside this small completion-control contract.
type areaEntryStateRequest struct {
	State string `json:"state"`
}

// registerAreaEntryStateRoutes connects the completion-control endpoint to the
// shared application mux. Its methodless fallback remains more specific than
// the production frontend handler, so unsupported methods still receive an API
// 405 instead of HTML when Cloud Run also serves the React bundle.
func registerAreaEntryStateRoutes(mux *http.ServeMux, database *sql.DB) {
	mux.HandleFunc("PUT /api/areas/{areaID}/{contentType}/entries/{entryID}/state", func(w http.ResponseWriter, r *http.Request) {
		state, err := decodeAreaEntryStateRequest(r)
		if err != nil {
			// JSON shape and state values are entirely client-controlled. Return one
			// stable message rather than decoder details or a copy of the request body.
			http.Error(w, "invalid area entry state", http.StatusBadRequest)
			return
		}
		entryID := r.PathValue("entryID")
		if strings.TrimSpace(entryID) == "" {
			// A URL segment containing only encoded spaces identifies no real entry.
			// Treat it like malformed browser input instead of sending it to storage
			// and incorrectly presenting a validation issue as a server failure.
			http.Error(w, "invalid area entry state", http.StatusBadRequest)
			return
		}

		entryState, err := saveAreaEntryState(
			r.Context(),
			database,
			r.PathValue("areaID"),
			r.PathValue("contentType"),
			entryID,
			state,
		)
		writeAreaEntryStateResponse(w, entryState, err)
	})

	mux.HandleFunc("/api/areas/{areaID}/{contentType}/entries/{entryID}/state", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", http.MethodPut)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	})
}

// decodeAreaEntryStateRequest accepts exactly one JSON object with the strict
// {"state":"open"} or {"state":"done"} shape. A second Decode catches
// trailing values, while DisallowUnknownFields catches misspellings before they
// can be mistaken for a request to set an empty state.
func decodeAreaEntryStateRequest(r *http.Request) (string, error) {
	if r == nil || r.Body == nil {
		return "", errors.New("area entry state request body is required")
	}

	var request areaEntryStateRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return "", fmt.Errorf("decode area entry state request: %w", err)
	}

	var trailingValue any
	if err := decoder.Decode(&trailingValue); !errors.Is(err, io.EOF) {
		if err == nil {
			return "", errors.New("area entry state request contains trailing JSON")
		}
		return "", fmt.Errorf("decode trailing area entry state request data: %w", err)
	}
	if !isAreaEntryState(request.State) {
		return "", errors.New("area entry state must be open or done")
	}
	return request.State, nil
}

// writeAreaEntryStateResponse maps the narrow storage error vocabulary to the
// browser contract. Expected missing configuration, missing entries, and a
// concurrent revision change receive safe 404 or 409 responses; all unexpected
// storage details stay in logs and become one stable 500 message.
func writeAreaEntryStateResponse(w http.ResponseWriter, entryState AreaEntryState, err error) {
	if errors.Is(err, errAreaEntryStateNotConfigured) || errors.Is(err, errAreaEntryStateNotFound) {
		http.Error(w, "area entry state is not configured", http.StatusNotFound)
		return
	}
	if errors.Is(err, errAreaEntryStateRevisionConflict) {
		http.Error(w, "area entry state changed before it could be saved", http.StatusConflict)
		return
	}
	if err != nil {
		log.Printf("save area entry state: %v", err)
		http.Error(w, "unable to save area entry state", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if encodeError := json.NewEncoder(w).Encode(entryState); encodeError != nil {
		// The response may already have started, so logging is the only safe action
		// left if the client disconnects while the small success body is written.
		log.Printf("encode area entry state: %v", encodeError)
	}
}
