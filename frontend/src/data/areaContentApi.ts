/**
 * The frontend boundary for area content loaded from the Go API.
 *
 * Supabase stores independently shaped JSON documents: Home and Health contain
 * ordinary review entries, while Meals contains weekly meal recommendations.
 * This module translates both database shapes into the one `ReviewArea` model
 * rendered by App.tsx. Components therefore do not need to know table names,
 * content types, or meal-specific database fields.
 *
 * The complete loading sequence is:
 *
 * 1. App.tsx calls `fetchReviewAreas`, normally once when the Review screen
 *    mounts. That function sends `GET /api/areas` to the Go server and asks for
 *    JSON. During local development, Vite will forward this relative `/api`
 *    request to Go; in Cloud Run, the frontend and Go API will share an origin.
 * 2. Go reads the rows from `personal_ai.area_content` and returns an array.
 *    Each row identifies a document with `area_id` and `content_type`, and its
 *    `content` property contains the JSON stored in Supabase.
 * 3. `fetchReviewAreas` parses the HTTP response and passes the unknown JSON to
 *    `adaptAreaContents`. Keeping the value unknown until this point prevents
 *    an unverified server response from being treated as trusted UI data.
 * 4. `adaptAreaContents` calls `readAreaContentDocument` for every returned
 *    row. That function checks the row object and converts Go's snake_case
 *    `area_id` and `content_type` fields into the internal camelCase names used
 *    below. Malformed rows stop the load with a descriptive error.
 * 5. `adaptAreaContents` walks `areaDefinitions` in display order and finds the
 *    matching area/content-type pair. Rows without a definition are ignored,
 *    so adding a backend document alone cannot create a broken navigation item.
 * 6. Home and Health go through `readEntryDocument`, which extracts their
 *    `entries` arrays. Each item then passes through `readReviewEntry` and
 *    `readMetadata` so ids, titles, states, optional details, links, and visible
 *    metadata have the exact shape expected by the Review screen.
 * 7. Meals follows its separate stored shape through `readMealRecommendations`.
 *    That function turns each recommendation into a `ReviewEntry`, creates a
 *    stable id from its weekday with `mealEntryId`, and presents its day and
 *    tags as searchable metadata.
 * 8. `adaptAreaContents` combines the translated entries with the frontend-owned
 *    name, description, accent, and order from `areaDefinitions`, then returns
 *    `ReviewArea[]` to `fetchReviewAreas`. The caller can store that final array
 *    in React state and render it without understanding any database schemas.
 *
 * A non-successful HTTP response, invalid top-level payload, malformed row, or
 * invalid content document throws an Error. App.tsx is responsible for catching
 * that Error and showing a useful loading failure instead of replacing the
 * current screen with partially translated data.
 */
import type {
  Accent,
  AreaId,
  EntryMetadata,
  EntryState,
  ReviewArea,
  ReviewEntry,
} from "../domain/review";

/** The three database-backed areas currently shown by the Review screen. */
export type RefreshableAreaId = "home" | "health" | "meals";

/** Areas whose individual entry state is stored by the current backend contract. */
export type PersistedEntryAreaId = "home" | "health";

/** Internal name for the same persisted-area union used by data adapters. */
type StoredAreaId = RefreshableAreaId;

/**
 * One saved generation prompt owned by the database.
 *
 * The API uses snake_case field names, but React callers use this camelCase
 * representation. The prompt is deliberately separate from area content:
 * editing instructions changes what a future refresh will generate and never
 * replaces the entries currently visible on the Review screen.
 */
export interface AreaContentPrompt {
  areaId: StoredAreaId;
  contentType: string;
  prompt: string;
}

/**
 * The confirmed state of one Home or Health entry after Go saves it.
 *
 * React applies the same state before this response arrives so the checkbox
 * feels immediate. Returning every identity field lets the helper reject a
 * successful-looking response for a different entry before React accepts it.
 */
export interface AreaEntryStateUpdate {
  areaId: PersistedEntryAreaId;
  contentType: string;
  entryId: string;
  state: EntryState;
  revision: number;
}

