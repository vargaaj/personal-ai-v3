package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

// storage_test.go exercises validation and prompt persistence at the Supabase
// storage boundary without requiring a live database. The small database/sql
// driver at the bottom models active and disabled prompt rows, so tests can
// verify the SQL-facing contract without reading local credentials.

func TestValidateAreaContentAcceptsSeededDocument(t *testing.T) {
	t.Parallel()

	content := AreaContent{
		AreaID:      "health",
		ContentType: "weekly_workout_routine",
		Content:     json.RawMessage(`{"last_updated":"2026-07-22","entries":[]}`),
		Revision:    1,
	}

	validationError := validateAreaContent(content)
	if validationError != nil {
		t.Fatalf("validateAreaContent() returned an unexpected error: %v", validationError)
	}
}

func TestValidateAreaContentRejectsInvalidStoredValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		content     AreaContent
		wantMessage string
	}{
		{
			name: "blank area id",
			content: AreaContent{
				AreaID:      "  ",
				ContentType: "maintenance_tasks",
				Content:     json.RawMessage(`{"entries":[]}`),
				Revision:    1,
			},
			wantMessage: "stored area id is required",
		},
		{
			name: "blank content type",
			content: AreaContent{
				AreaID:      "home",
				ContentType: "",
				Content:     json.RawMessage(`{"entries":[]}`),
				Revision:    1,
			},
			wantMessage: "stored content type is required",
		},
		{
			name: "invalid json",
			content: AreaContent{
				AreaID:      "reading",
				ContentType: "reading_queue",
				Content:     json.RawMessage(`{"entries":`),
				Revision:    1,
			},
			wantMessage: "is not valid JSON",
		},
		{
			name: "non-positive revision",
			content: AreaContent{
				AreaID:      "meals",
				ContentType: "weekly_meal_recommendations",
				Content:     json.RawMessage(`{"recommendations":[]}`),
				Revision:    0,
			},
			wantMessage: "must be positive",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := validateAreaContent(test.content)
			if err == nil {
				t.Fatal("validateAreaContent() returned nil, expected an error")
			}
			if !strings.Contains(err.Error(), test.wantMessage) {
				t.Fatalf("validateAreaContent() error = %q, want it to contain %q", err, test.wantMessage)
			}
		})
	}
}

func TestValidateAreaUpdatePromptAcceptsConfiguredPrompt(t *testing.T) {
	t.Parallel()

	updatePrompt := AreaUpdatePrompt{
		AreaID:      "health",
		ContentType: "weekly_workout_routine",
		Prompt:      "Replace the routine with a balanced plan for the coming week.",
	}

	validationError := validateAreaUpdatePrompt(updatePrompt)
	if validationError != nil {
		t.Fatalf("validateAreaUpdatePrompt() returned an unexpected error: %v", validationError)
	}
}

// TestAreaUpdatePromptUsesBrowserJSONFieldNames guards the API shape before the
// HTTP layer begins returning prompts to React. The prompt text must be nested as
// an ordinary JSON string under the snake_case identifiers used by existing area
// responses.
func TestAreaUpdatePromptUsesBrowserJSONFieldNames(t *testing.T) {
	t.Parallel()

	encoded, err := json.Marshal(AreaUpdatePrompt{
		AreaID:      "health",
		ContentType: "weekly_workout_routine",
		Prompt:      "Create next week's routine.",
	})
	if err != nil {
		t.Fatalf("marshal AreaUpdatePrompt: %v", err)
	}

	want := `{"area_id":"health","content_type":"weekly_workout_routine","prompt":"Create next week's routine."}`
	if string(encoded) != want {
		t.Fatalf("encoded prompt = %s, want %s", encoded, want)
	}
}

