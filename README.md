# Personal AI

A private review console for gathering the small decisions and recurring work
spread across everyday life. The current Review screen turns independently
stored Home, Health, and Meals documents into one responsive, accessible queue.
Reading is a retired legacy area: stored Reading rows are ignored and are not
part of the visible product.

The application now runs as one connected system:

```text
React + Vite  --->  Go JSON API  --->  Supabase Postgres
                         |
                         `--------->  OpenRouter (plan refreshes only)
```

The browser loads saved area content from Go. Go reads and writes JSON documents
in Supabase. Home, Health, and Meals can each be regenerated through OpenRouter
manually from the Review screen or automatically through the weekly scheduler.

## What works today

- Load the visible database-backed Home, Health, and Meals areas.
- Review every area together or focus on one area.
- Filter entries by Open, All, or Done and search their visible content.
- Persist Home and Health entry state changes through the revision-safe API;
  Meals entry state remains browser-session-only.
- Regenerate Home, Health, Meals, or all three from the interface.
- Show each area's stored update time and report full or partial refresh results.
- Retry failed initial loads without refreshing the page.
- Use a responsive desktop sidebar or keyboard-contained mobile drawer.
- Preserve keyboard focus and announce important state changes to screen readers.
- Reject malformed API data before it reaches the React domain model.
- Validate generated JSON and prevent overlapping refreshes from overwriting
  newer database revisions.

## Architecture

```text
.
|-- Dockerfile                      Cloud Run production image
|-- frontend/
|   |-- src/App.tsx                 Review screen and browser state
|   |-- src/data/areaContentApi.ts  Go API client and data translation
|   |-- src/domain/review.ts        Stable frontend domain types
|   `-- src/fixtures/               Legacy design fixtures
`-- backend/
    |-- main.go                     HTTP server and route registration
    |-- storage.go                  Supabase Postgres access
    |-- updater.go                  Refresh orchestration, locking, and persistence
    |-- area_content_validation.go  Per-content local semantic validation
    |-- area_entry_state_http.go    Home/Health entry-state PUT route
    |-- area_prompt_http.go         Area-update prompt HTTP routes
    |-- response_formats.go         Per-content OpenRouter response schemas
    `-- chat.go                     OpenRouter request boundary
```

The frontend's product and visual rationale lives in
[`frontend/DESIGN_DIRECTION.md`](frontend/DESIGN_DIRECTION.md).

### Production container

The root `Dockerfile` builds the application in three stages:

1. Node and pnpm create the production React bundle.
2. Go compiles the API as a static Linux binary with timezone data.
3. A small non-root runtime image receives only the binary and frontend bundle.

In the final image, Go serves both the React files and `/api` from one Cloud Run
origin. `FRONTEND_DIST_DIR` points Go to the copied bundle. Local development
leaves this variable unset because Vite serves the interface separately.

### Stored area documents

The frontend currently recognizes these rows from
`personal_ai.area_content`:

| Area | `content_type` | Stored collection | Model refresh |
| --- | --- | --- | --- |
| Home | `maintenance_tasks` | `entries` | Manual or scheduled |
| Health | `weekly_workout_routine` | `entries` | Manual or scheduled |
| Meals | `weekly_meal_recommendations` | `recommendations` | Manual or scheduled |

Display names, descriptions, colors, and ordering remain frontend-owned.
Reading is intentionally absent from the table because it is retired; matching
legacy rows are ignored. Other unknown database documents are ignored until a
matching frontend definition is added. Update instructions for the three
refreshable documents come from
`personal_ai.area_update_prompt`.

## Local development

### Prerequisites

- Node.js 22.13 or newer
- pnpm 11.9
- Go 1.26.4 or newer
- A reachable Supabase Postgres database with the two `personal_ai` tables and
  area rows described above
- An OpenRouter API key if you want to use Home, Health, or Meals refreshes

Database migrations and seed scripts are not currently included in this
repository, so a fresh clone still needs access to an already configured
database.

### Configure the backend

Create `backend/.env.local`:

```dotenv
DATABASE_URL=postgresql://your-supabase-session-pooler-connection
OPENROUTER_API_KEY=your-openrouter-key
```