/** Display information stays in the frontend rather than being repeated in every database row. */
interface AreaDefinition {
  id: StoredAreaId;
  contentType: string;
  name: string;
  description: string;
  accent: Accent;
}

/** The part of an API row needed to choose and translate its JSON document. */
interface AreaContentDocument {
  areaId: string;
  contentType: string;
  content: unknown;
  updatedAt: string;
}

/**
 * The complete frontend snapshot returned by the areas endpoint.
 *
 * `areas` drives the visible Review screen. `updatedAtByArea` preserves the
 * timestamp belonging to each area's database document so every section can
 * report its own refresh time. Areas without a stored document have no entry.
 */
export interface ReviewAreaSnapshot {
  areas: ReviewArea[];
  updatedAtByArea: Partial<Record<AreaId, string>>;
}

/** The three per-section outcomes returned by Go after a content refresh request. */
export type WeeklyAreaUpdateStatus = "updated" | "skipped" | "failed";

/**
 * Compatibility name for callers that already refer to the bulk refresh flow.
 * Home now uses the same configured refresh route as Health and Meals, so this
 * union represents every visible database-backed target rather than only plans.
 */
export type WeeklyPlanAreaId = RefreshableAreaId;

/**
 * One Home, Health, or Meals result returned by a manual refresh endpoint.
 *
 * `revision` and `updatedAt` are optional because a failed update was never
 * saved. Keeping those fields optional makes the browser model match that
 * visible outcome instead of inventing placeholder values.
 */
export interface WeeklyAreaUpdateResult {
  areaId: string;
  contentType: string;
  status: WeeklyAreaUpdateStatus;
  revision?: number;
  updatedAt?: string;
}

/**
 * Connects each refreshable visible area to the exact `content_type` stored by Go.
 *
 * React callers only choose a recognizable area such as `"home"`. This map
 * owns the database-facing detail needed to build and verify the individual
 * update route, so App.tsx never has to repeat strings such as
 * `"weekly_workout_routine"`.
 */
const refreshableAreaContentTypes: Record<RefreshableAreaId, string> = {
  home: "maintenance_tasks",
  health: "weekly_workout_routine",
  meals: "weekly_meal_recommendations",
};

/**
 * Entry-level state writes exist only for document shapes with stored entries.
 * Meals recommendations are translated into review rows in the browser and do
 * not have backend entry ids, so they are deliberately absent from this map.
 */
const persistedEntryContentTypes: Record<PersistedEntryAreaId, string> = {
  home: refreshableAreaContentTypes.home,
  health: refreshableAreaContentTypes.health,
};

/**
 * The bulk endpoint is a fixed all-or-nothing response contract, unlike an
 * individual section route. Listing the expected pairs in one place lets the
 * client reject a response that omits Home, repeats Meals, or invents another
 * target before App.tsx can report that all three areas were refreshed.
 */
const bulkRefreshTargets = Object.entries(refreshableAreaContentTypes) as Array<
  [RefreshableAreaId, string]
>;

// Array order controls both the sidebar and main review queue. Content type is
// included in the lookup because one area may eventually store multiple
// documents, such as a workout routine and a separate health checklist.
const areaDefinitions: AreaDefinition[] = [
  {
    id: "home",
    contentType: "maintenance_tasks",
    name: "Home",
    description: "Maintenance and records that keep the house running quietly.",
    accent: "juniper",
  },
  {
    id: "health",
    contentType: "weekly_workout_routine",
    name: "Health",
    description: "The next physical routines, kept specific and easy to start.",
    accent: "iris",
  },
  {
    id: "meals",
    contentType: "weekly_meal_recommendations",
    name: "Meals",
    description: "Meal recommendations for the week, ready when it is time to plan or shop.",
    accent: "saffron",
  },
];

/** Returns true for JSON objects while excluding arrays and null. */
function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

/** Reads a required, non-empty string and names the malformed field in errors. */
function readRequiredString(value: unknown, field: string): string {
  if (typeof value !== "string" || value.trim() === "") {
    throw new Error(`Area content field ${field} must be a non-empty string.`);
  }
  return value;
}

