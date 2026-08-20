package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// updater.go owns the "JSON in, JSON out" LLM update flow and the shared
// orchestration used to refresh weekly area documents.
//
// The lower-level updateJSON function sends one document to OpenRouter and
// validates the response. areaUpdateRunner coordinates storage around that call:
// it checks whether Home, Health, or Meals is due, loads the prompt only when
// it will actually be used, supplies calendar context, and saves with revision
// checking.
//
// A scheduled or manual refresh follows this sequence:
//
//  1. run chooses the Home, Health, and Meals documents.
//  2. updateTarget loads the current JSON for one document.
//  3. A scheduled run stops there when that document was already updated this
//     week. This is why its prompt is not fetched during ordinary due checks.
//  4. A due or manually forced run tries to claim a database lock for that
//     document. The lock works across Cloud Run instances, not only inside one
//     Go process. An overlapping request that cannot claim it stops immediately.
//  5. A request that claimed the lock reloads the document. If another request
//     saved a newer revision between the first read and the lock attempt, this
//     request also stops without calling the model a second time.
//  6. The runner loads the matching database prompt.
//  7. buildCalendarUpdatePrompt adds the weekly context appropriate for that
//     document: queue replacement rules for Home and calendar-plan rules for
//     Health and Meals.
//  8. updateJSON asks OpenRouter for replacement JSON and validates its shape.
//  9. saveAreaContent stores it only if another writer has not already changed
//     the document's revision.

// areaUpdateTarget identifies one independently stored document that participates
// in the weekly refresh. Keeping the content type explicit prevents a future
// second Health document from being updated merely because it shares an area id.
type areaUpdateTarget struct {
	AreaID      string
	ContentType string

	// ZDR is true only when this document must use Zero Data Retention. Home,
	// Health, and Meals are false; a future Email target will set this field to
	// true.
	ZDR bool
}

// weeklyAreaUpdateTargets is the explicit, stable order for every bulk and
// scheduled weekly refresh. Home comes first because its completed maintenance
// tasks are replaced before the Health workout and Meals plan are regenerated.
// Reading remains outside this weekly cadence until it has its own prompt and
// refresh policy.
var weeklyAreaUpdateTargets = []areaUpdateTarget{
	{AreaID: "home", ContentType: maintenanceTasksContentType, ZDR: false},
	{AreaID: "health", ContentType: weeklyWorkoutRoutineContentType, ZDR: false},
	{AreaID: "meals", ContentType: weeklyMealRecommendationsContentType, ZDR: false},
}

// errAreaUpdateTargetNotConfigured lets the HTTP layer distinguish an unknown
// area/content pair from an operational database or OpenRouter failure. The
// browser receives a safe 404 without exposing the internal error text.
var errAreaUpdateTargetNotConfigured = errors.New("area update target is not configured")

const (
	// Releasing a lock must not reuse a canceled HTTP request context. This short
	// independent deadline gives PostgreSQL time to unlock the session while
	// preventing a broken database connection from hanging request cleanup.
	areaUpdateUnlockTimeout = 5 * time.Second
)

// areaUpdateStatus describes the visible outcome of one target. The scheduler
// logs these results, while the future manual endpoint can return the same
// stable vocabulary to the frontend.
type areaUpdateStatus string

const (
	areaUpdateStatusUpdated areaUpdateStatus = "updated"
	areaUpdateStatusSkipped areaUpdateStatus = "skipped"
	areaUpdateStatusFailed  areaUpdateStatus = "failed"
)

// AreaUpdateResult reports what happened to one scheduled document. Error stays
// server-only because database and provider errors can contain operational
// details that should never be serialized into a browser response.
type AreaUpdateResult struct {
	AreaID      string           `json:"area_id"`
	ContentType string           `json:"content_type"`
	Status      areaUpdateStatus `json:"status"`
	Revision    int              `json:"revision,omitempty"`
	UpdatedAt   time.Time        `json:"updated_at,omitempty"`
	Error       error            `json:"-"`
}

// areaUpdateRunner holds the concrete Supabase pool used by the update workflow.
// updateTarget calls the named storage and model functions directly, avoiding
// broad dependency interfaces that can hide which implementation performs each
// step. generateJSON is the narrow exception used by updater_test.go: it lets a
// test supply untrusted model text and prove validation blocks storage without
// making a live OpenRouter request. Production leaves it nil and uses updateJSON.
type areaUpdateRunner struct {
	// db is the same database pool used by the HTTP server.
	db *sql.DB

	// targets identifies the documents this runner should update. Production
	// leaves it empty to use weeklyAreaUpdateTargets.
	targets []areaUpdateTarget

	// generateJSON returns raw model JSON for one target. It is intentionally
	// optional so the production path remains the concrete updateJSON function;
	// the runner, rather than a test double, always owns the validation-before-save
	// boundary below.
	generateJSON func(context.Context, string, string, string, time.Time, bool) (string, error)
}

