package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// storage.go owns the boundary between the Go backend and persisted application
// data. Area documents live in personal_ai.area_content, while the model
// instructions used to regenerate those documents live in
// personal_ai.area_update_prompt. This file opens the shared connection pool and
// translates both tables into values that the HTTP and updater layers can use.

// AreaContent is one independently updated piece of a visible life area. For
// example, the Health area's weekly workout routine is stored with area_id
// "health" and content_type "weekly_workout_routine". Content stays as raw JSON
// because updater.go sends that complete JSON document to the language model,
// while the API can pass the same document to the frontend without losing its
// area-specific shape.
//
// The text in backticks after each field is a Go struct tag used by the
// encoding/json package. For example, `json:"area_id"` means the exported Go
// field AreaID is written as the JSON key "area_id" in an API response, and the
// same key is recognized when JSON is decoded. These tags do not rename the Go
// fields or affect how SQL scans database columns into them.
type AreaContent struct {
	AreaID      string          `json:"area_id"`
	ContentType string          `json:"content_type"`
	Content     json.RawMessage `json:"content"`
	Revision    int             `json:"revision"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

// AreaUpdatePrompt contains the model instructions used to refresh one stored
// JSON document. It lives separately from AreaContent because ordinary Review
// screen reads do not need prompt text; the scheduler and manual update action
// load this row only when they are about to regenerate the matching document.
// The JSON names form the browser-facing contract used by the prompt editor: Go
// keeps idiomatic field names internally while the API returns snake_case keys
// alongside the existing area-content responses.
type AreaUpdatePrompt struct {
	AreaID      string `json:"area_id"`
	ContentType string `json:"content_type"`
	Prompt      string `json:"prompt"`
}

// errAreaUpdatePromptNotConfigured distinguishes a missing or disabled prompt
// from an unexpected database failure. The HTTP layer can turn this known state
// into a safe 404 while still keeping connection and SQL details in server logs.
var errAreaUpdatePromptNotConfigured = errors.New("area update prompt is not configured")

// errAreaEntryStateNotConfigured covers the documents this small mutation API
// is intentionally not allowed to edit. It includes an unsupported URL pair and
// a configured pair whose database row no longer exists, so the HTTP layer can
// give both cases the same safe 404 without exposing table details.
var errAreaEntryStateNotConfigured = errors.New("area entry state is not configured")

// errAreaEntryStateNotFound identifies a supported document whose entries array
// does not contain the requested id. This remains separate from an invalid
// stored document, which is an internal data problem rather than a normal 404.
var errAreaEntryStateNotFound = errors.New("area entry was not found")

// errAreaEntryStateRevisionConflict means another writer changed the whole
// document after this operation read it. The caller must reload before retrying
// so an entry-state click can never overwrite a newly generated weekly plan.
var errAreaEntryStateRevisionConflict = errors.New("area entry state revision conflict")

// openDatabase creates a small database/sql connection pool for Supabase. The
// supplied URL should be the Session Pooler connection string copied from the
// Supabase Connect panel and stored in the backend's DATABASE_URL environment
// variable. sql.Open configures a pool lazily, so PingContext verifies the URL
// and network connection before the server begins accepting requests.
func openDatabase(ctx context.Context, databaseURL string) (*sql.DB, error) {
	databaseURL = strings.TrimSpace(databaseURL)
	if databaseURL == "" {
		return nil, errors.New("database url is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	// sql.Open registers pgx as the PostgreSQL driver and returns *sql.DB, which
	// manages a reusable pool of connections rather than representing one live
	// connection. The pointer lets callers configure and use that same pool. The
	// first real network connection is attempted by PingContext below.
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// This personal application needs only a few concurrent database operations.
	// Keeping the pool deliberately small avoids consuming unnecessary Supabase
	// pooler connections if Cloud Run starts more than one server instance.
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	db.SetConnMaxIdleTime(5 * time.Minute)
	db.SetConnMaxLifetime(30 * time.Minute)

	pingError := db.PingContext(ctx)
	if pingError != nil {
		// Close releases any connection that PingContext may have opened before it
		// discovered invalid credentials or an unavailable database.
		_ = db.Close()
		return nil, fmt.Errorf("connect to database: %w", pingError)
	}

	return db, nil
}

// loadAreaContents returns every content document currently available to the
// Review screen. The database owns the mutable content and timestamps; the
// frontend continues to own display-only choices such as area names, ordering,
// descriptions, and accent colors.
func loadAreaContents(ctx context.Context, db *sql.DB) ([]AreaContent, error) {
	if db == nil {
		return nil, errors.New("database is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	rows, err := db.QueryContext(ctx, `
		SELECT area_id, content_type, content::text, revision, updated_at
		FROM personal_ai.area_content
		ORDER BY area_id, content_type
	`)
	if err != nil {
		return nil, fmt.Errorf("query area content: %w", err)
	}

	// Rows holds query resources and temporarily checks a connection out of the
	// pool. Defer schedules Close for every return path from this function, which
	// releases that connection even if scanning one row fails partway through.
	defer rows.Close()

	// A non-nil empty slice encodes as [] instead of null. That gives the React
	// client one predictable collection shape even before any rows are seeded.
	contents := make([]AreaContent, 0)
	for rows.Next() {
		content, err := scanAreaContentRows(rows)
		if err != nil {
			return nil, err
		}
		contents = append(contents, content)
	}
	rowsError := rows.Err()
	if rowsError != nil {
		return nil, fmt.Errorf("iterate area content: %w", rowsError)
	}

	return contents, nil
}

// loadAreaContent retrieves one specific JSON document for workflows such as
// updating the Health workout without also loading Home, Reading, and Meals.
func loadAreaContent(ctx context.Context, db *sql.DB, areaID string, contentType string) (AreaContent, error) {
	if db == nil {
		return AreaContent{}, errors.New("database is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	areaID = strings.TrimSpace(areaID)
	if areaID == "" {
		return AreaContent{}, errors.New("area id is required")
	}
	contentType = strings.TrimSpace(contentType)
	if contentType == "" {
		return AreaContent{}, errors.New("content type is required")
	}

	row := db.QueryRowContext(ctx, `
		SELECT area_id, content_type, content::text, revision, updated_at
		FROM personal_ai.area_content
		WHERE area_id = $1 AND content_type = $2
	`, areaID, contentType)

	content, err := scanAreaContentRow(row)
	if err != nil {
		return AreaContent{}, err
	}
	return content, nil
}

// loadAreaUpdatePrompt retrieves the active instructions for one area document.
// Both the weekly scheduler and the manual HTTP action call this function, which
// ensures those entry points use the same prompt without adding prompt text to
// every ordinary area-content query.
func loadAreaUpdatePrompt(ctx context.Context, db *sql.DB, areaID string, contentType string) (AreaUpdatePrompt, error) {
	if db == nil {
		return AreaUpdatePrompt{}, errors.New("database is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	areaID = strings.TrimSpace(areaID)
	if areaID == "" {
		return AreaUpdatePrompt{}, errors.New("area id is required")
	}
	contentType = strings.TrimSpace(contentType)
	if contentType == "" {
		return AreaUpdatePrompt{}, errors.New("content type is required")
	}

	var updatePrompt AreaUpdatePrompt
	row := db.QueryRowContext(ctx, `
		SELECT area_id, content_type, prompt
		FROM personal_ai.area_update_prompt
		WHERE area_id = $1
			AND content_type = $2
			AND enabled = TRUE
		`, areaID, contentType)

	// Scan must write each selected column into caller-owned storage, so it
	// accepts pointers rather than copies of these fields. Each & takes the
	// address of the matching field; Scan follows that address and assigns the
	// database value in SELECT order.
	err := row.Scan(
		&updatePrompt.AreaID,
		&updatePrompt.ContentType,
		&updatePrompt.Prompt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return AreaUpdatePrompt{}, fmt.Errorf(
			"%w for %s/%s",
			errAreaUpdatePromptNotConfigured,
			areaID,
			contentType,
		)
	}
	if err != nil {
		return AreaUpdatePrompt{}, fmt.Errorf("scan area update prompt: %w", err)
	}

	validationError := validateAreaUpdatePrompt(updatePrompt)
	if validationError != nil {
		return AreaUpdatePrompt{}, validationError
	}
	return updatePrompt, nil
}

// saveAreaUpdatePrompt creates or replaces the model instructions for one area
// document. The prompt editor supplies the same area/content pair that identifies
// the row, so the conflict target cannot accidentally change another area's
// instructions. The upsert also re-enables a previously disabled row, making a
// saved prompt immediately available to the manual and scheduled refresh flows.
// RETURNING reads the database result back immediately; the API can therefore
// respond with the exact text that future updates will load.
//
// Prompt text is not trimmed before saving because line breaks and intentional
// indentation can make longer model instructions easier to maintain. Validation
// still rejects a string containing only spaces or line breaks.
func saveAreaUpdatePrompt(
	ctx context.Context,
	db *sql.DB,
	areaID string,
	contentType string,
	prompt string,
) (AreaUpdatePrompt, error) {
	if db == nil {
		return AreaUpdatePrompt{}, errors.New("database is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	updatePrompt := AreaUpdatePrompt{
		AreaID:      strings.TrimSpace(areaID),
		ContentType: strings.TrimSpace(contentType),
		Prompt:      prompt,
	}
	validationError := validateAreaUpdatePrompt(updatePrompt)
	if validationError != nil {
		return AreaUpdatePrompt{}, validationError
	}

	// PostgreSQL performs the create-or-update decision atomically. A first save
	// inserts an enabled row; a later save for the same identifiers changes its
	// prompt and sets enabled back to true. This avoids a read-before-write race
	// and lets a person restore a prompt that was temporarily disabled.
	row := db.QueryRowContext(ctx, `
		INSERT INTO personal_ai.area_update_prompt (
			area_id,
			content_type,
			prompt,
			enabled
		)
		VALUES ($1, $2, $3, TRUE)
		ON CONFLICT (area_id, content_type)
		DO UPDATE SET
			prompt = EXCLUDED.prompt,
			enabled = TRUE
		RETURNING area_id, content_type, prompt
	`, updatePrompt.AreaID, updatePrompt.ContentType, updatePrompt.Prompt)

	var savedPrompt AreaUpdatePrompt
	err := row.Scan(
		&savedPrompt.AreaID,
		&savedPrompt.ContentType,
		&savedPrompt.Prompt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		// A successful INSERT ... ON CONFLICT always returns a row. Treat an empty
		// result as an unexpected storage failure instead of pretending a valid
		// create request is missing configuration.
		return AreaUpdatePrompt{}, fmt.Errorf("save area update prompt returned no row")
	}
	if err != nil {
		return AreaUpdatePrompt{}, fmt.Errorf("save area update prompt: %w", err)
	}

	validationError = validateAreaUpdatePrompt(savedPrompt)
	if validationError != nil {
		return AreaUpdatePrompt{}, validationError
	}
	return savedPrompt, nil
}

// saveAreaContent replaces one JSON document only if its revision still matches
// the version that was sent to the language model. A scheduler and a button
// click can overlap; the revision condition lets only the first completed update
// win instead of allowing the slower response to overwrite newer content.
func saveAreaContent(ctx context.Context, db *sql.DB, current AreaContent, updatedJSON string) (AreaContent, error) {
	if db == nil {
		return AreaContent{}, errors.New("database is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	validationError := validateAreaContent(current)
	if validationError != nil {
		return AreaContent{}, validationError
	}

	updatedJSON = strings.TrimSpace(updatedJSON)
	if updatedJSON == "" {
		return AreaContent{}, errors.New("updated json is required")
	}
	if !json.Valid([]byte(updatedJSON)) {
		return AreaContent{}, errors.New("updated json is not valid")
	}

	// PostgreSQL performs the revision comparison and replacement as one atomic
	// statement. RETURNING provides the exact revision and timestamp chosen by
	// the database for the API response and the scheduler's next due-time check.
	row := db.QueryRowContext(ctx, `
		UPDATE personal_ai.area_content
		SET content = $3::jsonb,
			revision = revision + 1,
			updated_at = NOW()
		WHERE area_id = $1
			AND content_type = $2
			AND revision = $4
		RETURNING area_id, content_type, content::text, revision, updated_at
	`, current.AreaID, current.ContentType, updatedJSON, current.Revision)

	savedContent, err := scanAreaContentRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return AreaContent{}, fmt.Errorf(
			"area content %s/%s changed before the update could be saved",
			current.AreaID,
			current.ContentType,
		)
	}
	if err != nil {
		return AreaContent{}, fmt.Errorf("save area content: %w", err)
	}
	return savedContent, nil
}

// AreaEntryState is the compact response returned after a person marks one
// Review-screen entry open or done. The identifiers come from the stored row
// rather than the URL, and Revision lets a client reconcile a save with the
// next full area-content refresh.
type AreaEntryState struct {
	AreaID      string `json:"area_id"`
	ContentType string `json:"content_type"`
	EntryID     string `json:"entry_id"`
	State       string `json:"state"`
	Revision    int    `json:"revision"`
}

// saveAreaEntryState performs the revision-safe read-modify-write operation for
// the two documents whose visible entries can be completed. It deliberately
// reads the current JSON before changing it: the returned revision is then used
// in the UPDATE predicate, so a concurrent weekly regeneration wins cleanly
// instead of being overwritten by this smaller state change.
//
// Unlike saveAreaContent, this operation does not update updated_at. That column
// records when the weekly document was generated, not when a person checked off
// one of its entries.
func saveAreaEntryState(
	ctx context.Context,
	db *sql.DB,
	areaID string,
	contentType string,
	entryID string,
	state string,
) (AreaEntryState, error) {
	if db == nil {
		return AreaEntryState{}, errors.New("database is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	areaID = strings.TrimSpace(areaID)
	contentType = strings.TrimSpace(contentType)
	if !supportsAreaEntryStateDocument(areaID, contentType) {
		return AreaEntryState{}, fmt.Errorf(
			"%w for %s/%s",
			errAreaEntryStateNotConfigured,
			areaID,
			contentType,
		)
	}
	if strings.TrimSpace(entryID) == "" {
		return AreaEntryState{}, errors.New("entry id is required")
	}
	if !isAreaEntryState(state) {
		return AreaEntryState{}, errors.New("entry state must be open or done")
	}

	// This is intentionally a normal read, not a row lock. The conditional save
	// below is the concurrency boundary and avoids holding a database transaction
	// open while JSON is decoded and reconstructed in Go.
	current, err := loadAreaContent(ctx, db, areaID, contentType)
	if errors.Is(err, sql.ErrNoRows) {
		return AreaEntryState{}, fmt.Errorf(
			"%w for %s/%s",
			errAreaEntryStateNotConfigured,
			areaID,
			contentType,
		)
	}
	if err != nil {
		return AreaEntryState{}, fmt.Errorf("load area entry state content: %w", err)
	}

	updatedJSON, err := replaceAreaEntryState(current, entryID, state)
	if err != nil {
		return AreaEntryState{}, err
	}

	// PostgreSQL compares the revision and increments it in one statement. There
	// is intentionally no updated_at assignment here: a completion click must not
	// make the weekly scheduler believe the document was regenerated today.
	row := db.QueryRowContext(ctx, `
		UPDATE personal_ai.area_content
		SET content = $3::jsonb,
			revision = revision + 1
		WHERE area_id = $1
			AND content_type = $2
			AND revision = $4
		RETURNING area_id, content_type, content::text, revision, updated_at
	`, current.AreaID, current.ContentType, string(updatedJSON), current.Revision)

	savedContent, err := scanAreaContentRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return AreaEntryState{}, fmt.Errorf(
			"%w for %s/%s",
			errAreaEntryStateRevisionConflict,
			current.AreaID,
			current.ContentType,
		)
	}
	if err != nil {
		return AreaEntryState{}, fmt.Errorf("save area entry state: %w", err)
	}

	return AreaEntryState{
		AreaID:      savedContent.AreaID,
		ContentType: savedContent.ContentType,
		EntryID:     entryID,
		State:       state,
		Revision:    savedContent.Revision,
	}, nil
}

// supportsAreaEntryStateDocument is intentionally an allowlist rather than a
// generic entries-array check. The frontend contract currently exposes state
// changes only for Home maintenance tasks and Health workout entries; new area
// types need an explicit backend decision before this endpoint can mutate them.
func supportsAreaEntryStateDocument(areaID string, contentType string) bool {
	return (areaID == "home" && contentType == "maintenance_tasks") ||
		(areaID == "health" && contentType == "weekly_workout_routine")
}

// isAreaEntryState keeps the storage boundary aligned with the HTTP request
// decoder. Rechecking here protects future non-HTTP callers from persisting a
// value that the Review interface does not know how to render.
func isAreaEntryState(state string) bool {
	return state == "open" || state == "done"
}

// replaceAreaEntryState changes only the state property of exactly one entry in
// an area document. RawMessage retains every untouched JSON value as raw JSON
// instead of translating entries into a narrow Go struct that could drop future
// fields such as metadata, links, or top-level generation details.
func replaceAreaEntryState(current AreaContent, entryID string, state string) (json.RawMessage, error) {
	var document map[string]json.RawMessage
	if err := json.Unmarshal(current.Content, &document); err != nil {
		return nil, fmt.Errorf("decode stored area entry document: %w", err)
	}

	rawEntries, found := document["entries"]
	if !found {
		return nil, fmt.Errorf("stored area content %s/%s has no entries array", current.AreaID, current.ContentType)
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(rawEntries, &entries); err != nil {
		return nil, fmt.Errorf("decode stored area entries: %w", err)
	}

	matchedEntries := 0
	for index, rawEntry := range entries {
		var entry map[string]json.RawMessage
		if err := json.Unmarshal(rawEntry, &entry); err != nil {
			return nil, fmt.Errorf("decode stored area entry %d: %w", index, err)
		}

		rawID, found := entry["id"]
		if !found {
			return nil, fmt.Errorf("stored area entry %d has no id", index)
		}
		var storedID string
		if err := json.Unmarshal(rawID, &storedID); err != nil {
			return nil, fmt.Errorf("decode stored area entry %d id: %w", index, err)
		}
		if storedID != entryID {
			continue
		}

		matchedEntries++
		if matchedEntries > 1 {
			return nil, fmt.Errorf("stored area content %s/%s has duplicate entry id %q", current.AreaID, current.ContentType, entryID)
		}

		// json.Marshal writes only this one newly selected state value. All other
		// entry fields remain their original RawMessage values when entry is later
		// marshalled back into the document.
		rawState, err := json.Marshal(state)
		if err != nil {
			return nil, fmt.Errorf("encode replacement entry state: %w", err)
		}
		entry["state"] = rawState
		updatedEntry, err := json.Marshal(entry)
		if err != nil {
			return nil, fmt.Errorf("encode updated area entry %d: %w", index, err)
		}
		entries[index] = updatedEntry
	}
	if matchedEntries == 0 {
		return nil, fmt.Errorf("%w for %s/%s entry %q", errAreaEntryStateNotFound, current.AreaID, current.ContentType, entryID)
	}

	updatedEntries, err := json.Marshal(entries)
	if err != nil {
		return nil, fmt.Errorf("encode updated area entries: %w", err)
	}
	document["entries"] = updatedEntries
	updatedDocument, err := json.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("encode updated area content: %w", err)
	}
	return updatedDocument, nil
}

// scanAreaContentRow scans one result returned by QueryRowContext. Its concrete
// *sql.Row parameter makes single-record reads explicit at each call site.
func scanAreaContentRow(row *sql.Row) (AreaContent, error) {
	var content AreaContent
	var contentJSON string

	err := row.Scan(
		&content.AreaID,
		&content.ContentType,
		&contentJSON,
		&content.Revision,
		&content.UpdatedAt,
	)
	return finishAreaContentScan(content, contentJSON, err)
}

// scanAreaContentRows scans the current result in a list returned by
// QueryContext. It is separate from scanAreaContentRow so no interface hides
// whether the caller is processing one row or iterating through many rows.
func scanAreaContentRows(rows *sql.Rows) (AreaContent, error) {
	var content AreaContent
	var contentJSON string

	err := rows.Scan(
		&content.AreaID,
		&content.ContentType,
		&contentJSON,
		&content.Revision,
		&content.UpdatedAt,
	)
	return finishAreaContentScan(content, contentJSON, err)
}

// finishAreaContentScan applies the shared post-scan JSON conversion and
// validation after either concrete scanner above has read the SQL columns.
func finishAreaContentScan(content AreaContent, contentJSON string, err error) (AreaContent, error) {
	if err != nil {
		return AreaContent{}, fmt.Errorf("scan area content: %w", err)
	}

	content.Content = json.RawMessage(contentJSON)
	validationError := validateAreaContent(content)
	if validationError != nil {
		return AreaContent{}, validationError
	}

	return content, nil
}

// validateAreaContent checks assumptions shared by database reads, the future
// HTTP response, and the JSON updater. PostgreSQL's jsonb type already guarantees
// valid JSON in normal operation; this extra boundary check makes corruption or
// an unexpected test double fail with a useful application-level message.
func validateAreaContent(content AreaContent) error {
	if strings.TrimSpace(content.AreaID) == "" {
		return errors.New("stored area id is required")
	}
	if strings.TrimSpace(content.ContentType) == "" {
		return errors.New("stored content type is required")
	}
	if !json.Valid(content.Content) {
		return fmt.Errorf("stored content for %s/%s is not valid JSON", content.AreaID, content.ContentType)
	}
	if content.Revision < 1 {
		return fmt.Errorf("stored revision for %s/%s must be positive", content.AreaID, content.ContentType)
	}

	return nil
}

// validateAreaUpdatePrompt rejects incomplete database rows before their text is
// combined with personal JSON and sent to OpenRouter.
func validateAreaUpdatePrompt(updatePrompt AreaUpdatePrompt) error {
	if strings.TrimSpace(updatePrompt.AreaID) == "" {
		return errors.New("stored update prompt area id is required")
	}
	if strings.TrimSpace(updatePrompt.ContentType) == "" {
		return errors.New("stored update prompt content type is required")
	}
	if strings.TrimSpace(updatePrompt.Prompt) == "" {
		return fmt.Errorf(
			"stored update prompt for %s/%s is required",
			updatePrompt.AreaID,
			updatePrompt.ContentType,
		)
	}

	return nil
}