/** Reads an optional string without accepting other JSON value types. */
function readOptionalString(value: unknown, field: string): string | undefined {
  if (value === undefined || value === null || value === "") return undefined;
  if (typeof value !== "string") {
    throw new Error(`Area content field ${field} must be a string when provided.`);
  }
  return value;
}

/**
 * Converts the metadata beneath an entry title.
 *
 * Metadata is optional at the storage boundary because a simple task may have
 * no secondary details. The UI always receives an array, which lets App.tsx
 * render entries without null checks.
 */
function readMetadata(value: unknown, entryLabel: string): EntryMetadata[] {
  if (value === undefined || value === null) return [];
  if (!Array.isArray(value)) {
    throw new Error(`${entryLabel} metadata must be an array.`);
  }

  return value.map((item, index) => {
    if (!isRecord(item)) {
      throw new Error(`${entryLabel} metadata item ${index + 1} must be an object.`);
    }

    const metadata: EntryMetadata = {
      label: readRequiredString(item.label, `${entryLabel}.metadata[${index}].label`),
      value: readRequiredString(item.value, `${entryLabel}.metadata[${index}].value`),
    };
    if (typeof item.attention === "boolean") {
      metadata.attention = item.attention;
    }
    return metadata;
  });
}

/** Converts one stored Home or Health entry into the UI model. */
function readReviewEntry(value: unknown, areaId: StoredAreaId, index: number): ReviewEntry {
  const entryLabel = `${areaId} entry ${index + 1}`;
  if (!isRecord(value)) {
    throw new Error(`${entryLabel} must be an object.`);
  }

  const state = readRequiredString(value.state, `${entryLabel}.state`);
  if (state !== "open" && state !== "done") {
    throw new Error(`${entryLabel} state must be open or done.`);
  }

  return {
    id: readRequiredString(value.id, `${entryLabel}.id`),
    title: readRequiredString(value.title, `${entryLabel}.title`),
    details: readOptionalString(value.details, `${entryLabel}.details`),
    href: readOptionalString(value.href, `${entryLabel}.href`),
    state: state as EntryState,
    metadata: readMetadata(value.metadata, entryLabel),
  };
}

/** Extracts the common `entries` array used by Home and Health. */
function readEntryDocument(content: unknown, areaId: StoredAreaId): ReviewEntry[] {
  if (!isRecord(content) || !Array.isArray(content.entries)) {
    throw new Error(`${areaId} content must contain an entries array.`);
  }
  return content.entries.map((entry, index) => readReviewEntry(entry, areaId, index));
}

/** Creates a stable React key from a meal's weekday, such as `meal-monday`. */
function mealEntryId(day: string): string {
  const daySlug = day
    .trim()
    .toLocaleLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-|-$/g, "");
  return `meal-${daySlug}`;
}

/**
 * Translates the weekly Meals document into ordinary review rows.
 *
 * A recommendation is informational rather than a persisted task, but the
 * current Review interface requires an open/done state. It starts as open and
 * can still be marked done locally for the current browser session.
 */
function readMealRecommendations(content: unknown): ReviewEntry[] {
  if (!isRecord(content) || !Array.isArray(content.recommendations)) {
    throw new Error("meals content must contain a recommendations array.");
  }

  return content.recommendations.map((value, index) => {
    const recommendationLabel = `meal recommendation ${index + 1}`;
    if (!isRecord(value)) {
      throw new Error(`${recommendationLabel} must be an object.`);
    }

    const day = readRequiredString(value.day, `${recommendationLabel}.day`);
    const metadata: EntryMetadata[] = [{ label: "Day", value: day }];

    // Tags are stored as a list because the LLM can add or remove categories.
    // The Review row presents them as one concise, searchable metadata value.
    if (value.tags !== undefined) {
      if (!Array.isArray(value.tags) || !value.tags.every((tag) => typeof tag === "string")) {
        throw new Error(`${recommendationLabel} tags must be an array of strings.`);
      }
      if (value.tags.length > 0) {
        metadata.push({ label: "Tags", value: value.tags.join(", ") });
      }
    }

    return {
      id: mealEntryId(day),
      title: readRequiredString(value.title, `${recommendationLabel}.title`),
      details: readOptionalString(value.description, `${recommendationLabel}.description`),
      state: "open",
      metadata,
    };
  });
}

