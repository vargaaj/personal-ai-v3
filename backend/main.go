// main.go starts the long-running HTTP server used by both local development
// and Cloud Run. It loads configuration, opens the Supabase Postgres connection,
// and exposes stored area content through a small JSON API. It also connects the
// shared Health/Meals updater to separate manual and scheduled endpoints.
//
// Server and data-flow map
//
//	Google Cloud Scheduler (runs outside this Go server)
//	  |
//	  +-- Every Sunday at 6:00 AM America/New_York
//	        -> POST /api/areas/scheduled-weekly-update
//	        -> wakes Cloud Run if it has scaled to zero
//	        -> Cloud Run starts main() before handling the request when necessary
//
//	main()
//	  |
//	  +-- godotenv.Load(".env.local")
//	  |     Loads local DATABASE_URL and OPENROUTER_API_KEY values.
//	  |
//	  +-- openDatabase(...)
//	  |     Opens and verifies db, the shared Supabase connection pool.
//	  |
//	  +-- time.LoadLocation("America/New_York")
//	  |     Supplies the timezone used to calculate Sunday-Saturday weeks.
//	  |
//	  +-- serverApplication{db: db}
//	  |     Owns the concrete database pool used by the HTTP handlers.
//	  |
//	  +-- areaUpdateRunner{db: db}
//	  |     Owns the same concrete database pool used by weekly updates.
//	  |
//	  +-- localizedAreaUpdater{runner: updateRunner, location: updateLocation}
//	  |     Adds the current New York time whenever its run method is called.
//	  |     This concrete updater is stored as serverApplication.updater.
//	  |
//	  +-- serverApplication.newHTTPHandler()
//	  |     Registers the browser-facing routes:
//	  |
//	  |       GET /api/areas
//	  |         -> loadAreaContents(ctx, serverApplication.db)
//	  |         -> Supabase area_content rows
//	  |         -> writeAreaContentsResponse(...)
//	  |         -> JSON response consumed by fetchReviewAreas(...)
//	  |
//	  |       GET /api/areas/{areaID}/{contentType}/prompt
//	  |       PUT /api/areas/{areaID}/{contentType}/prompt
//	  |         -> loadAreaUpdatePrompt(...) or saveAreaUpdatePrompt(...)
//	  |         -> Supabase area_update_prompt row for any stored area document
//	  |         -> JSON response consumed by the prompt editor
//	  |         -> does not regenerate the matching area_content document
//	  |
//	  |       PUT /api/areas/{areaID}/{contentType}/entries/{entryID}/state
//	  |         -> saveAreaEntryState(...)
//	  |         -> reads then conditionally saves one Home or Health entry state
//	  |         -> preserves every other JSON field and leaves updated_at unchanged
//	  |         -> JSON response consumed by the Review completion controls
//	  |
//	  |       POST /api/areas/weekly-update
//	  |         -> serverApplication.updater.run(force=true)
//	  |         -> same areaUpdateRunner/updateTarget path above
//	  |         -> force=true bypasses weeklyAreaUpdateDue's skip
//	  |         -> writeAreaUpdateResultsResponse(...)
//	  |         -> JSON results consumed by the frontend update button
//	  |
//	  |       POST /api/areas/{areaID}/{contentType}/update
//	  |         -> called by one Home, Health, or Meals section button
//	  |         -> serverApplication.updater.runOne(force=true, ...)
//	  |         -> validates the pair against weeklyAreaUpdateTargets
//	  |         -> runs the same updateTarget(...) path for only that document
//	  |         -> returns an array containing one safe result
//	  |
//	  |       POST /api/areas/scheduled-weekly-update
//	  |         -> called by Google Cloud Scheduler on Sunday morning
//	  |         -> serverApplication.updater.run(force=false)
//	  |         -> areaUpdateRunner.run(...)
//	  |         -> updateTarget(...) once for Home, Health, and Meals
//	  |              |
//	  |              +-- loadAreaContent(ctx, db, ...)
//	  |              |     Reads the current JSON, revision, and updated_at.
//	  |              |
//	  |              +-- weeklyAreaUpdateDue(...)
//	  |                    |
//	  |                    +-- updated this Sunday-Saturday week
//	  |                    |     -> return a skipped result
//	  |                    |     -> do not load the prompt or call OpenRouter
//	  |                    |
//	  |                    +-- due for this week
//	  |                          -> loadAreaUpdatePrompt(ctx, db, ...)
//	  |                          -> buildCalendarUpdatePrompt(...)
//	  |                          -> updateJSON(...)
//	  |                          -> sendChatMessageWithRetry(...)
//	  |                          -> sendChatMessage(...)
//	  |                          -> saveAreaContent(ctx, db, ...)
//	  |                               Saves only if the revision is unchanged.
//	  |         -> writeAreaUpdateResultsResponse(...)
//	  |         -> returns HTTP 500 when either document fails so Scheduler retries
//	  |         -> duplicate scheduler deliveries safely return skipped results
//	  |
//	  +-- server.ListenAndServe()
//	        Accepts browser and Cloud Scheduler HTTP requests.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// serverApplication contains the concrete services used by HTTP requests. API
// handlers read through db or call updater directly. frontendFiles is optional:
// Vite serves the interface during local development, while the Cloud Run
// container supplies the built React files through FRONTEND_DIST_DIR.
type serverApplication struct {
	db            *sql.DB
	updater       *localizedAreaUpdater
	frontendFiles fs.FS
}