// TestSaveAreaUpdatePromptCreatesAndPreservesExactText verifies the first save
// used by the prompt editor. Newlines and indentation are intentionally
// preserved so a person sees the same maintainable prompt after closing and
// reopening the editor.
func TestSaveAreaUpdatePromptCreatesAndPreservesExactText(t *testing.T) {
	t.Parallel()

	state := &promptStorageState{}
	database := sql.OpenDB(promptStorageConnector{state: state})
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("close prompt storage database: %v", err)
		}
	})

	prompt := "Create a balanced weekly routine.\n\n  Keep recovery days explicit."
	savedPrompt, err := saveAreaUpdatePrompt(
		context.Background(),
		database,
		" health ",
		" weekly_workout_routine ",
		prompt,
	)
	if err != nil {
		t.Fatalf("saveAreaUpdatePrompt() returned an unexpected error: %v", err)
	}

	if savedPrompt.AreaID != "health" || savedPrompt.ContentType != "weekly_workout_routine" {
		t.Fatalf(
			"saved prompt identifiers = %q/%q, want health/weekly_workout_routine",
			savedPrompt.AreaID,
			savedPrompt.ContentType,
		)
	}
	if savedPrompt.Prompt != prompt {
		t.Fatalf("saved prompt = %q, want exact text %q", savedPrompt.Prompt, prompt)
	}
	if !strings.Contains(state.query, "INSERT INTO personal_ai.area_update_prompt") {
		t.Fatalf("query = %q, want area_update_prompt INSERT", state.query)
	}
	if !strings.Contains(state.query, "ON CONFLICT (area_id, content_type)") {
		t.Fatalf("query = %q, want conflict upsert", state.query)
	}
	if !strings.Contains(state.query, "enabled = TRUE") {
		t.Fatalf("query = %q, want enabled reactivation", state.query)
	}
	if state.areaID != "health" || state.contentType != "weekly_workout_routine" {
		t.Fatalf(
			"query identifiers = %q/%q, want trimmed identifiers",
			state.areaID,
			state.contentType,
		)
	}
	if state.prompt != prompt {
		t.Fatalf("query prompt = %q, want exact text %q", state.prompt, prompt)
	}
	if !state.created {
		t.Fatal("saveAreaUpdatePrompt() did not create the missing prompt row")
	}
}

// TestSaveAreaUpdatePromptUpdatesAndReenablesExistingRow verifies that a save
// replaces a previous prompt and restores it to the enabled state. This is what
// lets the generic editor work for both already-active and disabled prompts.
func TestSaveAreaUpdatePromptUpdatesAndReenablesExistingRow(t *testing.T) {
	t.Parallel()

	state := &promptStorageState{
		storedPrompts: map[string]promptStorageRow{
			promptStorageKey("home", "maintenance_tasks"): {
				prompt:  "Old maintenance instructions.",
				enabled: false,
			},
		},
	}
	database := sql.OpenDB(promptStorageConnector{state: state})
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("close prompt storage database: %v", err)
		}
	})

	savedPrompt, err := saveAreaUpdatePrompt(
		context.Background(),
		database,
		"home",
		"maintenance_tasks",
		"Keep the maintenance queue current, including seasonal tasks.",
	)
	if err != nil {
		t.Fatalf("saveAreaUpdatePrompt() returned an unexpected error: %v", err)
	}
	if state.created {
		t.Fatal("saveAreaUpdatePrompt() created a row instead of updating the existing row")
	}
	stored := state.storedPrompts[promptStorageKey("home", "maintenance_tasks")]
	if !stored.enabled {
		t.Fatal("saved prompt row is disabled, want enabled")
	}
	if stored.prompt != savedPrompt.Prompt {
		t.Fatalf("stored prompt = %q, want returned prompt %q", stored.prompt, savedPrompt.Prompt)
	}
}

// TestLoadAreaUpdatePromptReportsMissingConfiguration verifies the read-side
// distinction needed by the HTTP GET route. An absent row and a disabled row
// both intentionally appear as the same not-configured state to callers.
func TestLoadAreaUpdatePromptReportsMissingConfiguration(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name  string
		state *promptStorageState
	}{
		{
			name:  "absent row",
			state: &promptStorageState{},
		},
		{
			name: "disabled row",
			state: &promptStorageState{
				storedPrompts: map[string]promptStorageRow{
					promptStorageKey("home", "maintenance_tasks"): {
						prompt:  "Dormant instructions.",
						enabled: false,
					},
				},
			},
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			database := sql.OpenDB(promptStorageConnector{state: test.state})
			t.Cleanup(func() {
				if err := database.Close(); err != nil {
					t.Errorf("close prompt storage database: %v", err)
				}
			})

			_, err := loadAreaUpdatePrompt(
				context.Background(),
				database,
				"home",
				"maintenance_tasks",
			)
			if !errors.Is(err, errAreaUpdatePromptNotConfigured) {
				t.Fatalf("loadAreaUpdatePrompt() error = %v, want not-configured error", err)
			}
			for _, requiredPredicate := range []string{
				"WHERE area_id = $1",
				"AND content_type = $2",
				"AND enabled = TRUE",
			} {
				if !strings.Contains(test.state.selectQuery, requiredPredicate) {
					t.Fatalf(
						"SELECT query = %q, want predicate %q",
						test.state.selectQuery,
						requiredPredicate,
					)
				}
			}
		})
	}
}