/** Converts the snake_case JSON fields returned by Go into a small internal row type. */
function readAreaContentDocument(value: unknown, index: number): AreaContentDocument {
  if (!isRecord(value)) {
    throw new Error(`Area content row ${index + 1} must be an object.`);
  }

  const updatedAt = readRequiredString(value.updated_at, `row ${index + 1}.updated_at`);
  if (Number.isNaN(Date.parse(updatedAt))) {
    throw new Error(`row ${index + 1}.updated_at must be a valid timestamp.`);
  }

  return {
    areaId: readRequiredString(value.area_id, `row ${index + 1}.area_id`),
    contentType: readRequiredString(value.content_type, `row ${index + 1}.content_type`),
    content: value.content,
    updatedAt,
  };
}

/**
 * Validates one result from either manual-update endpoint and converts Go's
 * snake_case field names to the camelCase names used by the React application.
 * Provider and database errors are deliberately absent because the Go endpoint
 * keeps those private and returns only the safe `failed` status.
 */
function readWeeklyAreaUpdateResult(value: unknown, index: number): WeeklyAreaUpdateResult {
  const resultLabel = `Weekly area update result ${index + 1}`;
  if (!isRecord(value)) {
    throw new Error(`${resultLabel} must be an object.`);
  }

  const status = readRequiredString(value.status, `${resultLabel}.status`);
  if (status !== "updated" && status !== "skipped" && status !== "failed") {
    throw new Error(`${resultLabel} status must be updated, skipped, or failed.`);
  }

  const result: WeeklyAreaUpdateResult = {
    areaId: readRequiredString(value.area_id, `${resultLabel}.area_id`),
    contentType: readRequiredString(value.content_type, `${resultLabel}.content_type`),
    status,
  };

  // Successful responses include the new database revision. A failed result
  // omits it, so only copy the value when Go actually returned a number.
  if (value.revision !== undefined) {
    if (typeof value.revision !== "number" || !Number.isInteger(value.revision)) {
      throw new Error(`${resultLabel}.revision must be an integer when provided.`);
    }
    result.revision = value.revision;
  }

  // Keep the timestamp as an ISO string. The button only needs to report that
  // the refresh finished; future UI can format this value for the local timezone.
  if (value.updated_at !== undefined) {
    result.updatedAt = readRequiredString(value.updated_at, `${resultLabel}.updated_at`);
  }

  return result;
}

/**
 * Validates the result array shared by the combined and individual update
 * endpoints. Keeping this check in one function means both button types reject
 * malformed server responses in exactly the same way.
 */
function readWeeklyAreaUpdateResults(payload: unknown): WeeklyAreaUpdateResult[] {
  if (!Array.isArray(payload)) {
    throw new Error("The weekly update API response must be an array.");
  }

  return payload.map(readWeeklyAreaUpdateResult);
}

/**
 * Verifies the full result set returned only by the bulk refresh endpoint.
 *
 * Each configured Home, Health, and Meals pair must appear exactly once. The
 * status may be updated, skipped, or failed, but a structurally incomplete or
 * duplicate response is unsafe because the page-level message could otherwise
 * claim a full refresh while one visible area was never considered by Go.
 */
function readBulkRefreshResults(payload: unknown): WeeklyAreaUpdateResult[] {
  const results = readWeeklyAreaUpdateResults(payload);
  if (results.length !== bulkRefreshTargets.length) {
    throw new Error("The bulk update API returned an unexpected result set.");
  }

  const expectedPairs = new Set(
    bulkRefreshTargets.map(([areaId, contentType]) => `${areaId}:${contentType}`),
  );
  const receivedPairs = new Set<string>();

  for (const result of results) {
    const pair = `${result.areaId}:${result.contentType}`;
    if (!expectedPairs.has(pair) || receivedPairs.has(pair)) {
      throw new Error("The bulk update API returned an unexpected result set.");
    }
    receivedPairs.add(pair);
  }

  // The length and duplicate checks above make this defensive final comparison
  // explicit: every expected pair is present once, not merely a same-length set.
  if (receivedPairs.size !== expectedPairs.size) {
    throw new Error("The bulk update API returned an unexpected result set.");
  }

  return results;
}