// areaUpdateLock owns the one database session that holds a PostgreSQL advisory
// lock. Advisory locks belong to sessions, so the same *sql.Conn must remain
// reserved until release runs; returning it to the pool earlier could let an
// unrelated query inherit a lock that it does not know how to release.
type areaUpdateLock struct {
	conn *sql.Conn
	key  string
}

// areaUpdateLockKey creates one stable namespace for an area/content pair.
// Prefixing the area id with its length keeps pairs unambiguous even if a future
// identifier contains punctuation also used by the other identifier.
func areaUpdateLockKey(target areaUpdateTarget) string {
	return fmt.Sprintf("%d:%s%s", len(target.AreaID), target.AreaID, target.ContentType)
}

// tryAcquireAreaUpdateLock attempts to claim this target for one update.
//
// pg_try_advisory_lock returns immediately instead of making a second browser or
// scheduler request wait for a potentially long OpenRouter call. A failed claim
// is reported as "skipped" by updateTarget. The successful connection remains
// reserved in areaUpdateLock until release is called because PostgreSQL advisory
// locks belong to the session that acquired them.
func tryAcquireAreaUpdateLock(
	ctx context.Context,
	db *sql.DB,
	target areaUpdateTarget,
) (*areaUpdateLock, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if db == nil {
		return nil, false, errors.New("database is required")
	}

	lockKey := areaUpdateLockKey(target)
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, false, fmt.Errorf(
			"reserve %s/%s update lock connection: %w",
			target.AreaID,
			target.ContentType,
			err,
		)
	}

	var acquired bool
	err = conn.QueryRowContext(
		ctx,
		`SELECT pg_try_advisory_lock(hashtextextended($1, 0))`,
		lockKey,
	).Scan(&acquired)
	if err != nil {
		_ = conn.Close()
		return nil, false, fmt.Errorf(
			"acquire %s/%s update lock: %w",
			target.AreaID,
			target.ContentType,
			err,
		)
	}
	if acquired {
		return &areaUpdateLock{
			conn: conn,
			key:  lockKey,
		}, true, nil
	}

	// This connection did not acquire a lock and therefore has no session state
	// that must remain attached to the request.
	if err := conn.Close(); err != nil {
		return nil, false, fmt.Errorf(
			"release unused %s/%s update lock connection: %w",
			target.AreaID,
			target.ContentType,
			err,
		)
	}
	return nil, false, nil
}

// release relinquishes the advisory lock and returns its reserved connection to
// the pool. It uses an independent context because a browser disconnect or Cloud
// Run request timeout must not leave a lock attached to a pooled session.
func (lock *areaUpdateLock) release() error {
	if lock == nil || lock.conn == nil {
		return errors.New("area update lock connection is required")
	}

	releaseContext, cancelRelease := context.WithTimeout(
		context.Background(),
		areaUpdateUnlockTimeout,
	)
	defer cancelRelease()

	var released bool
	releaseError := lock.conn.QueryRowContext(
		releaseContext,
		`SELECT pg_advisory_unlock(hashtextextended($1, 0))`,
		lock.key,
	).Scan(&released)
	if releaseError != nil || !released {
		// A session with an uncertain lock state must never return to the shared
		// pool. driver.ErrBadConn tells database/sql to discard this physical
		// connection, and PostgreSQL releases session locks when it closes.
		_ = lock.conn.Raw(func(any) error {
			return driver.ErrBadConn
		})
		_ = lock.conn.Close()

		if releaseError != nil {
			return fmt.Errorf("release database advisory lock: %w", releaseError)
		}
		return errors.New("database advisory lock was not held")
	}

	if err := lock.conn.Close(); err != nil {
		return fmt.Errorf("return update lock connection to pool: %w", err)
	}
	return nil
}

