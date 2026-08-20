// updater_test.go covers scheduling and cross-request coordination without a
// live Supabase database or an OpenRouter request.
//
// Most tests call pure calendar helpers. The overlapping-update test uses a tiny
// database/sql driver defined at the bottom of this file. That driver represents
// two Cloud Run requests sharing PostgreSQL advisory-lock state and proves that
// the second request stops before it fetches a prompt or calls the model.
package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAreaUpdateRunnerRequiresDatabase(t *testing.T) {
	t.Parallel()

	runner := areaUpdateRunner{}
	now := time.Date(2026, time.July, 24, 10, 0, 0, 0, time.UTC)

	_, err := runner.run(context.Background(), now, false)
	if err == nil {
		t.Fatal("run() returned nil, expected a missing database error")
	}
	if !strings.Contains(err.Error(), "database is required") {
		t.Fatalf("run() error = %q, want it to mention the database", err)
	}
}

func TestWeeklyAreaUpdateDueUsesSundayBoundary(t *testing.T) {
	t.Parallel()

	location := time.FixedZone("America/New_York", -4*60*60)
	now := time.Date(2026, time.July, 24, 10, 0, 0, 0, location)

	tests := []struct {
		name      string
		updatedAt time.Time
		wantDue   bool
	}{
		{
			name:      "never updated",
			updatedAt: time.Time{},
			wantDue:   true,
		},
		{
			name:      "updated before current Sunday",
			updatedAt: time.Date(2026, time.July, 18, 23, 59, 59, 0, location),
			wantDue:   true,
		},
		{
			name:      "updated at current Sunday midnight",
			updatedAt: time.Date(2026, time.July, 19, 0, 0, 0, 0, location),
			wantDue:   false,
		},
		{
			name:      "updated later this week",
			updatedAt: time.Date(2026, time.July, 22, 12, 0, 0, 0, location),
			wantDue:   false,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			gotDue := weeklyAreaUpdateDue(test.updatedAt, now)
			if gotDue != test.wantDue {
				t.Fatalf("weeklyAreaUpdateDue() = %t, want %t", gotDue, test.wantDue)
			}
		})
	}
}

func TestBuildCalendarUpdatePromptAddsCurrentWeek(t *testing.T) {
	t.Parallel()

	location := time.FixedZone("America/New_York", -4*60*60)
	now := time.Date(2026, time.July, 24, 10, 0, 0, 0, location)

	prompt, err := buildCalendarUpdatePrompt(
		"Refresh the meals for the supplied target week.",
		weeklyMealRecommendationsContentType,
		now,
	)
	if err != nil {
		t.Fatalf("buildCalendarUpdatePrompt() returned an unexpected error: %v", err)
	}
	if !strings.Contains(prompt, "Current date: 2026-07-24") {
		t.Fatalf("prompt omitted current date: %q", prompt)
	}
	if !strings.Contains(prompt, "Target week starts Sunday: 2026-07-19") {
		t.Fatalf("prompt omitted target week: %q", prompt)
	}
	if !strings.Contains(prompt, "Sunday through Saturday, including the starting Sunday") {
		t.Fatalf("prompt omitted the full-week instruction: %q", prompt)
	}
}

func TestBuildCalendarUpdatePromptRejectsBlankStoredPrompt(t *testing.T) {
	t.Parallel()

	_, err := buildCalendarUpdatePrompt("   ", maintenanceTasksContentType, time.Now())
	if err == nil {
		t.Fatal("buildCalendarUpdatePrompt() returned nil, expected an error")
	}
}