`DATABASE_URL` is required when the server starts. `OPENROUTER_API_KEY` is only
read when a Home, Health, or Meals update endpoint calls OpenRouter. The backend
loads this file for local development; deployed environments can provide the
same variables directly.

The server uses port `8081` locally. Set `PORT` in the environment to override
it.

### Start the application

In the first terminal, start the Go API from the repository root:

```powershell
cd backend
go run .
```

In a second terminal, install and start the frontend:

```powershell
cd frontend
pnpm install
pnpm dev
```

Open the URL printed by Vite, typically `http://localhost:5173`. Vite proxies
relative `/api` requests to the Go server at `http://localhost:8081`, so both
processes need to be running for the Review screen to load.

## API

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/api/areas` | Return every stored area document |
| `PUT` | `/api/areas/{areaID}/{contentType}/entries/{entryID}/state` | Persist one Home or Health entry's `open`/`done` state with revision safety |
| `GET` | `/api/areas/{areaID}/{contentType}/prompt` | Return one enabled refresh prompt |
| `PUT` | `/api/areas/{areaID}/{contentType}/prompt` | Create or replace a refresh prompt without regenerating content |
| `POST` | `/api/areas/weekly-update` | Force-refresh Home, Health, and Meals |
| `POST` | `/api/areas/{areaID}/{contentType}/update` | Force-refresh one configured weekly document |
| `POST` | `/api/areas/scheduled-weekly-update` | Refresh weekly documents only when due |

The scheduled route is designed for Google Cloud Scheduler. Weekly plans use a
Sunday-through-Saturday week in `America/New_York`; repeat scheduler deliveries
skip documents already refreshed in that week. Database advisory locks and
revision checks also prevent overlapping browser, scheduler, or Cloud Run
requests from saving competing updates. If any scheduled document fails, the
route returns a non-successful HTTP status so Scheduler can retry. Manual
requests instead return per-area results that the Review screen can display.
Home refreshes preserve every unfinished task exactly and replace only completed
tasks, while Health and Meals generate a complete Sunday-through-Saturday plan.
The runner passes its requested local date and Sunday week start into validation:
Health `last_updated` must exactly equal that requested date, and Meals
`week_starting` must exactly equal that requested week's Sunday.

Home and Health entry-state changes use the PUT route above. Storage loads the
current document revision and saves only when that revision still matches, so a
concurrent refresh cannot be overwritten by an older browser action. Meals
recommendations have no persisted entry ids and remain session-only in the
browser.

Prompt GET and PUT requests can be canceled when the editor closes, a newer
request starts, or the page unmounts. The Edit Prompt control is disabled during
any Home, Health, or Meals refresh so a sequential bulk refresh cannot observe a
prompt that changed halfway through the run. A `skipped` update result means no
new revision was saved: the interface keeps the honest skipped message and
performs a bounded, delayed snapshot reconciliation to discover another request's
completed save without claiming that the current request succeeded.

## Verification

Run frontend checks from `frontend/`:

```powershell
pnpm test
pnpm build
```

Run backend tests from `backend/`:

```powershell
go test ./...
```

The test suites use mocks and local values; they do not require a live Supabase
connection or make OpenRouter requests.

## Deploy with the Google Cloud Console

The application uses two Cloud Run services built from the same GitHub
repository:

| Service | Caller | Access |
| --- | --- | --- |
| `personal-ai-web` | Your browser | Identity-Aware Proxy |
| `personal-ai-weekly` | Cloud Scheduler | IAM with a dedicated service account |

Keeping Scheduler on its own service gives it a narrow machine identity without
exposing the personal interface publicly. Both services use the same Supabase
database and OpenRouter credentials.

### 1. Prepare the Google Cloud project

From **APIs & Services > Library**, enable:

- Cloud Run
- Cloud Build
- Artifact Registry
- Secret Manager
- Cloud Scheduler
- Identity-Aware Proxy

From **IAM & Admin > Service Accounts**, create:

- `personal-ai-runtime`, used by both running containers
- `personal-ai-scheduler`, used only to invoke the scheduled service

### 2. Store production secrets

From **Secret Manager**, create:

- `personal-ai-database-url`, containing the Supabase Session Pooler URL
- `personal-ai-openrouter-api-key`, containing the OpenRouter API key

On each secret, grant `personal-ai-runtime` the **Secret Manager Secret
Accessor** role. Keep the secret values in Secret Manager rather than adding
them to GitHub or ordinary Cloud Run environment variables.

### 3. Deploy the web service

From **Cloud Run**, choose **Connect repository** and configure:

- Repository branch: `main`
- Build provider: Cloud Build
- Build type: Dockerfile
- Dockerfile path: `/Dockerfile`
- Service name: `personal-ai-web`
- Authentication: Require authentication with IAP
- Runtime service account: `personal-ai-runtime`
- Request timeout: 30 minutes
- Maximum instances: 1
- Concurrency: 8

Reference the two Secret Manager values as environment variables:

| Environment variable | Secret |
| --- | --- |
| `DATABASE_URL` | `personal-ai-database-url` |
| `OPENROUTER_API_KEY` | `personal-ai-openrouter-api-key` |

Select a numbered secret version rather than `latest`. After deployment, open
the service's **Security** settings and grant your Google account the
**IAP-secured Web App User** role. A personal project without a Google
organization may require a one-time OAuth consent configuration in the Console.

### 4. Deploy the scheduled service

Connect the same repository a second time with:

- Service name: `personal-ai-weekly`
- Authentication: Require authentication with IAM
- Runtime service account: `personal-ai-runtime`
- The same two secret environment variables
- Request timeout: 30 minutes
- Maximum instances: 1
- Concurrency: 1

On the service's **Permissions** page, grant `personal-ai-scheduler` the
**Cloud Run Invoker** role. Do not grant unauthenticated access.

Connecting both services to the repository creates a deployment trigger for
each service, keeping both current when `main` changes.

### 5. Create the Scheduler job

From **Cloud Scheduler**, create an HTTP job:

- Name: `personal-ai-weekly-update`
- Recommended frequency: `0 6 * * *`
- Timezone: `America/New_York`
- Method: POST
- URL:
  `https://<personal-ai-weekly-url>/api/areas/scheduled-weekly-update`