// run refreshes every configured target independently. Scheduled calls pass
// force=false so documents already updated during the current Sunday-Saturday week
// are skipped before their prompts are loaded. The manual button will pass
// force=true so a person can intentionally regenerate all sections at any time.
//
// `(runner areaUpdateRunner)` is the method receiver. It means run belongs to an
// areaUpdateRunner value, is called as `runner.run(...)`, and can read the
// dependency fields above through the local name `runner`.
func (runner areaUpdateRunner) run(ctx context.Context, now time.Time, force bool) ([]AreaUpdateResult, error) {
	// Some simple callers and tests may not have a request context. Substitute a
	// context that never cancels so downstream database/model functions still
	// receive the non-nil context required by their APIs.
	if ctx == nil {
		ctx = context.Background()
	}

	// The supplied time decides both whether an update is due and which dates are
	// added to its prompt. Reject the zero value so a missing clock cannot silently
	// produce year-one scheduling context.
	if now.IsZero() {
		return nil, errors.New("update time is required")
	}

	// Every update reads and writes Supabase. Reject a missing pool before any
	// target begins instead of allowing a nil database pointer to reach storage.go.
	if runner.db == nil {
		return nil, errors.New("database is required")
	}

	// Copy the optional runner-specific targets into a local variable. Tests use
	// this field to run one section without also setting up the other section.
	targets := runner.targets

	// An empty target list means "use the application's normal weekly sections,"
	// currently Home, Health, and Meals in weeklyAreaUpdateTargets order.
	if len(targets) == 0 {
		targets = weeklyAreaUpdateTargets
	}

	// Preallocate enough result capacity for one outcome per target. The length
	// starts at zero because append below adds each completed outcome.
	results := make([]AreaUpdateResult, 0, len(targets))

	// Process Home, Health, and Meals one at a time. Sequential model calls avoid
	// sending multiple potentially large personal JSON requests at once.
	for _, target := range targets {
		// A canceled HTTP request or server shutdown should stop before another
		// potentially slow model request begins.
		if ctxError := ctx.Err(); ctxError != nil {
			return results, ctxError
		}

		// updateTarget contains the complete workflow for this one document. It
		// converts document-specific failures into a failed result, allowing the
		// loop to continue to the next independent section.
		results = append(results, runner.updateTarget(ctx, now, force, target))
	}

	// Reaching this point means every configured target produced an updated,
	// skipped, or failed result. A nil top-level error distinguishes those normal
	// per-target outcomes from invalid runner configuration or cancellation.
	return results, nil
}

// runOne updates exactly one configured AreaContent document.
//
// The individual Home, Health, and Meals buttons call this method with
// force=true.
// Looking up the target in weeklyAreaUpdateTargets is important: a browser
// cannot invent an arbitrary area id, content type, or privacy setting and cause
// the server to send that unconfigured document to OpenRouter.
func (runner areaUpdateRunner) runOne(
	ctx context.Context,
	now time.Time,
	force bool,
	areaID string,
	contentType string,
) ([]AreaUpdateResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if now.IsZero() {
		return nil, errors.New("update time is required")
	}

	areaID = strings.TrimSpace(areaID)
	if areaID == "" {
		return nil, errors.New("area id is required")
	}
	contentType = strings.TrimSpace(contentType)
	if contentType == "" {
		return nil, errors.New("content type is required")
	}

	// Tests may provide a smaller target list. Production leaves runner.targets
	// empty and therefore uses the normal Home, Health, and Meals configuration.
	targets := runner.targets
	if len(targets) == 0 {
		targets = weeklyAreaUpdateTargets
	}

	for _, target := range targets {
		if target.AreaID != areaID || target.ContentType != contentType {
			continue
		}

		// Validate the database only after finding the target. This allows an
		// unknown URL to return 404 without attempting any database operation.
		if runner.db == nil {
			return nil, errors.New("database is required")
		}

		// Keep the response shape consistent with the existing update-all route:
		// it is an array containing one result instead of a separate object shape.
		return []AreaUpdateResult{
			runner.updateTarget(ctx, now, force, target),
		}, nil
	}

	return nil, fmt.Errorf(
		"%w: %s/%s",
		errAreaUpdateTargetNotConfigured,
		areaID,
		contentType,
	)
}