func TestValidateAreaUpdatePromptRejectsBlankFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		updatePrompt AreaUpdatePrompt
		wantMessage  string
	}{
		{
			name: "blank area id",
			updatePrompt: AreaUpdatePrompt{
				ContentType: "weekly_workout_routine",
				Prompt:      "Create the next workout routine.",
			},
			wantMessage: "area id is required",
		},
		{
			name: "blank content type",
			updatePrompt: AreaUpdatePrompt{
				AreaID: "health",
				Prompt: "Create the next workout routine.",
			},
			wantMessage: "content type is required",
		},
		{
			name: "blank prompt",
			updatePrompt: AreaUpdatePrompt{
				AreaID:      "meals",
				ContentType: "weekly_meal_recommendations",
				Prompt:      "  ",
			},
			wantMessage: "is required",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := validateAreaUpdatePrompt(test.updatePrompt)
			if err == nil {
				t.Fatal("validateAreaUpdatePrompt() returned nil, expected an error")
			}
			if !strings.Contains(err.Error(), test.wantMessage) {
				t.Fatalf("validateAreaUpdatePrompt() error = %q, want it to contain %q", err, test.wantMessage)
			}
		})
	}
}

// promptStorageState is the in-memory table used by the database/sql test
// driver. Every test owns a separate state value, so parallel tests cannot
// overwrite one another's observations.
type promptStorageState struct {
	query         string
	selectQuery   string
	areaID        string
	contentType   string
	prompt        string
	created       bool
	storedPrompts map[string]promptStorageRow
	queryError    error
}

// promptStorageRow models the only persisted fields that matter to the prompt
// API tests. enabled determines whether a GET should expose the row.
type promptStorageRow struct {
	prompt  string
	enabled bool
}

// promptStorageKey mirrors the table's composite primary key in the test
// double. A separator avoids ambiguity between identifiers such as a/bc and
// ab/c without affecting the SQL used by production code.
func promptStorageKey(areaID string, contentType string) string {
	return areaID + "\x00" + contentType
}

// promptStorageConnector gives database/sql the lightweight connection below.
// It is deliberately limited to this test file and never reads DATABASE_URL.
type promptStorageConnector struct {
	state *promptStorageState
}

func (connector promptStorageConnector) Connect(context.Context) (driver.Conn, error) {
	return &promptStorageConnection{state: connector.state}, nil
}

func (connector promptStorageConnector) Driver() driver.Driver {
	return promptStorageDriver{}
}

// promptStorageDriver exists only because driver.Connector requires a Driver
// method. database/sql opens connections through Connect above, so Open is not
// expected during these tests.
type promptStorageDriver struct{}

func (promptStorageDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("prompt storage test driver must be opened through its connector")
}

// promptStorageConnection implements the minimum database/sql interfaces used
// by QueryRowContext. Unsupported transaction and prepared-statement paths fail
// loudly if storage behavior changes unexpectedly.
type promptStorageConnection struct {
	state *promptStorageState
}

func (connection *promptStorageConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prompt storage test driver does not support prepared statements")
}

func (connection *promptStorageConnection) Close() error {
	return nil
}

func (connection *promptStorageConnection) Begin() (driver.Tx, error) {
	return nil, errors.New("prompt storage test driver does not support transactions")
}

