# Where I Left Off

Updated: 2026-08-19

## Current stopping point

The pre-commit-fix work now describes the connected database-backed refresh
system and its current boundaries.

- Home, Health, and Meals are the visible database-backed areas and all three
  support manual or scheduled OpenRouter refreshes.
- Reading is retired; matching stored rows are ignored by the frontend.
- Home and Health entry state now persists through
  `PUT /api/areas/{areaID}/{contentType}/entries/{entryID}/state`. The database
  revision predicate makes a concurrent refresh or state change fail safely;
  Meals recommendations remain browser-session-only because their rendered ids
  are derived locally.
- Home is non-ZDR like Health and Meals. Home refreshes preserve unfinished
  tasks while replacing completed ones; Health and Meals generate their own
  complete weekly document shapes.
- The runner's requested local date/week is part of generated-content
  validation: Health `last_updated` must exactly match the requested date, and
  Meals `week_starting` must exactly match the requested Sunday week start.
- OpenRouter requests use a separate response schema for each content type.
  Local semantic validators check the decoded generated document before it is
  saved; generated documents do not have to share one universal shape.
- Backend responsibilities were split into response formats, area-content
  validation, and prompt HTTP routes while `updater.go` retains refresh
  orchestration.
- Health exercise prose still renders as a semantic bulleted list when its
  separators identify multiple exercises, while one-sentence recovery text
  remains a paragraph.
- Prompt GET/PUT requests can be canceled when the dialog closes or becomes
  stale, and Edit Prompt is disabled during every content refresh.
- A skipped update remains visibly skipped. The browser schedules bounded,
  delayed snapshot reconciliation to notice another request's save without
  claiming that the skipped request succeeded.

## Next step

Collect the final text from the three existing GPT-5.5 Pi review panes once the
Herdr approval service permits another read, then visually check the Health
section through Vite on desktop and mobile. Confirm each exercise has its own
bullet and that a simple single-sentence recovery day remains a paragraph.

No commit has been created for this work.

## Start the application

Backend terminal, from the repository root:

```bash
cd backend
go run .
```

Frontend terminal, from the repository root:

```bash
cd frontend
pnpm install
pnpm dev
```

Open the Vite URL, normally `http://localhost:5173`. The frontend proxies `/api`
to the Go server on `http://localhost:8081`.

The local secrets file belongs at `backend/.env.local`. It is a hidden file, so
plain `ls` will not show it; use `ls -la backend` from the repository root. Do
not print its contents in shared output. If WSL reports that the direct Supabase
database hostname's IPv6 address is unreachable, use the Supabase session-pooler
connection string for `DATABASE_URL`, as shown in `README.md`.

## Verification

- Pass: `GOCACHE=/tmp/personal-ai-v3-go-cache GOTMPDIR=/tmp go test ./...`
  from `backend/`.
- Pass: `GOCACHE=/tmp/personal-ai-v3-go-race-cache GOTMPDIR=/tmp go test -race
  ./...` from `backend/`.
- Pass: `GOCACHE=/tmp/personal-ai-v3-go-vet-cache GOTMPDIR=/tmp go vet ./...`
  from `backend/`.
- Pass: `pnpm build` from `frontend/`; Vite transformed 4,562 modules and
  completed the production build.
- Pass: `timeout 300s pnpm test` from `frontend/`; 2 test files and all 51
  tests passed.
- Pass: `git diff --check`.
- Pass: high-confidence secret-pattern checks for the tracked diff and the new
  project files. The local credentials filename is also confirmed ignored.
- Two supplemental read-only GPT-5.5 reviewers found no backend or
  frontend/cross-layer issues and both returned `Commit-ready: Yes`.

Three read-only GPT-5.5 Pi reviews were launched in the existing Herdr panes.
Their final pane text is still pending because Herdr's control-plane read was
rejected after the Codex approval service reported its usage limit. The panes
were left open; do not describe their final verdicts as collected yet.

## Safety note

An ignored local file named `Persona-ai-v3 creds.txt` exists at the repository
root. It was not opened. Do not inspect, stage, or commit it without explicit
approval.

The branch is `main`; the current HEAD remains `6de407c` (`Prepare Cloud Run
deployment`, 2026-07-30). The worktree contains other pre-existing user changes,
so review and stage files selectively.