// updateTarget performs one complete load, generate, and save operation. Errors
// become a failed result instead of stopping the other weekly targets; a model
// failure for one Home, Health, or Meals document must not prevent the other
// independent documents from receiving their updates.
func (runner areaUpdateRunner) updateTarget(
	ctx context.Context,
	now time.Time,
	force bool,
	target areaUpdateTarget,
) (result AreaUpdateResult) {
	result = AreaUpdateResult{
		AreaID:      target.AreaID,
		ContentType: target.ContentType,
		Status:      areaUpdateStatusFailed,
	}

	current, err := loadAreaContent(ctx, runner.db, target.AreaID, target.ContentType)
	if err != nil {
		result.Error = fmt.Errorf("load %s/%s content: %w", target.AreaID, target.ContentType, err)
		return result
	}

	// This check intentionally precedes loadPrompt. The hourly scheduler may read
	// lightweight content metadata many times during a week, but it retrieves the
	// potentially long prompt only on the run that will use it.
	if !force && !weeklyAreaUpdateDue(current.UpdatedAt, now) {
		result.Status = areaUpdateStatusSkipped
		result.Revision = current.Revision
		result.UpdatedAt = current.UpdatedAt
		return result
	}

	// Serialize the expensive part of the workflow across every Cloud Run
	// instance. An in-memory mutex would only coordinate requests handled by one
	// process, while PostgreSQL advisory locks are visible to every instance that
	// uses this database.
	updateLock, acquired, err := tryAcquireAreaUpdateLock(ctx, runner.db, target)
	if err != nil {
		result.Error = fmt.Errorf(
			"lock %s/%s update: %w",
			target.AreaID,
			target.ContentType,
			err,
		)
		return result
	}
	if !acquired {
		// Another request is already updating this exact document. Treat it as a
		// successful skip so this request neither spends model quota nor reports an
		// operational failure for expected overlap.
		result.Status = areaUpdateStatusSkipped
		result.Revision = current.Revision
		result.UpdatedAt = current.UpdatedAt
		return result
	}
	defer func() {
		if releaseError := updateLock.release(); releaseError != nil {
			wrappedReleaseError := fmt.Errorf(
				"release %s/%s update lock: %w",
				target.AreaID,
				target.ContentType,
				releaseError,
			)

			// Preserve an earlier load, model, or save failure while still making
			// the lock-cleanup failure visible in server logs. If the content was
			// otherwise saved successfully, report a failed result because an
			// unreleased pooled lock could prevent all later refreshes.
			if result.Error != nil {
				result.Error = errors.Join(result.Error, wrappedReleaseError)
			} else {
				result.Status = areaUpdateStatusFailed
				result.Error = wrappedReleaseError
			}
		}
	}()

	// Another request may have completed while this request waited for the lock.
	// Reloading under the lock reveals its new revision before any prompt or
	// OpenRouter work begins.
	lockedCurrent, err := loadAreaContent(
		ctx,
		runner.db,
		target.AreaID,
		target.ContentType,
	)
	if err != nil {
		result.Error = fmt.Errorf(
			"reload locked %s/%s content: %w",
			target.AreaID,
			target.ContentType,
			err,
		)
		return result
	}
	if lockedCurrent.Revision != current.Revision {
		result.Status = areaUpdateStatusSkipped
		result.Revision = lockedCurrent.Revision
		result.UpdatedAt = lockedCurrent.UpdatedAt
		return result
	}
	current = lockedCurrent

	// Recheck the calendar condition while holding the lock. The revision check
	// above normally catches a completed competing update, while this condition
	// also protects scheduled runs if database-maintained timing changes without
	// a revision change.
	if !force && !weeklyAreaUpdateDue(current.UpdatedAt, now) {
		result.Status = areaUpdateStatusSkipped
		result.Revision = current.Revision
		result.UpdatedAt = current.UpdatedAt
		return result
	}

	updatePrompt, err := loadAreaUpdatePrompt(ctx, runner.db, target.AreaID, target.ContentType)
	if err != nil {
		result.Error = fmt.Errorf("load %s/%s update prompt: %w", target.AreaID, target.ContentType, err)
		return result
	}

	// Home is a replacement queue rather than a calendar plan, so the helper
	// receives the content type and adds instructions that match this target.
	promptWithCalendar, err := buildCalendarUpdatePrompt(
		updatePrompt.Prompt,
		target.ContentType,
		now,
	)
	if err != nil {
		result.Error = fmt.Errorf("prepare %s/%s update prompt: %w", target.AreaID, target.ContentType, err)
		return result
	}

	// Carry this target's privacy requirement into the model request. Home,
	// Health, and Meals use the general free-model fallbacks; a future Email
	// target can use Ling with strict ZDR without changing this orchestration.
	// Tests may supply raw generated text, but validation remains in this runner
	// so every result follows the same trusted-before-storage path.
	generateJSON := runner.generateJSON
	if generateJSON == nil {
		generateJSON = updateJSON
	}
	updatedJSON, err := generateJSON(
		ctx,
		promptWithCalendar,
		string(current.Content),
		target.ContentType,
		now,
		target.ZDR,
	)
	if err != nil {
		result.Error = fmt.Errorf("generate %s/%s content: %w", target.AreaID, target.ContentType, err)
		return result
	}

	// The model response is untrusted even when its provider accepted a JSON
	// Schema. Validate it after generation and immediately before storage, using
	// this runner's supplied local time so stale Health or Meals dates cannot
	// reach Supabase. Home and generic content retain their existing validators.
	if err := validateGeneratedJSON(target.ContentType, string(current.Content), updatedJSON, now); err != nil {
		result.Error = fmt.Errorf("validate %s/%s generated content: %w", target.AreaID, target.ContentType, err)
		return result
	}

	saved, err := saveAreaContent(ctx, runner.db, current, updatedJSON)
	if err != nil {
		result.Error = fmt.Errorf("save %s/%s content: %w", target.AreaID, target.ContentType, err)
		return result
	}

	result.Status = areaUpdateStatusUpdated
	result.Revision = saved.Revision
	result.UpdatedAt = saved.UpdatedAt
	return result
}