// QueryContext supports exactly the prompt SELECT and INSERT ... ON CONFLICT
// statements used by storage.go. It models PostgreSQL's enabled filter and
// atomic upsert closely enough for focused storage and handler tests while
// failing loudly if a test accidentally exercises a different database path.
func (connection *promptStorageConnection) QueryContext(
	_ context.Context,
	query string,
	arguments []driver.NamedValue,
) (driver.Rows, error) {
	if connection.state == nil {
		return nil, errors.New("prompt storage test state is required")
	}
	if connection.state.queryError != nil {
		return nil, connection.state.queryError
	}

	if strings.Contains(query, "SELECT area_id, content_type, prompt") {
		return connection.selectAreaUpdatePrompt(query, arguments)
	}
	if strings.Contains(query, "INSERT INTO personal_ai.area_update_prompt") {
		return connection.upsertAreaUpdatePrompt(query, arguments)
	}
	return nil, errors.New("prompt storage test driver received an unexpected query")
}

// selectAreaUpdatePrompt first proves that storage.go sent the full scoped
// enabled-only query before the test double applies its own in-memory filtering.
// Without this check, a disabled-row test could pass even if production SQL
// accidentally removed WHERE enabled = TRUE or one of the identifier predicates.
func (connection *promptStorageConnection) selectAreaUpdatePrompt(
	query string,
	arguments []driver.NamedValue,
) (driver.Rows, error) {
	if len(arguments) != 2 {
		return nil, errors.New("prompt storage read must receive two arguments")
	}
	query = strings.Join(strings.Fields(query), " ")
	connection.state.selectQuery = query
	for _, requiredPredicate := range []string{
		"WHERE area_id = $1",
		"AND content_type = $2",
		"AND enabled = TRUE",
	} {
		if !strings.Contains(query, requiredPredicate) {
			return nil, fmt.Errorf("prompt storage read is missing required predicate %q", requiredPredicate)
		}
	}

	areaID, areaIDOK := arguments[0].Value.(string)
	contentType, contentTypeOK := arguments[1].Value.(string)
	if !areaIDOK || !contentTypeOK {
		return nil, errors.New("prompt storage read arguments must be strings")
	}

	// This mirrors the already-validated enabled predicate above. The map only
	// supplies fixture data; it does not stand in for a missing SQL condition.
	stored, found := connection.state.storedPrompts[promptStorageKey(areaID, contentType)]
	if !found || !stored.enabled {
		return &promptStorageRows{}, nil
	}
	return &promptStorageRows{values: []driver.Value{areaID, contentType, stored.prompt}}, nil
}

// upsertAreaUpdatePrompt records PostgreSQL's insert-or-conflict-update result.
// A nil map represents an initially empty table and is allocated at the first
// successful save, just as PostgreSQL creates the first row without setup work.
func (connection *promptStorageConnection) upsertAreaUpdatePrompt(
	query string,
	arguments []driver.NamedValue,
) (driver.Rows, error) {
	if len(arguments) != 3 {
		return nil, errors.New("prompt storage upsert must receive three arguments")
	}
	areaID, areaIDOK := arguments[0].Value.(string)
	contentType, contentTypeOK := arguments[1].Value.(string)
	prompt, promptOK := arguments[2].Value.(string)
	if !areaIDOK || !contentTypeOK || !promptOK {
		return nil, errors.New("prompt storage upsert arguments must be strings")
	}

	connection.state.query = strings.Join(strings.Fields(query), " ")
	connection.state.areaID = areaID
	connection.state.contentType = contentType
	connection.state.prompt = prompt
	key := promptStorageKey(areaID, contentType)
	if connection.state.storedPrompts == nil {
		connection.state.storedPrompts = make(map[string]promptStorageRow)
	}
	_, alreadyExists := connection.state.storedPrompts[key]
	connection.state.created = !alreadyExists
	connection.state.storedPrompts[key] = promptStorageRow{prompt: prompt, enabled: true}

	return &promptStorageRows{
		values: []driver.Value{areaID, contentType, prompt},
	}, nil
}

// promptStorageRows exposes one RETURNING row in the same column order selected
// by saveAreaUpdatePrompt.
type promptStorageRows struct {
	values   []driver.Value
	returned bool
}

func (rows *promptStorageRows) Columns() []string {
	return []string{"area_id", "content_type", "prompt"}
}

func (rows *promptStorageRows) Close() error {
	return nil
}

func (rows *promptStorageRows) Next(destination []driver.Value) error {
	if rows.returned || len(rows.values) == 0 {
		return io.EOF
	}
	copy(destination, rows.values)
	rows.returned = true
	return nil
}