// localizedAreaUpdater combines the shared update workflow with the timezone
// used to decide calendar weeks. Its named run method replaces the previous
// anonymous function assignment, keeping the control flow easier to find and
// read.
type localizedAreaUpdater struct {
	runner   areaUpdateRunner
	location *time.Location
}

// run supplies the current localized time and forwards the force choice to the
// shared runner. The scheduled endpoint passes false; the manual endpoint passes
// true.
//
// `(updater localizedAreaUpdater)` is called a method receiver in Go. It means
// run is a method belonging to the localizedAreaUpdater type rather than a
// standalone function. `updater` is the local variable name used inside this
// method, and `localizedAreaUpdater` is its type. That is why the body can read
// `updater.location` and call `updater.runner.run(...)`.
//
// Given a value such as `application.updater`, callers invoke this method as
// `application.updater.run(ctx, true)`. Go places that updater value into the
// receiver variable named `updater`, then places ctx and true into the ordinary
// parameters that follow the method name.
//
// This is a value receiver rather than a pointer receiver because run only reads
// the small updater value; it does not replace its runner or location fields.
func (updater localizedAreaUpdater) run(ctx context.Context, force bool) ([]AreaUpdateResult, error) {
	if updater.location == nil {
		return nil, errors.New("area update location is required")
	}
	return updater.runner.run(ctx, time.Now().In(updater.location), force)
}

// runOne is the localizedAreaUpdater method used by an individual area button.
// Like run above, `(updater localizedAreaUpdater)` is its method receiver. The
// method supplies New York time, then asks areaUpdateRunner to find and update
// only the area/content pair captured from the request URL.
func (updater localizedAreaUpdater) runOne(
	ctx context.Context,
	force bool,
	areaID string,
	contentType string,
) ([]AreaUpdateResult, error) {
	if updater.location == nil {
		return nil, errors.New("area update location is required")
	}
	return updater.runner.runOne(
		ctx,
		time.Now().In(updater.location),
		force,
		areaID,
		contentType,
	)
}