- Authentication: OIDC
- Service account: `personal-ai-scheduler`
- Audience: the base `personal-ai-weekly` URL without the `/api/...` path
- Attempt deadline: 30 minutes
- Retry attempts: 2
- Minimum retry backoff: 60 seconds
- Maximum retry backoff: 5 minutes

The recommended job runs every morning at 6:00. The backend's due check still
updates each plan at most once during a Sunday-through-Saturday week; daily
delivery simply allows a later day to recover if Sunday failed. Use
`0 6 * * 0` instead if a strict Sunday-only attempt is preferred.

After creating the job, select **Force run** and inspect its execution status
and the `personal-ai-weekly` Cloud Run logs. Then open `personal-ai-web` and
confirm that IAP signs you in and the Review screen loads the existing Supabase
content.

## Data and secrets

Manual and scheduled refreshes send the selected document and its stored prompt
to OpenRouter. Home, Health, and Meals are non-ZDR targets, so do not store data
in those documents that you are not comfortable sharing with the configured
provider. Each content type has its own response schema: Home uses a maintenance
task document, Health uses a workout document, and Meals uses a recommendation
document. After decoding, the backend performs local semantic validation for
that content type before saving; there is no universal same-shape rule. The
validators still enforce the preservation rules that matter for each document,
including keeping unfinished Home tasks unchanged.

`.env.local` files are ignored by Git. Keep database credentials, API keys, and
other machine-specific secrets out of committed files.

## Current limitations

- Meals entry state changes are browser-session-only and reset on reload; Home
  and Health state changes are persisted by the entry-state endpoint.
- Reading is retired and ignored; Home, Health, and Meals are the model-driven
  update targets.
- Database migrations and seed data are intentionally deferred; deployment
  currently requires the existing configured Supabase database.
- Cloud Run and Scheduler resources are configured in the Google Cloud Console;
  infrastructure-as-code is not currently included.
- The Go handlers do not implement application-level authentication; a
  deployment must keep the web service behind IAP and the scheduled service
  behind IAM.
