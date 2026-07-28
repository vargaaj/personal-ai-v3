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
// data. New application state lives in Supabase Postgres in the
// personal_ai.area_content table. It opens the shared connection pool and turns
// query rows into the AreaContent values returned by the HTTP server.

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
type AreaUpdatePrompt struct {
	AreaID      string
	ContentType string
	Prompt      string
}

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
			"no active update prompt is configured for %s/%s",
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