func TestBuildCalendarUpdatePromptUsesQueueRulesForHome(t *testing.T) {
	t.Parallel()

	prompt, err := buildCalendarUpdatePrompt(
		"Refresh the household task queue.",
		maintenanceTasksContentType,
		time.Date(2026, time.July, 24, 10, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatalf("buildCalendarUpdatePrompt() returned an unexpected error: %v", err)
	}
	if !strings.Contains(prompt, "preserve every task whose state is open exactly") {
		t.Fatalf("Home prompt omitted the preserve-open instruction: %q", prompt)
	}
	if !strings.Contains(prompt, "Replace only tasks whose state is done") {
		t.Fatalf("Home prompt omitted the replace-done instruction: %q", prompt)
	}
	if strings.Contains(prompt, "Generate the complete plan for Sunday through Saturday") {
		t.Fatalf("Home prompt retained the conflicting calendar-plan instruction: %q", prompt)
	}
}

func TestWeeklyAreaUpdateTargetsIncludeHomeInStableOrder(t *testing.T) {
	t.Parallel()

	// This list drives the section endpoint, the manual update-all action, and
	// the scheduler whenever a runner has no test-specific targets. Keep the
	// assertion exact so a future target does not accidentally reorder Home.
	want := []areaUpdateTarget{
		{AreaID: "home", ContentType: maintenanceTasksContentType, ZDR: false},
		{AreaID: "health", ContentType: weeklyWorkoutRoutineContentType, ZDR: false},
		{AreaID: "meals", ContentType: weeklyMealRecommendationsContentType, ZDR: false},
	}
	if !reflect.DeepEqual(weeklyAreaUpdateTargets, want) {
		t.Fatalf("weeklyAreaUpdateTargets = %#v, want %#v", weeklyAreaUpdateTargets, want)
	}
}

func TestUpdateTargetSkipsModelWhileAnotherUpdateHoldsLock(t *testing.T) {
	t.Parallel()

	// The first lock represents an update already generating this Health plan on
	// another Cloud Run request. Its database row starts at revision 1.
	initialUpdatedAt := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	overlapState := newOverlappingUpdateDatabaseState(initialUpdatedAt)
	database := sql.OpenDB(overlappingUpdateConnector{state: overlapState})
	database.SetMaxOpenConns(4)
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("close overlapping-update database: %v", err)
		}
	})

	target := areaUpdateTarget{
		AreaID:      "health",
		ContentType: "weekly_workout_routine",
	}

	firstLock, acquired, err := tryAcquireAreaUpdateLock(context.Background(), database, target)
	if err != nil {
		t.Fatalf("acquire first update lock: %v", err)
	}
	if !acquired {
		t.Fatal("first update did not acquire its advisory lock")
	}

	// updateTarget loads the current row, sees that the other database session
	// owns the target lock, and must return without loading the model prompt.
	runner := areaUpdateRunner{db: database}
	result := runner.updateTarget(
		context.Background(),
		time.Date(2026, time.July, 24, 10, 0, 0, 0, time.UTC),
		true,
		target,
	)

	if err := firstLock.release(); err != nil {
		t.Fatalf("release first update lock: %v", err)
	}

	if result.Error != nil {
		t.Fatalf("updateTarget() returned an unexpected error: %v", result.Error)
	}
	if result.Status != areaUpdateStatusSkipped {
		t.Fatalf("updateTarget() status = %q, want %q", result.Status, areaUpdateStatusSkipped)
	}
	if result.Revision != 1 {
		t.Fatalf("updateTarget() revision = %d, want current revision 1", result.Revision)
	}
	if !result.UpdatedAt.Equal(initialUpdatedAt) {
		t.Fatalf(
			"updateTarget() updated time = %s, want %s",
			result.UpdatedAt,
			initialUpdatedAt,
		)
	}
	if promptQueries := overlapState.promptQueryCount(); promptQueries != 0 {
		t.Fatalf("updateTarget() made %d prompt queries, want 0 before model generation", promptQueries)
	}
}

func TestUpdateTargetRejectsWrongWeekMealsBeforeSave(t *testing.T) {
	t.Parallel()

	// This fixture has every required Meals field and starts on a Sunday, but it
	// belongs to the previous planning week. The runner's generatedJSON seam
	// replaces only OpenRouter for this test; updateTarget still executes its
	// real lock, prompt, validation, and storage-control flow.
	_, wrongWeekMealsJSON := structuredWeeklyPlanJSONs("2026-07-24", "2026-07-12")
	state := newValidationGateDatabaseState()
	database := sql.OpenDB(validationGateConnector{state: state})
	database.SetMaxOpenConns(4)
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("close validation-gate database: %v", err)
		}
	})

	runner := areaUpdateRunner{
		db: database,
		generateJSON: func(
			context.Context,
			string,
			string,
			string,
			time.Time,
			bool,
		) (string, error) {
			return wrongWeekMealsJSON, nil
		},
	}
	now := time.Date(2026, time.July, 24, 10, 0, 0, 0, time.UTC)
	result := runner.updateTarget(
		context.Background(),
		now,
		true,
		areaUpdateTarget{
			AreaID:      "meals",
			ContentType: weeklyMealRecommendationsContentType,
		},
	)

	if result.Status != areaUpdateStatusFailed {
		t.Fatalf("updateTarget() status = %q, want failed", result.Status)
	}
	if result.Error == nil || !strings.Contains(result.Error.Error(), "must equal target week 2026-07-19") {
		t.Fatalf("updateTarget() error = %v, want wrong-week validation failure", result.Error)
	}
	if saveAttempts := state.saveAttemptCount(); saveAttempts != 0 {
		t.Fatalf("updateTarget() attempted %d saves, want 0 after wrong-week validation", saveAttempts)
	}
}