// weeklyAreaUpdateDue uses calendar weeks instead of an exact 168-hour duration.
// Sunday begins a new planning week, so content last updated on Saturday becomes
// due on Sunday. Content generated at any point from Sunday through Saturday
// belongs to that current week and is skipped by later scheduled checks.
func weeklyAreaUpdateDue(updatedAt time.Time, now time.Time) bool {
	if updatedAt.IsZero() {
		return true
	}
	return updatedAt.Before(startOfSundayWeek(now))
}

// startOfSundayWeek returns midnight Sunday in now's location. The due check
// uses this boundary to decide whether the stored Home, Health, or Meals
// document came from the current Sunday-Saturday planning week. main.go
// supplies New York time, so the boundary follows the application's local
// calendar.
func startOfSundayWeek(now time.Time) time.Time {
	// Go numbers Sunday as weekday zero, Monday as one, and so on. Converting the
	// weekday directly to an integer therefore also gives the number of calendar
	// days to move backward to reach the most recent Sunday.
	daysSinceSunday := int(now.Weekday())

	// Remove the clock portion before moving backward. For example, a Friday at
	// 10:30 AM becomes Friday at midnight and then moves back five days to Sunday.
	todayAtMidnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	return todayAtMidnight.AddDate(0, 0, -daysSinceSunday)
}

// buildCalendarUpdatePrompt adds changing dates and target-specific weekly
// instructions to stable database prompts. Home has a task queue, not a
// Sunday-through-Saturday plan: its open tasks must remain untouched while only
// completed positions are replaced. Health and Meals retain the plan language.
func buildCalendarUpdatePrompt(
	storedPrompt string,
	contentType string,
	now time.Time,
) (string, error) {
	storedPrompt = strings.TrimSpace(storedPrompt)
	if storedPrompt == "" {
		return "", errors.New("stored update prompt is required")
	}

	weekStart := startOfSundayWeek(now)
	context := storedPrompt +
		"\n\nScheduling context:\n" +
		"Current date: " + now.Format("2006-01-02") + "\n" +
		"Target week starts Sunday: " + weekStart.Format("2006-01-02") + "\n"

	if strings.TrimSpace(contentType) == maintenanceTasksContentType {
		return context +
			"Weekly Home queue: preserve every task whose state is open exactly as " +
			"supplied. Replace only tasks whose state is done, keeping the same " +
			"entry count and order. Each replacement must be a new unfinished open task.", nil
	}

	return context +
		"Generate the complete plan for Sunday through Saturday, including the starting Sunday.", nil
}