/**
 * Validates the compact prompt object returned by the prompt GET and PUT routes.
 *
 * Prompt text may contain leading or trailing whitespace that a person entered
 * intentionally, so this reader requires a string but does not trim the saved
 * value. Blank values are rejected before a PUT is sent instead.
 */
function readAreaContentPrompt(
  payload: unknown,
  expectedAreaId: StoredAreaId,
  expectedContentType: string,
): AreaContentPrompt {
  if (!isRecord(payload)) {
    throw new Error("The area prompt API response must be an object.");
  }

  const areaId = readRequiredString(payload.area_id, "area prompt area_id");
  const contentType = readRequiredString(payload.content_type, "area prompt content_type");
  if (areaId !== expectedAreaId || contentType !== expectedContentType) {
    throw new Error(`The ${expectedAreaId} prompt API returned an unexpected prompt.`);
  }

  if (typeof payload.prompt !== "string") {
    throw new Error("Area prompt field prompt must be a string.");
  }

  return {
    areaId: expectedAreaId,
    contentType: expectedContentType,
    prompt: payload.prompt,
  };
}

/**
 * Validates the complete response from an entry-state PUT.
 *
 * Every returned identity must match the request. This prevents an optimistic
 * checkbox from remaining changed if a proxy, stale server, or malformed mock
 * returns a valid object that actually describes another stored entry.
 */
function readAreaEntryStateUpdate(
  payload: unknown,
  expectedAreaId: PersistedEntryAreaId,
  expectedContentType: string,
  expectedEntryId: string,
  expectedState: EntryState,
): AreaEntryStateUpdate {
  if (!isRecord(payload)) {
    throw new Error("The entry state API response must be an object.");
  }

  const areaId = readRequiredString(payload.area_id, "entry state area_id");
  const contentType = readRequiredString(payload.content_type, "entry state content_type");
  const entryId = readRequiredString(payload.entry_id, "entry state entry_id");
  const state = readRequiredString(payload.state, "entry state state");
  if (
    areaId !== expectedAreaId ||
    contentType !== expectedContentType ||
    entryId !== expectedEntryId ||
    state !== expectedState
  ) {
    throw new Error(`The ${expectedAreaId} entry state API returned an unexpected entry.`);
  }

  if (typeof payload.revision !== "number" || !Number.isInteger(payload.revision)) {
    throw new Error("Entry state field revision must be an integer.");
  }

  return {
    areaId: expectedAreaId,
    contentType: expectedContentType,
    entryId: expectedEntryId,
    state: expectedState,
    revision: payload.revision,
  };
}

/**
 * Builds a prompt route from a visible area rather than exposing database field
 * names to components. Encoding both dynamic segments keeps this safe if a
 * future stored identifier contains a character with URL meaning.
 */
function areaPromptUrl(areaId: StoredAreaId): string {
  const definition = areaDefinitions.find((candidate) => candidate.id === areaId);
  if (!definition) {
    // StoredAreaId and areaDefinitions are intentionally kept in the same file.
    // This guard makes a future type or definition change fail clearly instead
    // of sending an incomplete URL from an editor interaction.
    throw new Error(`No content type is configured for ${areaId}.`);
  }

  return `/api/areas/${encodeURIComponent(areaId)}/${encodeURIComponent(definition.contentType)}/prompt`;
}

/**
 * Loads one area's saved generation instructions for the prompt editor.
 *
 * A 404 is a normal blank-create state: the area has visible database content
 * but no enabled prompt yet. All other non-success statuses remain errors so
 * the dialog can distinguish a missing prompt from a connection or server
 * problem and avoid overwriting the person's entered text.
 */