// TestSaveAreaEntryStateChangesOnlyOneRawEntryField exercises the completion
// path without a live database. The fixture includes fields that the current UI
// does not need so this test guards the important storage promise: changing a
// checkbox cannot discard future entry metadata or generation details.
func TestSaveAreaEntryStateChangesOnlyOneRawEntryField(t *testing.T) {
	updatedAt := time.Date(2026, time.July, 26, 10, 30, 0, 0, time.UTC)
	untouchedEntry := `{"id":"check-batteries","title":"Check batteries","state":"done","metadata":[{"label":"Room","value":"Hall","attention":false}]}`
	state := &entryStateStorageState{
		documents: map[string]entryStateStorageRow{
			entryStateStorageKey("home", "maintenance_tasks"): {
				content:   `{"last_updated":"2026-07-26","generation_notes":{"source":"weekly","flags":[true,false]},"entries":[{"id":"replace-filter","title":"Replace filter","details":"Use the utility closet filter.","href":"","state":"open","metadata":[{"label":"Category","value":"Maintenance","attention":false}]},` + untouchedEntry + `]}`,
				revision:  7,
				updatedAt: updatedAt,
			},
		},
	}
	database := sql.OpenDB(entryStateStorageConnector{state: state})
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("close entry-state storage database: %v", err)
		}
	})

	saved, err := saveAreaEntryState(
		context.Background(),
		database,
		"home",
		"maintenance_tasks",
		"replace-filter",
		"done",
	)
	if err != nil {
		t.Fatalf("saveAreaEntryState() returned an unexpected error: %v", err)
	}
	if saved.AreaID != "home" || saved.ContentType != "maintenance_tasks" || saved.EntryID != "replace-filter" {
		t.Fatalf("saved identifiers = %#v, want home/maintenance_tasks/replace-filter", saved)
	}
	if saved.State != "done" || saved.Revision != 8 {
		t.Fatalf("saved state/revision = %q/%d, want done/8", saved.State, saved.Revision)
	}
	if !strings.Contains(state.readQuery, "WHERE area_id = $1 AND content_type = $2") {
		t.Fatalf("read query = %q, want document identifier predicates", state.readQuery)
	}
	for _, requiredClause := range []string{
		"SET content = $3::jsonb, revision = revision + 1",
		"AND revision = $4",
		"RETURNING area_id, content_type, content::text, revision, updated_at",
	} {
		if !strings.Contains(state.updateQuery, requiredClause) {
			t.Fatalf("update query = %q, want clause %q", state.updateQuery, requiredClause)
		}
	}
	if strings.Contains(state.updateQuery, "updated_at =") {
		t.Fatalf("update query = %q, must not change the weekly generation timestamp", state.updateQuery)
	}

	stored := state.documents[entryStateStorageKey("home", "maintenance_tasks")]
	if !stored.updatedAt.Equal(updatedAt) {
		t.Fatalf("updated_at = %s, want unchanged %s", stored.updatedAt, updatedAt)
	}

	var document map[string]json.RawMessage
	if err := json.Unmarshal([]byte(stored.content), &document); err != nil {
		t.Fatalf("decode stored document: %v", err)
	}
	if got := string(document["generation_notes"]); got != `{"source":"weekly","flags":[true,false]}` {
		t.Fatalf("generation_notes = %s, want unchanged raw value", got)
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(document["entries"], &entries); err != nil {
		t.Fatalf("decode stored entries: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entry count = %d, want 2", len(entries))
	}
	if got := string(entries[1]); got != untouchedEntry {
		t.Fatalf("untouched entry = %s, want exact raw JSON %s", got, untouchedEntry)
	}
	var changedEntry map[string]json.RawMessage
	if err := json.Unmarshal(entries[0], &changedEntry); err != nil {
		t.Fatalf("decode changed entry: %v", err)
	}
	if got := string(changedEntry["state"]); got != `"done"` {
		t.Fatalf("changed entry state = %s, want done", got)
	}
	if got := string(changedEntry["metadata"]); got != `[{"label":"Category","value":"Maintenance","attention":false}]` {
		t.Fatalf("changed entry metadata = %s, want unchanged raw value", got)
	}
}