func main() {
	// During local development, godotenv copies values from backend/.env.local
	// into this process's environment. In Cloud Run, .env.local is absent and
	// Secret Manager exposes the database secret as an environment variable named
	// DATABASE_URL instead. Both paths deliberately end at the same os.Getenv
	// call below, so using a Cloud Run environment-variable secret requires no Go
	// code change. A missing local file is therefore expected in production.
	envFileError := godotenv.Load(".env.local")
	if envFileError != nil {
		if os.IsNotExist(envFileError) {
			log.Println("no .env.local found; using system environment")
		} else {
			log.Fatal(envFileError)
		}
	}

	// Confirm that Supabase is reachable before accepting HTTP requests. A short
	// startup timeout prevents invalid credentials or networking problems from
	// leaving a Cloud Run instance stuck indefinitely while it starts.
	connectContext, cancelConnect := context.WithTimeout(context.Background(), 10*time.Second)

	// os.Getenv always reads from the current process environment. Locally,
	// godotenv populated DATABASE_URL above; in Cloud Run, the platform injects
	// the Secret Manager value before the Go process starts.
	databaseURL := os.Getenv("DATABASE_URL")
	db, err := openDatabase(connectContext, databaseURL)

	// The connection attempt is finished, so cancel the timeout context now. This
	// immediately releases its timer and related resources instead of retaining
	// them for the rest of the server's lifetime.
	cancelConnect()
	if err != nil {
		log.Fatal(err)
	}

	// openDatabase returns a connection pool. Schedule its Close method for when
	// main exits so idle database connections are released during server shutdown.
	defer db.Close()

	// Weekly documents are planned as Sunday-Saturday weeks in the user's local
	// timezone. Loading the IANA location preserves correct dates across daylight
	// saving changes instead of using a fixed UTC offset.
	updateLocation, locationError := time.LoadLocation("America/New_York")
	if locationError != nil {
		log.Fatal(locationError)
	}

	// `&serverApplication{...}` constructs the value and returns its address, so
	// application has type *serverApplication. Every registered HTTP closure then
	// refers to this one shared application instead of receiving a struct copy.
	// serverApplication owns the concrete database pool used by every handler and
	// update operation; no loader function is hidden behind a main.go type alias.
	application := &serverApplication{
		db: db,
	}

	// Local development normally leaves FRONTEND_DIST_DIR empty because Vite
	// serves the interface on port 5173 and proxies /api requests here. The
	// production Dockerfile sets this variable to the directory copied into the
	// final image, allowing this same Go process to serve both the interface and
	// API from one Cloud Run origin.
	frontendDirectory := strings.TrimSpace(os.Getenv("FRONTEND_DIST_DIR"))
	if frontendDirectory != "" {
		frontendFiles := os.DirFS(frontendDirectory)
		indexInformation, indexError := fs.Stat(frontendFiles, "index.html")
		if indexError != nil {
			log.Fatalf("open frontend index in %s: %v", frontendDirectory, indexError)
		}
		if indexInformation.IsDir() {
			log.Fatalf("frontend index in %s is a directory", frontendDirectory)
		}

		application.frontendFiles = frontendFiles
		log.Printf("serving frontend files from %s", frontendDirectory)
	}

	// Give the update runner the same concrete database pool. updater.go calls
	// loadAreaContent, loadAreaUpdatePrompt, updateJSON, and saveAreaContent
	// directly, so there are no loader aliases or anonymous functions here.
	updateRunner := areaUpdateRunner{
		db: db,
	}

	// The updater field has type *localizedAreaUpdater so nil can mean "updates
	// are not configured"; the handlers check that state before using it. The &
	// returns a non-nil pointer to this configured value. Go automatically reads
	// through that pointer when its run and runOne methods are called.
	//
	// Both the manual browser endpoint and the due-only Cloud Scheduler endpoint
	// call this exact updater.
	application.updater = &localizedAreaUpdater{
		runner:   updateRunner,
		location: updateLocation,
	}

	// Cloud Run provides PORT automatically. Port 8081 is the local fallback,
	// allowing the server to start with `go run .` without setting PORT manually.
	port := strings.TrimSpace(os.Getenv("PORT"))
	if port == "" {
		port = "8081"
	}

	// The & creates a pointer to this http.Server value. Server methods maintain
	// internal state while requests are running, so they must operate on this one
	// shared instance rather than a copied value. It also avoids copying fields
	// such as mutexes that Go's HTTP server uses for synchronization.
	server := &http.Server{
		Addr:    ":" + port,
		Handler: application.newHTTPHandler(),
		// Limit the time allowed for request headers. This protects the server from
		// clients that open a connection and then send headers extremely slowly.
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("personal-ai server listening on http://localhost:%s", port)

	// ListenAndServe blocks here while the server accepts requests. If a future
	// graceful-shutdown path closes the server, Go returns http.ErrServerClosed;
	// that expected shutdown signal should not be logged as a fatal failure.
	serverError := server.ListenAndServe()
	if errors.Is(serverError, http.ErrServerClosed) {
		return
	}
	if serverError != nil {
		log.Fatal(serverError)
	}
}

// logAreaUpdateResults keeps internal errors in server logs and records only
// meaningful activity. Scheduled skipped results are intentionally quiet.
func logAreaUpdateResults(trigger string, results []AreaUpdateResult) {
	for _, result := range results {
		switch result.Status {
		case areaUpdateStatusUpdated:
			log.Printf(
				"%s area update completed: %s/%s revision %d",
				trigger,
				result.AreaID,
				result.ContentType,
				result.Revision,
			)
		case areaUpdateStatusFailed:
			log.Printf(
				"%s area update failed: %s/%s: %v",
				trigger,
				result.AreaID,
				result.ContentType,
				result.Error,
			)
		}
	}
}

// newHTTPHandler registers the application's routes. Each request calls a
// concrete method or storage function: GET calls loadAreaContents with
// application.db, prompt and entry-state routes are composed by their focused
// registrars, and POST calls application.updater.run.
func (application *serverApplication) newHTTPHandler() http.Handler {
	mux := http.NewServeMux()

	// The React Review screen calls this endpoint on initial load. Content is a
	// json.RawMessage in AreaContent, so encoding the response embeds each stored
	// JSON object directly rather than returning an escaped JSON string.
	mux.HandleFunc("GET /api/areas", func(w http.ResponseWriter, r *http.Request) {
		contents, err := loadAreaContents(r.Context(), application.db)
		writeAreaContentsResponse(w, contents, err)
	})

	// The extracted registrar keeps the prompt feature grouped in one file. Go's
	// ServeMux resolves overlaps by pattern specificity, so its method-specific
	// routes and prompt fallback retain precedence over the frontend catch-all.
	registerAreaUpdatePromptRoutes(mux, application.db)

	// Completion controls on Home maintenance tasks and Health workouts use a
	// separate registrar because they mutate one entry inside a stored document.
	// Its storage function reads the current revision before its conditional save,
	// so this route cannot overwrite a concurrent weekly regeneration.
	registerAreaEntryStateRoutes(mux, application.db)

	// The Review screen's manual button calls this endpoint to regenerate all
	// weekly documents immediately. Passing force=true bypasses the current-week
	// skip, but the same prompts, model validation, and revision-safe save used by
	// the scheduler still apply.
	mux.HandleFunc("POST /api/areas/weekly-update", func(w http.ResponseWriter, r *http.Request) {
		if application.updater == nil {
			http.Error(w, "area updates are not configured", http.StatusServiceUnavailable)
			return
		}

		results, err := application.updater.run(r.Context(), true)
		writeAreaUpdateResultsResponse(
			w,
			"manual",
			results,
			manualAreaUpdateFailureHTTPStatus,
			err,
		)
	})

	// A Home, Health, or Meals section button calls this route to regenerate only
	// its own AreaContent document. Go's ServeMux places the two brace-delimited URL
	// segments into r.PathValue("areaID") and r.PathValue("contentType").
	mux.HandleFunc("POST /api/areas/{areaID}/{contentType}/update", func(w http.ResponseWriter, r *http.Request) {
		if application.updater == nil {
			http.Error(w, "area updates are not configured", http.StatusServiceUnavailable)
			return
		}

		areaID := r.PathValue("areaID")
		contentType := r.PathValue("contentType")
		results, err := application.updater.runOne(
			r.Context(),
			true,
			areaID,
			contentType,
		)
		if errors.Is(err, errAreaUpdateTargetNotConfigured) {
			http.Error(w, "area content is not configured for updates", http.StatusNotFound)
			return
		}

		writeAreaUpdateResultsResponse(
			w,
			"manual individual",
			results,
			manualAreaUpdateFailureHTTPStatus,
			err,
		)
	})

	// Google Cloud Scheduler calls this separate endpoint every Sunday morning.
	// Passing force=false is important because Cloud Scheduler guarantees that it
	// will attempt delivery, but a request can occasionally be delivered more than
	// once. The Sunday-week due check makes a duplicate call return "skipped"
	// instead of generating and saving a second meal or workout plan.
	mux.HandleFunc("POST /api/areas/scheduled-weekly-update", func(w http.ResponseWriter, r *http.Request) {
		if application.updater == nil {
			http.Error(w, "area updates are not configured", http.StatusServiceUnavailable)
			return
		}

		results, err := application.updater.run(r.Context(), false)
		writeAreaUpdateResultsResponse(
			w,
			"scheduled",
			results,
			scheduledAreaUpdateFailureHTTPStatus,
			err,
		)
	})

	// When the production container supplies a React bundle, this final
	// catch-all handles paths that were not claimed by the API routes above.
	// Registering it without an HTTP method lets the handler return a true 404 for
	// unsupported methods on unknown API paths. Known prompt paths have the more
	// specific methodless fallback above and intentionally return 405 instead.
	if application.frontendFiles != nil {
		mux.Handle("/", newFrontendHandler(application.frontendFiles))
	}

	return mux
}

const (
	// Vite gives production assets content-based names, so a changed JavaScript,
	// CSS, or font file receives a different URL. Those files can therefore stay
	// in browser caches for a year without hiding a newer deployment.
	frontendAssetCacheControl = "public, max-age=31536000, immutable"

	// index.html contains the current hashed asset names. Revalidating it ensures
	// a returning visitor discovers a new deployment instead of loading asset
	// URLs from an older page shell.
	frontendIndexCacheControl = "no-cache"
)

// newFrontendHandler serves the Vite production bundle from the same origin as
// the Go API. Exact files such as /assets/index-abc.js are returned directly.
// A browser navigation such as /review falls back to index.html so React Router
// can choose the visible screen after the page loads. Unknown API paths never
// receive HTML, which keeps misspelled frontend requests visible as real 404s.
func newFrontendHandler(frontendFiles fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Browser page loads use GET or HEAD. Rejecting other methods prevents a
		// misspelled mutation route from being mistaken for an ordinary React
		// navigation and receiving the application shell.
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}

		// The registered API routes are more specific and reach their handlers
		// first. This check covers only unknown API paths that fall through to the
		// catch-all, preserving a machine-readable 404 boundary instead of sending
		// index.html to fetch().
		if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}

		requestedFile := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if requestedFile == "." || requestedFile == "" {
			writeFrontendFile(
				w,
				r,
				frontendFiles,
				"index.html",
				frontendIndexCacheControl,
			)
			return
		}

		fileInformation, statError := fs.Stat(frontendFiles, requestedFile)
		if statError == nil && !fileInformation.IsDir() {
			cacheControl := frontendIndexCacheControl
			if strings.HasPrefix(requestedFile, "assets/") {
				cacheControl = frontendAssetCacheControl
			}
			writeFrontendFile(w, r, frontendFiles, requestedFile, cacheControl)
			return
		}

		// Browser navigation requests advertise that they accept HTML. Missing
		// scripts, fonts, API paths, and other non-page resources should remain
		// 404s rather than receiving HTML with an incorrect 200 status.
		if !strings.Contains(r.Header.Get("Accept"), "text/html") {
			http.NotFound(w, r)
			return
		}

		writeFrontendFile(
			w,
			r,
			frontendFiles,
			"index.html",
			frontendIndexCacheControl,
		)
	})
}