export async function fetchAreaContentPrompt(
  areaId: StoredAreaId,
  signal?: AbortSignal,
): Promise<AreaContentPrompt | null> {
  const response = await fetch(areaPromptUrl(areaId), {
    headers: { Accept: "application/json" },
    signal,
  });

  if (response.status === 404) return null;
  if (!response.ok) {
    throw new Error(`Unable to load ${areaId} prompt (HTTP ${response.status}).`);
  }

  const definition = areaDefinitions.find((candidate) => candidate.id === areaId);
  if (!definition) throw new Error(`No content type is configured for ${areaId}.`);
  return readAreaContentPrompt(await response.json(), areaId, definition.contentType);
}

/**
 * Saves prompt instructions for a future manual or scheduled area refresh.
 *
 * This route only persists the prompt. It intentionally does not call an
 * update endpoint or reload review data, preserving the entries already shown
 * until a person explicitly chooses a Refresh action later.
 */
export async function saveAreaContentPrompt(
  areaId: StoredAreaId,
  prompt: string,
  signal?: AbortSignal,
): Promise<AreaContentPrompt> {
  if (prompt.trim() === "") {
    throw new Error("A prompt cannot be blank.");
  }

  const response = await fetch(areaPromptUrl(areaId), {
    method: "PUT",
    headers: {
      Accept: "application/json",
      "Content-Type": "application/json",
    },
    body: JSON.stringify({ prompt }),
    signal,
  });

  if (!response.ok) {
    throw new Error(`Unable to save ${areaId} prompt (HTTP ${response.status}).`);
  }

  const definition = areaDefinitions.find((candidate) => candidate.id === areaId);
  if (!definition) throw new Error(`No content type is configured for ${areaId}.`);
  return readAreaContentPrompt(await response.json(), areaId, definition.contentType);
}

/**
 * Persists an optimistic Home or Health checkbox change.
 *
 * The component supplies only product-facing ids and the desired state. This
 * helper owns the stored content type, URL encoding, request body, HTTP error,
 * and response validation required by the Go entry-state contract.
 */
export async function saveAreaEntryState(
  areaId: PersistedEntryAreaId,
  entryId: string,
  state: EntryState,
  signal?: AbortSignal,
): Promise<AreaEntryStateUpdate> {
  const contentType = persistedEntryContentTypes[areaId];
  const response = await fetch(
    `/api/areas/${encodeURIComponent(areaId)}/${encodeURIComponent(contentType)}/entries/${encodeURIComponent(entryId)}/state`,
    {
      method: "PUT",
      headers: {
        Accept: "application/json",
        "Content-Type": "application/json",
      },
      body: JSON.stringify({ state }),
      signal,
    },
  );

  if (!response.ok) {
    throw new Error(`Unable to save ${areaId} entry state (HTTP ${response.status}).`);
  }

  return readAreaEntryStateUpdate(
    await response.json(),
    areaId,
    contentType,
    entryId,
    state,
  );
}

/**
 * Adapts the complete `/api/areas` payload into the domain model used by React.
 * Unknown documents are ignored until a display definition is intentionally
 * added above; this prevents a future backend-only document from appearing as
 * a broken navigation item.
 */
export function adaptAreaContentSnapshot(payload: unknown): ReviewAreaSnapshot {
  if (!Array.isArray(payload)) {
    throw new Error("The areas API response must be an array.");
  }

  const documents = payload.map(readAreaContentDocument);
  const updatedAtByArea: ReviewAreaSnapshot["updatedAtByArea"] = {};

  const areas = areaDefinitions.flatMap((definition) => {
    const document = documents.find(
      (candidate) =>
        candidate.areaId === definition.id && candidate.contentType === definition.contentType,
    );
    if (!document) return [];

    // Store the timestamp under the same id used by the rendered section. For
    // example, the Health row's timestamp becomes `updatedAtByArea.health`;
    // Meals retains its own independent value even when both were refreshed by
    // one weekly-update request.
    updatedAtByArea[definition.id] = document.updatedAt;

    const entries =
      definition.id === "meals"
        ? readMealRecommendations(document.content)
        : readEntryDocument(document.content, definition.id);

    return [{
      id: definition.id,
      name: definition.name,
      description: definition.description,
      accent: definition.accent,
      entries,
    }];
  });

  return { areas, updatedAtByArea };
}