// TestSaveAreaEntryStateReportsExpectedNotFoundAndConflictStates verifies the
// sentinels the HTTP layer needs for safe 404 and 409 responses. These cases are
// normal races or stale user selections, not database-driver failures.
func TestSaveAreaEntryStateReportsExpectedNotFoundAndConflictStates(t *testing.T) {
	for _, test := range []struct {
		name          string
		areaID        string
		contentType   string
		entryID       string
		state         *entryStateStorageState
		wantError     error
		wantUpdateSQL bool
	}{
		{
			name:        "unsupported document",
			areaID:      "meals",
			contentType: "weekly_meal_recommendations",
			entryID:     "dinner",
			state:       &entryStateStorageState{},
			wantError:   errAreaEntryStateNotConfigured,
		},
		{
			name:        "missing stored document",
			areaID:      "health",
			contentType: "weekly_workout_routine",
			entryID:     "monday",
			state:       &entryStateStorageState{},
			wantError:   errAreaEntryStateNotConfigured,
		},
		{
			name:        "missing entry",
			areaID:      "home",
			contentType: "maintenance_tasks",
			entryID:     "missing-task",
			state: &entryStateStorageState{documents: map[string]entryStateStorageRow{
				entryStateStorageKey("home", "maintenance_tasks"): entryStateFixtureDocument(),
			}},
			wantError: errAreaEntryStateNotFound,
		},
		{
			name:        "revision conflict",
			areaID:      "health",
			contentType: "weekly_workout_routine",
			entryID:     "monday",
			state: &entryStateStorageState{
				documents: map[string]entryStateStorageRow{
					entryStateStorageKey("health", "weekly_workout_routine"): entryStateFixtureDocument(),
				},
				forceRevisionConflict: true,
			},
			wantError:     errAreaEntryStateRevisionConflict,
			wantUpdateSQL: true,
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			database := sql.OpenDB(entryStateStorageConnector{state: test.state})
			t.Cleanup(func() {
				if err := database.Close(); err != nil {
					t.Errorf("close entry-state storage database: %v", err)
				}
			})

			_, err := saveAreaEntryState(
				context.Background(),
				database,
				test.areaID,
				test.contentType,
				test.entryID,
				"done",
			)
			if !errors.Is(err, test.wantError) {
				t.Fatalf("saveAreaEntryState() error = %v, want %v", err, test.wantError)
			}
			if gotUpdateSQL := test.state.updateQuery != ""; gotUpdateSQL != test.wantUpdateSQL {
				t.Fatalf("update executed = %t, want %t", gotUpdateSQL, test.wantUpdateSQL)
			}
		})
	}
}

// entryStateFixtureDocument provides the smallest valid entries document for
// tests that care about routing outcomes rather than raw-field preservation.
func entryStateFixtureDocument() entryStateStorageRow {
	return entryStateStorageRow{
		content:   `{"entries":[{"id":"monday","title":"Monday session","state":"open"}]}`,
		revision:  3,
		updatedAt: time.Date(2026, time.July, 26, 10, 30, 0, 0, time.UTC),
	}
}

// entryStateStorageState is an in-memory area_content table for this feature's
// storage and HTTP tests. It records SQL so tests verify the revision predicate
// and the deliberate boundary that entry state does not change updated_at.
type entryStateStorageState struct {
	documents             map[string]entryStateStorageRow
	readQuery             string
	updateQuery           string
	queryError            error
	forceRevisionConflict bool
}

// entryStateStorageRow models the database columns read by loadAreaContent and
// returned by the conditional UPDATE. content remains text because PostgreSQL's
// content::text conversion is the contract used by the production scanner.
type entryStateStorageRow struct {
	content   string
	revision  int
	updatedAt time.Time
}

func entryStateStorageKey(areaID string, contentType string) string {
	return areaID + "\x00" + contentType
}

type entryStateStorageConnector struct {
	state *entryStateStorageState
}

func (connector entryStateStorageConnector) Connect(context.Context) (driver.Conn, error) {
	return &entryStateStorageConnection{state: connector.state}, nil
}

func (connector entryStateStorageConnector) Driver() driver.Driver {
	return entryStateStorageDriver{}
}

type entryStateStorageDriver struct{}

func (entryStateStorageDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("entry-state storage test driver must be opened through its connector")
}

