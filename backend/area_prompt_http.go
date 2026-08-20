// area_prompt_http.go owns the HTTP boundary for editing stored area-update
// prompts. serverApplication.newHTTPHandler composes these routes into the
// application's shared ServeMux; this file deliberately does not run updates,
// so saving instructions cannot regenerate the area content a person sees.
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

// areaUpdatePromptRequest is deliberately smaller than AreaUpdatePrompt because
// a browser is allowed to choose only the prompt text. The URL identifies the
// area and content type, and the database determines the enabled state; accepting
// either of those fields from JSON could make the visible editor save a different
// document than the one a person opened.
type areaUpdatePromptRequest struct {
	Prompt string `json:"prompt"`
}

// Called by serverApplication.newHTTPHandler in main.go. It installs the prompt
// routes together. Go's ServeMux chooses the most specific matching pattern, so
// method-specific GET and PUT routes win over the methodless fallback, and the
// prompt fallback wins over the optional frontend catch-all.
func registerAreaUpdatePromptRoutes(mux *http.ServeMux, database *sql.DB) {
	// The prompt editor reads one configuration at a time. This route deliberately
	// accepts any area/content identifiers present in storage rather than only the
	// three weekly-update targets, so every area can use the same persisted prompt API.
	// A missing or disabled row is a normal not-configured state, not a database
	// failure, and writeAreaUpdatePromptResponse translates it into a safe 404.
	mux.HandleFunc("GET /api/areas/{areaID}/{contentType}/prompt", func(w http.ResponseWriter, r *http.Request) {
		updatePrompt, err := loadAreaUpdatePrompt(
			r.Context(),
			database,
			r.PathValue("areaID"),
			r.PathValue("contentType"),
		)
		writeAreaUpdatePromptResponse(w, updatePrompt, err)
	})

	// Saving a prompt changes only the instructions that future refreshes will
	// read. It never calls the updater, so editing wording in the interface does
	// not unexpectedly regenerate the area content currently shown to a person.
	mux.HandleFunc("PUT /api/areas/{areaID}/{contentType}/prompt", func(w http.ResponseWriter, r *http.Request) {
		prompt, err := decodeAreaUpdatePromptRequest(r)
		if err != nil {
			// Parsing errors are client-controlled and therefore return a stable 400
			// instead of exposing decoder details or echoing the submitted payload.
			http.Error(w, "invalid area update prompt", http.StatusBadRequest)
			return
		}

		updatePrompt, err := saveAreaUpdatePrompt(
			r.Context(),
			database,
			r.PathValue("areaID"),
			r.PathValue("contentType"),
			prompt,
		)
		writeAreaUpdatePromptResponse(w, updatePrompt, err)
	})

	// A methodless pattern has lower precedence than the GET and PUT patterns
	// above, but a more specific path than the production frontend catch-all.
	// It therefore gives DELETE, POST, and other unsupported prompt requests the
	// same API-shaped 405 response whether or not this Cloud Run instance also
	// serves the React bundle. The Allow header tells a client exactly which
	// prompt operations are available without disclosing storage details.
	mux.HandleFunc("/api/areas/{areaID}/{contentType}/prompt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", "GET, PUT")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	})
}

// Called by the PUT route registered by registerAreaUpdatePromptRoutes in this
// file. It accepts exactly one browser JSON object with a prompt field.
// DisallowUnknownFields prevents a spelling error such as {"promt":"..."} from
// silently saving an empty prompt, and the second Decode rejects extra payloads.
// Prompt text is returned without trimming because whitespace can be meaningful
// in multi-line instructions; TrimSpace is used only to reject blank input.
func decodeAreaUpdatePromptRequest(r *http.Request) (string, error) {
	if r == nil || r.Body == nil {
		return "", errors.New("prompt request body is required")
	}

	var request areaUpdatePromptRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return "", fmt.Errorf("decode prompt request: %w", err)
	}

	// Decode consumes exactly one JSON value. A second decode must find EOF;
	// otherwise the body contained a second value, malformed trailing bytes, or
	// another payload that is not part of the one-object request contract.
	var trailingValue any
	if err := decoder.Decode(&trailingValue); !errors.Is(err, io.EOF) {
		if err == nil {
			return "", errors.New("prompt request contains trailing JSON")
		}
		return "", fmt.Errorf("decode trailing prompt request data: %w", err)
	}

	if strings.TrimSpace(request.Prompt) == "" {
		return "", errors.New("prompt request must not be blank")
	}
	return request.Prompt, nil
}

// Called by the GET and PUT routes registered by
// registerAreaUpdatePromptRoutes in this file. It converts a completed prompt
// read or save into the browser contract. The sentinel error represents an
// expected absent or disabled configuration and receives a 404. Every other
// error stays in server logs because database driver messages can reveal
// connection or SQL details.
func writeAreaUpdatePromptResponse(w http.ResponseWriter, updatePrompt AreaUpdatePrompt, err error) {
	if errors.Is(err, errAreaUpdatePromptNotConfigured) {
		http.Error(w, "area update prompt is not configured", http.StatusNotFound)
		return
	}
	if err != nil {
		log.Printf("access area update prompt: %v", err)
		http.Error(w, "unable to access area update prompt", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if encodeError := json.NewEncoder(w).Encode(updatePrompt); encodeError != nil {
		// At this point the response may have started, so it is too late to replace
		// it with a clean error response. Logging still makes the failure visible.
		log.Printf("encode area update prompt: %v", encodeError)
	}
}