// updateJSON sends one JSON document to the LLM with instructions for how to
// change it. The returned string is still JSON text because updateTarget then
// validates the same result immediately before it can be saved.
func updateJSON(
	ctx context.Context,
	prompt string,
	originalJSON string,
	contentType string,
	now time.Time,
	zdr bool,
) (string, error) {
	// Trim user-provided inputs at the boundary so the rest of the function can
	// treat blank prompt text or blank JSON content as clear validation errors.
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return "", errors.New("update prompt is required")
	}

	originalJSON = strings.TrimSpace(originalJSON)
	if originalJSON == "" {
		return "", errors.New("original json is required")
	}
	if !json.Valid([]byte(originalJSON)) {
		return "", errors.New("original json is not valid")
	}

	// The content type selects the response contract at the OpenRouter boundary.
	// Home, Health, and Meals each receive their own JSON Schema. Other
	// future update targets can continue through the generic same-shape path until
	// they also define a document-specific contract.
	contentType = strings.TrimSpace(contentType)
	if contentType == "" {
		return "", errors.New("content type is required")
	}

	// A nil context is allowed for simple local calls. Converting it to
	// context.Background keeps sendChatMessage from receiving a nil context.
	if ctx == nil {
		ctx = context.Background()
	}

	// sendChatMessageWithRetry builds the final chat request and calls
	// OpenRouter. It retries short-lived network or model-response timeouts
	// before returning an error to main.go.
	chat, err := sendChatMessageWithRetry(
		ctx,
		prompt,
		originalJSON,
		contentType,
		zdr,
	)
	if err != nil {
		return "", err
	}

	// Validate the provider response before returning it. updateTarget repeats
	// this check immediately before storage, which preserves the same safety for
	// its narrow test generator while this direct model boundary rejects malformed
	// output for ordinary production calls.
	updatedJSON := strings.TrimSpace(chat.Response)
	err = validateGeneratedJSON(contentType, originalJSON, updatedJSON, now)
	if err != nil {
		return "", err
	}

	return updatedJSON, nil
}

// sendChatMessageWithRetry calls the lower-level chat function and retries
// errors that look temporary. This specifically helps with OpenRouter responses
// that begin successfully but time out while the HTTP client is reading the
// response body.
func sendChatMessageWithRetry(
	ctx context.Context,
	prompt string,
	originalJSON string,
	contentType string,
	zdr bool,
) (chatResponse, error) {
	// Keep the retry count small so the command does not hang for a long time.
	// Attempt 1 is the normal call; attempts 2 and 3 are retry calls.
	maxAttempts := 3

	// lastErr stores the most recent failure so we can return the real underlying
	// error if every attempt fails.
	var lastErr error

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		// If the caller's context was canceled, stop immediately. Retrying cannot
		// help when the caller has already said the work should end.
		if ctx.Err() != nil {
			return chatResponse{}, ctx.Err()
		}

		// sendChatMessage makes the actual OpenRouter request. A successful call
		// returns the model name and JSON text response.
		chat, err := sendChatMessage(
			ctx,
			prompt,
			originalJSON,
			contentType,
			zdr,
		)
		if err == nil {
			fmt.Println("Model used:", chat.Model)
			return chat, nil
		}

		// Save this error in case this is the final attempt or the error is not
		// something retryable.
		lastErr = err

		// Only retry errors that look temporary. For example, a missing API key is
		// not retryable because the same request will fail every time.
		if !isRetryableChatError(err) {
			return chatResponse{}, err
		}

		// If this was the final allowed attempt, leave the loop and return the most
		// recent timeout or network error below.
		if attempt == maxAttempts {
			break
		}

		// Wait briefly before trying again. The wait gets longer each attempt:
		// attempt 1 waits 1 second, attempt 2 waits 2 seconds.
		retryDelay := time.Duration(attempt) * time.Second
		time.Sleep(retryDelay)
	}

	return chatResponse{}, fmt.Errorf("chat request failed after %d attempts: %w", maxAttempts, lastErr)
}

// isRetryableChatError decides whether a failed chat request is worth trying
// again. It returns true for timeout-style errors and false for permanent errors
// such as bad input, missing credentials, or invalid model responses.
func isRetryableChatError(err error) bool {
	// A nil error means there is nothing to retry.
	if err == nil {
		return false
	}

	// context.Canceled means the caller intentionally stopped the work. Retrying
	// would ignore that cancellation request, so it is not retryable.
	if errors.Is(err, context.Canceled) {
		return false
	}

	// context.DeadlineExceeded is Go's standard timeout error. The OpenRouter SDK
	// may wrap it, so errors.Is catches the wrapped form when available.
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	// Some HTTP client timeout errors arrive as plain text instead of a wrapped
	// context.DeadlineExceeded value. Lowercasing makes the checks insensitive to
	// capitalization differences in library error messages.
	errorText := strings.ToLower(err.Error())
	if strings.Contains(errorText, "context deadline exceeded") {
		return true
	}
	if strings.Contains(errorText, "client.timeout") {
		return true
	}
	if strings.Contains(errorText, "timeout") {
		return true
	}

	return false
}