// entryStateStorageConnection implements only QueryContext because both the
// production read and conditional save use QueryRowContext with RETURNING.
type entryStateStorageConnection struct {
	state *entryStateStorageState
}

func (connection *entryStateStorageConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("entry-state storage test driver does not support prepared statements")
}

func (connection *entryStateStorageConnection) Close() error {
	return nil
}

func (connection *entryStateStorageConnection) Begin() (driver.Tx, error) {
	return nil, errors.New("entry-state storage test driver does not support transactions")
}

func (connection *entryStateStorageConnection) QueryContext(
	_ context.Context,
	query string,
	arguments []driver.NamedValue,
) (driver.Rows, error) {
	if connection.state == nil {
		return nil, errors.New("entry-state storage test state is required")
	}
	if connection.state.queryError != nil {
		return nil, connection.state.queryError
	}

	if strings.Contains(query, "SELECT area_id, content_type, content::text, revision, updated_at") {
		return connection.loadAreaContent(query, arguments)
	}
	if strings.Contains(query, "UPDATE personal_ai.area_content") {
		return connection.saveAreaEntryState(query, arguments)
	}
	return nil, errors.New("entry-state storage test driver received an unexpected query")
}

func (connection *entryStateStorageConnection) loadAreaContent(
	query string,
	arguments []driver.NamedValue,
) (driver.Rows, error) {
	if len(arguments) != 2 {
		return nil, errors.New("entry-state storage read must receive two arguments")
	}
	areaID, areaIDOK := arguments[0].Value.(string)
	contentType, contentTypeOK := arguments[1].Value.(string)
	if !areaIDOK || !contentTypeOK {
		return nil, errors.New("entry-state storage read arguments must be strings")
	}
	connection.state.readQuery = strings.Join(strings.Fields(query), " ")
	stored, found := connection.state.documents[entryStateStorageKey(areaID, contentType)]
	if !found {
		return &entryStateStorageRows{}, nil
	}
	return &entryStateStorageRows{values: [][]driver.Value{{
		areaID,
		contentType,
		stored.content,
		int64(stored.revision),
		stored.updatedAt,
	}}}, nil
}

func (connection *entryStateStorageConnection) saveAreaEntryState(
	query string,
	arguments []driver.NamedValue,
) (driver.Rows, error) {
	if len(arguments) != 4 {
		return nil, errors.New("entry-state storage save must receive four arguments")
	}
	areaID, areaIDOK := arguments[0].Value.(string)
	contentType, contentTypeOK := arguments[1].Value.(string)
	contentJSON, contentJSONOK := arguments[2].Value.(string)
	revision, revisionOK := arguments[3].Value.(int64)
	if !areaIDOK || !contentTypeOK || !contentJSONOK || !revisionOK {
		return nil, errors.New("entry-state storage save arguments have unexpected types")
	}

	connection.state.updateQuery = strings.Join(strings.Fields(query), " ")
	key := entryStateStorageKey(areaID, contentType)
	stored, found := connection.state.documents[key]
	if !found || int64(stored.revision) != revision {
		return &entryStateStorageRows{}, nil
	}
	if connection.state.forceRevisionConflict {
		// Model another writer winning after the initial read but before this
		// conditional UPDATE reaches PostgreSQL.
		stored.revision++
		connection.state.documents[key] = stored
		return &entryStateStorageRows{}, nil
	}

	stored.content = contentJSON
	stored.revision++
	connection.state.documents[key] = stored
	return &entryStateStorageRows{values: [][]driver.Value{{
		areaID,
		contentType,
		stored.content,
		int64(stored.revision),
		stored.updatedAt,
	}}}, nil
}

// entryStateStorageRows supports empty result sets for missing rows and
// revision conflicts, plus one RETURNING row for a successful conditional save.
type entryStateStorageRows struct {
	values [][]driver.Value
	index  int
}

func (rows *entryStateStorageRows) Columns() []string {
	return []string{"area_id", "content_type", "content", "revision", "updated_at"}
}

func (rows *entryStateStorageRows) Close() error {
	return nil
}

func (rows *entryStateStorageRows) Next(destination []driver.Value) error {
	if rows.index >= len(rows.values) {
		return io.EOF
	}
	copy(destination, rows.values[rows.index])
	rows.index++
	return nil
}