// validationGateDatabaseState supplies the normal database reads and advisory
// lock used by updateTarget. It counts UPDATE attempts so the test above proves
// a syntactically valid but wrong-week model response cannot reach storage.
type validationGateDatabaseState struct {
	mu sync.Mutex

	lockHeld     bool
	saveAttempts int
}

func newValidationGateDatabaseState() *validationGateDatabaseState {
	return &validationGateDatabaseState{}
}

func (state *validationGateDatabaseState) saveAttemptCount() int {
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.saveAttempts
}

// validationGateConnector creates test connections that share the same lock
// and save counter, mirroring the separate PostgreSQL sessions used by the
// runner's advisory-lock connection and ordinary storage queries.
type validationGateConnector struct {
	state *validationGateDatabaseState
}

func (connector validationGateConnector) Connect(context.Context) (driver.Conn, error) {
	return &validationGateConnection{state: connector.state}, nil
}

func (connector validationGateConnector) Driver() driver.Driver {
	return validationGateDriver{state: connector.state}
}

type validationGateDriver struct {
	state *validationGateDatabaseState
}

func (testDriver validationGateDriver) Open(string) (driver.Conn, error) {
	return &validationGateConnection{state: testDriver.state}, nil
}

// validationGateConnection recognizes exactly the operations in this update
// path. Treating a save as an explicit operation makes an accidental bypass of
// the validation boundary visible even before the final assertion runs.
type validationGateConnection struct {
	state     *validationGateDatabaseState
	holdsLock bool
}

func (connection *validationGateConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepared statements are not supported by the validation-gate test driver")
}

func (connection *validationGateConnection) Close() error {
	connection.state.mu.Lock()
	defer connection.state.mu.Unlock()

	if connection.holdsLock {
		connection.state.lockHeld = false
		connection.holdsLock = false
	}
	return nil
}

func (connection *validationGateConnection) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions are not supported by the validation-gate test driver")
}

func (connection *validationGateConnection) QueryContext(
	_ context.Context,
	query string,
	_ []driver.NamedValue,
) (driver.Rows, error) {
	switch {
	case strings.Contains(query, "pg_try_advisory_lock"):
		connection.state.mu.Lock()
		acquired := !connection.state.lockHeld
		if acquired {
			connection.state.lockHeld = true
			connection.holdsLock = true
		}
		connection.state.mu.Unlock()
		return newSingleTestRow([]string{"acquired"}, acquired), nil

	case strings.Contains(query, "pg_advisory_unlock"):
		connection.state.mu.Lock()
		released := connection.holdsLock && connection.state.lockHeld
		if released {
			connection.state.lockHeld = false
			connection.holdsLock = false
		}
		connection.state.mu.Unlock()
		return newSingleTestRow([]string{"released"}, released), nil

	case strings.Contains(query, "FROM personal_ai.area_content"):
		return newSingleTestRow(
			[]string{"area_id", "content_type", "content", "revision", "updated_at"},
			"meals",
			weeklyMealRecommendationsContentType,
			`{"week_starting":"2026-07-12","recommendations":[]}`,
			int64(1),
			time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC),
		), nil

	case strings.Contains(query, "FROM personal_ai.area_update_prompt"):
		return newSingleTestRow(
			[]string{"area_id", "content_type", "prompt"},
			"meals",
			weeklyMealRecommendationsContentType,
			"Generate this week's practical meals.",
		), nil

	case strings.Contains(query, "UPDATE personal_ai.area_content"):
		connection.state.mu.Lock()
		connection.state.saveAttempts++
		connection.state.mu.Unlock()
		return nil, errors.New("wrong-week content unexpectedly reached storage")

	default:
		return nil, fmt.Errorf("validation-gate test driver received an unexpected query: %s", query)
	}
}

// overlappingUpdateDatabaseState is the shared PostgreSQL-like state used by
// every test connection. The mutex represents database-wide visibility: a lock
// acquired on one connection must be visible to all other connections.
type overlappingUpdateDatabaseState struct {
	mu sync.Mutex

	lockHeld      bool
	revision      int64
	updatedAt     time.Time
	promptQueries int
}