// writeFrontendFile writes one already-selected bundle file. It derives the
// response type from the extension so browsers interpret HTML, JavaScript, CSS,
// and bundled fonts correctly. HEAD requests receive the same headers without a
// body, matching the normal HTTP behavior used by uptime and cache checks.
func writeFrontendFile(
	w http.ResponseWriter,
	r *http.Request,
	frontendFiles fs.FS,
	fileName string,
	cacheControl string,
) {
	content, readError := fs.ReadFile(frontendFiles, fileName)
	if readError != nil {
		http.NotFound(w, r)
		return
	}

	contentType := mime.TypeByExtension(path.Ext(fileName))
	if contentType == "" {
		contentType = http.DetectContentType(content)
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", cacheControl)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(content)))
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}

	if _, writeError := w.Write(content); writeError != nil {
		log.Printf("write frontend file %s: %v", fileName, writeError)
	}
}

// writeAreaContentsResponse turns a completed database read into the public HTTP
// response. Keeping encoding here lets tests verify JSON and safe errors without
// replacing the real loadAreaContents function with a callback.
func writeAreaContentsResponse(w http.ResponseWriter, contents []AreaContent, err error) {
	if err != nil {
		// Driver errors can mention internal details such as the database host,
		// username, table, or query. Keep that diagnostic in server logs and send
		// only a stable public message to the browser.
		log.Printf("load area contents: %v", err)
		http.Error(w, "unable to load area content", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	encodeError := json.NewEncoder(w).Encode(contents)
	if encodeError != nil {
		// At this point the response may have started, so it is too late to replace
		// it with a clean error response. Logging still makes the failure visible.
		log.Printf("encode area contents: %v", encodeError)
	}
}

const (
	// Manual requests use 207 so the browser's fetch remains successful and can
	// read the safe per-area results. The Review screen then explains which of
	// Home, Health, or Meals failed without receiving private provider diagnostics.
	manualAreaUpdateFailureHTTPStatus = http.StatusMultiStatus

	// Scheduled requests use a non-2xx status because Cloud Scheduler considers
	// every 2xx response delivered successfully. Returning 500 asks Scheduler to
	// apply its configured retry policy. On a retry, a document that already
	// succeeded is skipped by the Sunday-week due check, while a failed document
	// remains due and gets another attempt.
	scheduledAreaUpdateFailureHTTPStatus = http.StatusInternalServerError
)

// writeAreaUpdateResultsResponse turns completed manual or scheduled update
// results into a safe response. trigger identifies which endpoint ran so server
// logs can distinguish a browser click from a Sunday Cloud Scheduler request.
// failureHTTPStatus controls whether a per-area failure remains readable by the
// browser as HTTP 207 or asks Cloud Scheduler to retry through HTTP 500.
func writeAreaUpdateResultsResponse(
	w http.ResponseWriter,
	trigger string,
	results []AreaUpdateResult,
	failureHTTPStatus int,
	err error,
) {
	if err != nil {
		log.Printf("%s area update: %v", trigger, err)
		http.Error(w, "unable to update weekly area content", http.StatusInternalServerError)
		return
	}

	logAreaUpdateResults(trigger, results)

	// A result error remains excluded by AreaUpdateResult's json:"-" tag. The
	// browser receives only stable statuses and can report which area failed
	// without learning database hosts, SQL details, or provider diagnostics.
	responseStatus := http.StatusOK
	for _, result := range results {
		if result.Status == areaUpdateStatusFailed {
			responseStatus = failureHTTPStatus
			break
		}
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(responseStatus)
	encodeError := json.NewEncoder(w).Encode(results)
	if encodeError != nil {
		log.Printf("encode %s area update results: %v", trigger, encodeError)
	}
}