/**
 * Adapts only the visible areas for callers that do not need snapshot metadata.
 *
 * This compatibility helper keeps the existing domain-focused API intact while
 * the Review screen can opt into `adaptAreaContentSnapshot` when it also needs
 * to show when each source document was refreshed.
 */
export function adaptAreaContents(payload: unknown): ReviewArea[] {
  return adaptAreaContentSnapshot(payload).areas;
}

/**
 * Loads the current Review snapshot from the same origin as the frontend.
 *
 * The snapshot retains both translated areas and their individual source
 * timestamps, allowing each section's status copy to survive a browser reload.
 * Vite will proxy this relative `/api` request to Go during local development;
 * Cloud Run will later serve the frontend and API from one origin directly.
 */
export async function fetchReviewAreaSnapshot(
  signal?: AbortSignal,
): Promise<ReviewAreaSnapshot> {
  const response = await fetch("/api/areas", {
    headers: { Accept: "application/json" },
    signal,
  });

  if (!response.ok) {
    throw new Error(`Unable to load review areas (HTTP ${response.status}).`);
  }

  const payload: unknown = await response.json();
  return adaptAreaContentSnapshot(payload);
}

/**
 * Loads only the translated areas for existing callers that do not need the
 * newest update timestamp.
 */
export async function fetchReviewAreas(signal?: AbortSignal): Promise<ReviewArea[]> {
  const snapshot = await fetchReviewAreaSnapshot(signal);
  return snapshot.areas;
}

/**
 * Regenerates Home, Health, and Meals through the Go bulk-refresh endpoint.
 *
 * The request intentionally contains no prompt or existing content. Go loads
 * both from Supabase, calls OpenRouter, validates each replacement document,
 * and saves it. React receives only the outcome for each section.
 */
export async function requestWeeklyAreaUpdate(
  signal?: AbortSignal,
): Promise<WeeklyAreaUpdateResult[]> {
  const response = await fetch("/api/areas/weekly-update", {
    method: "POST",
    headers: { Accept: "application/json" },
    signal,
  });

  if (!response.ok) {
    throw new Error(`Unable to update weekly plans (HTTP ${response.status}).`);
  }

  const payload: unknown = await response.json();
  return readBulkRefreshResults(payload);
}

/**
 * Regenerates one configured area after its section Refresh button is clicked.
 *
 * For example, `"home"` becomes
 * `POST /api/areas/home/maintenance_tasks/update`; `"health"` becomes
 * `POST /api/areas/health/weekly_workout_routine/update`. The Go server then
 * loads only that document and prompt, waits for OpenRouter, validates and
 * saves the replacement, and finally returns a one-item result array.
 */
export async function requestAreaContentUpdate(
  areaId: RefreshableAreaId,
  signal?: AbortSignal,
): Promise<WeeklyAreaUpdateResult> {
  // Look up the backend content type here rather than asking the component to
  // know how database documents are named.
  const contentType = refreshableAreaContentTypes[areaId];
  const response = await fetch(
    `/api/areas/${encodeURIComponent(areaId)}/${encodeURIComponent(contentType)}/update`,
    {
      method: "POST",
      headers: { Accept: "application/json" },
      signal,
    },
  );

  if (!response.ok) {
    throw new Error(`Unable to update ${areaId} plan (HTTP ${response.status}).`);
  }

  const results = readWeeklyAreaUpdateResults(await response.json());
  const result = results[0];

  // An individual route should report exactly the target it was asked to
  // update. Rejecting anything else avoids showing a Health success message
  // for a response that actually described Meals or multiple documents.
  if (
    results.length !== 1 ||
    !result ||
    result.areaId !== areaId ||
    result.contentType !== contentType
  ) {
    throw new Error(`The ${areaId} update API returned an unexpected result.`);
  }

  return result;
}