func newOverlappingUpdateDatabaseState(updatedAt time.Time) *overlappingUpdateDatabaseState {
	return &overlappingUpdateDatabaseState{
		revision:  1,
		updatedAt: updatedAt,
	}
}

func (state *overlappingUpdateDatabaseState) promptQueryCount() int {
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.promptQueries
}

// overlappingUpdateConnector gives database/sql fresh logical connections that
// all point to the same state above. It avoids a third-party SQL mocking package
// and keeps this concurrency test within updater_test.go.
type overlappingUpdateConnector struct {
	state *overlappingUpdateDatabaseState
}

func (connector overlappingUpdateConnector) Connect(context.Context) (driver.Conn, error) {
	return &overlappingUpdateConnection{state: connector.state}, nil
}

func (connector overlappingUpdateConnector) Driver() driver.Driver {
	return overlappingUpdateDriver{state: connector.state}
}

type overlappingUpdateDriver struct {
	state *overlappingUpdateDatabaseState
}

func (testDriver overlappingUpdateDriver) Open(string) (driver.Conn, error) {
	return &overlappingUpdateConnection{state: testDriver.state}, nil
}

// overlappingUpdateConnection tracks whether this exact database session owns
// the advisory lock. PostgreSQL releases session locks when a connection closes,
// so Close mirrors that behavior for cleanup and failed-test paths.
type overlappingUpdateConnection struct {
	state     *overlappingUpdateDatabaseState
	holdsLock bool
}

func (connection *overlappingUpdateConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepared statements are not supported by the overlap test driver")
}

func (connection *overlappingUpdateConnection) Close() error {
	connection.state.mu.Lock()
	defer connection.state.mu.Unlock()

	if connection.holdsLock {
		connection.state.lockHeld = false
		connection.holdsLock = false
	}
	return nil
}

func (connection *overlappingUpdateConnection) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions are not supported by the overlap test driver")
}

// QueryContext implements only the three database operations reached by this
// test: advisory-lock attempts, advisory unlock, and loading one content row. A
// prompt query is counted and rejected so an accidental model path fails loudly.
func (connection *overlappingUpdateConnection) QueryContext(
	_ context.Context,
	query string,
	_ []driver.NamedValue,
) (driver.Rows, error) {
	switch {
	case strings.Contains(query, "pg_try_advisory_lock"):
		connection.state.mu.Lock()
		acquired := !connection.state.lockHeld
		if acquired {
			connection.state.lockHeld = true
			connection.holdsLock = true
		}
		connection.state.mu.Unlock()

		return newSingleTestRow([]string{"acquired"}, acquired), nil

	case strings.Contains(query, "pg_advisory_unlock"):
		connection.state.mu.Lock()
		released := connection.holdsLock && connection.state.lockHeld
		if released {
			connection.state.lockHeld = false
			connection.holdsLock = false
		}
		connection.state.mu.Unlock()
		return newSingleTestRow([]string{"released"}, released), nil

	case strings.Contains(query, "FROM personal_ai.area_content"):
		connection.state.mu.Lock()
		revision := connection.state.revision
		updatedAt := connection.state.updatedAt
		connection.state.mu.Unlock()

		return newSingleTestRow(
			[]string{"area_id", "content_type", "content", "revision", "updated_at"},
			"health",
			"weekly_workout_routine",
			`{"entries":[{"id":"current-plan"}]}`,
			revision,
			updatedAt,
		), nil

	case strings.Contains(query, "FROM personal_ai.area_update_prompt"):
		connection.state.mu.Lock()
		connection.state.promptQueries++
		connection.state.mu.Unlock()
		return nil, errors.New("overlapping update unexpectedly loaded the model prompt")

	default:
		return nil, fmt.Errorf("overlap test driver received an unexpected query: %s", query)
	}
}

// singleTestRow is the smallest driver.Rows implementation needed to return one
// scalar lock result or one area-content record through database/sql.
type singleTestRow struct {
	columns []string
	values  []driver.Value
	read    bool
}

func newSingleTestRow(columns []string, values ...driver.Value) *singleTestRow {
	return &singleTestRow{
		columns: columns,
		values:  values,
	}
}

func (row *singleTestRow) Columns() []string {
	return row.columns
}

func (row *singleTestRow) Close() error {
	return nil
}

func (row *singleTestRow) Next(destination []driver.Value) error {
	if row.read {
		return io.EOF
	}
	row.read = true
	copy(destination, row.values)
	return nil
}
