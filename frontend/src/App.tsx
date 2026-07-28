/**
 * The interactive Review screen backed by the Go areas API.
 *
 * This component loads translated `ReviewArea` objects through the frontend
 * data module, composes the responsive shell, derives the cross-area queue, and
 * applies completion updates in browser memory. Loading the saved JSON and
 * saving later LLM updates are separate responsibilities; this screen never
 * reads Supabase or understands database document shapes directly.
 */
import { useEffect, useMemo, useRef, useState } from "react";
import {
  ArrowSquareOut,
  ArrowsClockwise,
  Check,
  CheckCircle,
  EnvelopeSimple,
  ForkKnife,
  Heartbeat,
  HouseLine,
  ListChecks,
  MagnifyingGlass,
  NewspaperClipping,
  SidebarSimple,
  Wallet,
  X,
} from "@phosphor-icons/react";
import {
  fetchReviewAreaSnapshot,
  requestAreaContentUpdate,
  requestWeeklyAreaUpdate,
} from "./data/areaContentApi";
import type { WeeklyPlanAreaId } from "./data/areaContentApi";
import type { AreaId, EntryState, QueueFilter, ReviewArea } from "./domain/review";

/** The three visible phases of the initial `/api/areas` request. */
type AreaLoadState = "loading" | "ready" | "error";

/** The visible phases of a person-triggered Health and Meals regeneration. */
type WeeklyUpdateState = "idle" | "updating" | "success" | "partial" | "error";

/** The visible phases and explanatory copy for one section-level update. */
interface AreaUpdateFeedback {
  state: "updating" | "success" | "error";
  message: string;
}

/**
 * Supplies the user-facing name for each area with an individual update button.
 * Keeping this map exhaustive means a future weekly target must choose its
 * visible button and status wording when it is added to `WeeklyPlanAreaId`.
 */
const weeklyPlanAreaNames: Record<WeeklyPlanAreaId, string> = {
  health: "Health",
  meals: "Meals",
};

/**
 * Narrows a general Review area to one that the backend can regenerate today.
 *
 * TypeScript calls this a "type guard." After this function returns true,
 * App.tsx knows the value is specifically `"health"` or `"meals"` and permits
 * it to be passed to `requestAreaContentUpdate`.
 */
function isWeeklyPlanAreaId(areaId: AreaId): areaId is WeeklyPlanAreaId {
  return areaId === "health" || areaId === "meals";
}

/**
 * Formats one backend timestamp in the browser's local timezone.
 *
 * Including the date keeps an area last refreshed yesterday or last week from
 * looking like it changed today. The data adapter has already validated the
 * timestamp before this display helper receives it.
 */
function formatAreaUpdatedAt(updatedAt: string): string {
  return new Intl.DateTimeFormat("en-US", {
    month: "short",
    day: "numeric",
    year: "numeric",
    hour: "numeric",
    minute: "2-digit",
  }).format(new Date(updatedAt));
}

// Keep the label mapping exhaustive so adding a filter state produces a type
// error until its user-facing copy is also chosen.
const filterLabels: Record<QueueFilter, string> = {
  open: "Open",
  all: "All",
  // Completion timestamps do not exist in the domain model yet, so this label
  // promises only the state the application can actually determine.
  done: "Done",
};

/**
 * Chooses the icon shown beside each area name.
 *
 * This is a programmatic mapping, not a collection of brand logos. For example,
 * when `areaId` is `"mail"`, the switch returns an envelope icon. The same
 * helper is used in the sidebar and in the Mail section heading, so those two
 * places cannot accidentally use different icons.
 *
 * The icons are decorative because visible text such as "Mail" or "Home"
 * already tells the person which area they are viewing.
 */
function AreaGlyph({ areaId, size = 19 }: { areaId: AreaId; size?: number }) {
  // Sharing these props keeps every area icon visually and semantically
  // consistent while allowing the two layout contexts to choose a size.
  const props = { size, weight: "regular" as const, "aria-hidden": true };

  switch (areaId) {
    case "mail":
      return <EnvelopeSimple {...props} />;
    case "home":
      return <HouseLine {...props} />;
    case "health":
      return <Heartbeat {...props} />;
    case "reading":
      return <NewspaperClipping {...props} />;
    case "meals":
      return <ForkKnife {...props} />;
    case "money":
      return <Wallet {...props} />;
  }
}

export default function ReviewApp() {
  // The page begins without fixture content. Once fetchReviewAreaSnapshot
  // translates the Go response, storing its areas here causes the sidebar,
  // counts, and review sections to rerender from database-backed data together.
  const [areas, setAreas] = useState<ReviewArea[]>([]);

  // Each timestamp belongs to one stored area document. Keeping this metadata
  // beside, rather than inside, the ReviewArea domain model avoids pretending
  // that fixture-only areas have a persisted update time. Reloading after any
  // successful refresh replaces this map with the timestamps returned by Go.
  const [updatedAtByArea, setUpdatedAtByArea] = useState<
    Partial<Record<AreaId, string>>
  >({});

  // Load state distinguishes a legitimate empty database from a request that
  // is still running or failed. Incrementing `loadAttempt` after an error reruns
  // the effect below without coupling App.tsx to fetch implementation details.
  const [areaLoadState, setAreaLoadState] = useState<AreaLoadState>("loading");
  const [areaLoadError, setAreaLoadError] = useState("");
  const [loadAttempt, setLoadAttempt] = useState(0);

  // The weekly update has its own state because it is a longer POST request,
  // separate from loading the currently saved areas. While it is "updating,"
  // the button is disabled so a double-click cannot start overlapping model
  // requests for the same Health and Meals documents.
  const [weeklyUpdateState, setWeeklyUpdateState] = useState<WeeklyUpdateState>("idle");
  const [weeklyUpdateMessage, setWeeklyUpdateMessage] = useState("");

  // Health and Meals keep separate feedback so a person who clicks the Health
  // button sees the result beside Health rather than in the page-level weekly
  // update message. A missing key means that section has not been updated during
  // this browser session.
  const [areaUpdateFeedback, setAreaUpdateFeedback] = useState<
    Partial<Record<WeeklyPlanAreaId, AreaUpdateFeedback>>
  >({});

  // Only one model update may run at a time. This prevents an individual Health
  // request and the combined weekly request from loading the same revision and
  // racing to save different replacements.
  const individualUpdateRunning =
    areaUpdateFeedback.health?.state === "updating" ||
    areaUpdateFeedback.meals?.state === "updating";
  const updateInProgress = weeklyUpdateState === "updating" || individualUpdateRunning;

  // Area selection, text search, and the Open/All/Completed filter are stored
  // separately. A person can therefore combine them—for example, show only
  // completed Mail entries whose text contains "Google".
  const [filter, setFilter] = useState<QueueFilter>("open");
  const [query, setQuery] = useState("");
  const [activeArea, setActiveArea] = useState<AreaId | null>(null);

  // Mobile navigation is stateful; desktop visibility remains CSS-controlled.
  const [navOpen, setNavOpen] = useState(false);

  // These element references coordinate keyboard focus as the mobile drawer
  // opens and closes. The restoration flag prevents the initial page render
  // from moving focus to a menu button the person has not used.
  const menuButtonRef = useRef<HTMLButtonElement>(null);
  const closeNavButtonRef = useRef<HTMLButtonElement>(null);
  const restoreMenuFocusRef = useRef(false);

  // When the last visible entry is completed, React replaces the queue with an
  // empty state. This reference lets the post-render effect focus that state's
  // reset action instead of allowing focus to fall back to the document body.
  const emptyStateActionRef = useRef<HTMLButtonElement>(null);
  const focusEmptyStateAfterUpdateRef = useRef(false);

  // Completion changes are announced separately instead of making the entire
  // queue a live region, which would be excessively noisy for screen readers.
  const [announcement, setAnnouncement] = useState("");

  // Load every configured area when the screen mounts or the person retries a
  // failed request. AbortController cancels the browser request when React
  // unmounts this screen or starts a newer attempt, preventing an older response
  // from replacing newer state. React StrictMode intentionally runs effects an
  // extra time during development; canceling cleanup makes that behavior safe.
  useEffect(() => {
    const requestController = new AbortController();

    setAreaLoadState("loading");
    setAreaLoadError("");

    fetchReviewAreaSnapshot(requestController.signal)
      .then((snapshot) => {
        if (requestController.signal.aborted) return;

        // Store content and timestamps from the same response so a section
        // cannot show refreshed rows beside metadata from an older request.
        setAreas(snapshot.areas);
        setUpdatedAtByArea(snapshot.updatedAtByArea);
        setAreaLoadState("ready");
      })
      .catch((error: unknown) => {
        if (requestController.signal.aborted) return;

        const message = error instanceof Error ? error.message : "Unable to load review areas.";
        setAreaLoadError(message);
        setAreaLoadState("error");
      });

    return () => requestController.abort();
  }, [loadAttempt]);

  // Global counts are derived from the same state as the rows so header and
  // navigation totals cannot drift after an in-memory completion change.
  const openCount = useMemo(
    () => areas.reduce((total, area) => total + area.entries.filter((entry) => entry.state === "open").length, 0),
    [areas],
  );

  // The summary describes areas that currently contain open work, not every
  // configured area. Completing the last open Money entry therefore reduces
  // this count even though Money remains available in navigation.
  const openAreaCount = useMemo(
    () => areas.filter((area) => area.entries.some((entry) => entry.state === "open")).length,
    [areas],
  );

  const doneCount = useMemo(
    () => areas.reduce((total, area) => total + area.entries.filter((entry) => entry.state === "done").length, 0),
    [areas],
  );

  const activeAreaName = activeArea ? areas.find((area) => area.id === activeArea)?.name : undefined;

  // Normalize once before filtering every entry. Locale-aware casing is more
  // robust for future user-authored text than a simple ASCII-only comparison.
  const normalizedQuery = query.trim().toLocaleLowerCase();

  // Filtering happens in three steps: keep the selected area (or every area),
  // keep only entries matching the status and search text, then remove any area
  // group left with no visible entries. The complete `areas` state is retained,
  // so clearing the controls can show everything again.
  const visibleAreas = useMemo(
    () =>
      areas
        .filter((area) => !activeArea || area.id === activeArea)
        .map((area) => ({
          ...area,
          entries: area.entries.filter((entry) => {
            const matchesState = filter === "all" || entry.state === filter;

            // Search both primary prose and structured metadata so a person can
            // find an entry by sender, due date, category, or visible wording.
            const searchableText = [
              entry.title,
              entry.details ?? "",
              ...entry.metadata.flatMap((item) => [item.label, item.value]),
            ]
              .join(" ")
              .toLocaleLowerCase();
            return matchesState && (!normalizedQuery || searchableText.includes(normalizedQuery));
          }),
        }))
        .filter((area) => area.entries.length > 0),
    [activeArea, areas, filter, normalizedQuery],
  );

  // Opening the mobile drawer sends focus to its Close button. Escape and every
  // close path return focus to the menu trigger after the background becomes
  // interactive again. Crossing into the desktop breakpoint closes the modal
  // state without focusing the now-hidden mobile trigger.
  useEffect(() => {
    if (!navOpen) {
      if (restoreMenuFocusRef.current) {
        restoreMenuFocusRef.current = false;
        menuButtonRef.current?.focus();
      }
      return;
    }

    closeNavButtonRef.current?.focus();

    function handleDrawerKeyDown(event: KeyboardEvent) {
      if (event.key === "Tab") {
        const drawer = closeNavButtonRef.current?.closest(".sidebar");
        if (!drawer) return;

        // The drawer currently contains buttons only, but including links keeps
        // the focus loop correct if navigation later gains real destinations.
        const drawerControls = Array.from(
          drawer.querySelectorAll<HTMLElement>("button:not([disabled]), a[href]"),
        );
        const firstControl = drawerControls[0];
        const lastControl = drawerControls[drawerControls.length - 1];
        if (!firstControl || !lastControl) return;

        // Native `inert` removes the background from ordinary tab order. This
        // explicit boundary loop additionally prevents focus from falling to
        // browser chrome or escaping in environments with partial inert support.
        if (event.shiftKey && document.activeElement === firstControl) {
          event.preventDefault();
          lastControl.focus();
        } else if (!event.shiftKey && document.activeElement === lastControl) {
          event.preventDefault();
          firstControl.focus();
        } else if (!drawer.contains(document.activeElement)) {
          event.preventDefault();
          firstControl.focus();
        }
        return;
      }

      if (event.key === "Escape") {
        event.preventDefault();
        closeNavigation();
      }
    }

    function handleViewportResize() {
      if (window.innerWidth <= 900) return;

      restoreMenuFocusRef.current = false;
      setNavOpen(false);
    }

    document.addEventListener("keydown", handleDrawerKeyDown);
    window.addEventListener("resize", handleViewportResize);
    return () => {
      document.removeEventListener("keydown", handleDrawerKeyDown);
      window.removeEventListener("resize", handleViewportResize);
    };
  }, [navOpen]);

  // Focus the empty-state reset only after React has removed the final visible
  // row and mounted the replacement control.
  useEffect(() => {
    if (!focusEmptyStateAfterUpdateRef.current || visibleAreas.length > 0) return;

    focusEmptyStateAfterUpdateRef.current = false;
    emptyStateActionRef.current?.focus();
  }, [visibleAreas.length]);

  // Compute the human date once per page load; the screen is a review session,
  // so it does not need a timer that causes midnight-only rerenders.
  const dateLabel = useMemo(
    () =>
      new Intl.DateTimeFormat("en-US", {
        weekday: "long",
        month: "long",
        day: "numeric",
      }).format(new Date()),
    [],
  );

  /**
   * Opens the mobile navigation drawer.
   *
   * The compact header's menu button calls this function. The effect above
   * moves focus into the drawer after React exposes it.
   */
  function openNavigation() {
    restoreMenuFocusRef.current = false;
    setNavOpen(true);
  }

  /**
   * Closes the mobile drawer and schedules focus restoration.
   *
   * The Close button, scrim, Escape key, and mobile navigation choices all use
   * this path so keyboard position is restored consistently.
   */
  function closeNavigation() {
    if (!navOpen) return;

    restoreMenuFocusRef.current = true;
    setNavOpen(false);
  }

  /**
   * Retries the complete areas request after a visible loading error.
   *
   * The error panel's "Try again" button calls this function. Changing the
   * attempt number triggers the loading effect above, which clears the old
   * message and requests a fresh snapshot from Go.
   */
  function retryAreaLoad() {
    setLoadAttempt((currentAttempt) => currentAttempt + 1);
  }

  /**
   * Regenerates the saved Health and Meals plans after a button click.
   *
   * The POST request does not finish until Go has called OpenRouter, validated
   * the returned JSON, and attempted to save both documents. Successful or
   * partially successful runs increment `loadAttempt`, which reruns the GET
   * effect above and replaces the visible rows with the latest saved content.
   */
  async function updateWeeklyPlans() {
    // The disabled button normally prevents a second call. This guard also
    // protects the function if it is invoked programmatically while one request
    // is already in progress.
    if (updateInProgress) return;

    setWeeklyUpdateState("updating");
    setWeeklyUpdateMessage("Updating Health and Meals. This can take a minute.");

    try {
      const results = await requestWeeklyAreaUpdate();
      const failedResults = results.filter((result) => result.status === "failed");
      const savedResults = results.filter((result) => result.status !== "failed");

      // Reload when at least one document was saved or confirmed current. A
      // partial result should still reveal the section that updated correctly.
      if (savedResults.length > 0) {
        setLoadAttempt((currentAttempt) => currentAttempt + 1);
      }

      if (failedResults.length === 0 && results.length > 0) {
        setWeeklyUpdateState("success");
        setWeeklyUpdateMessage("Health and Meals updated. The latest weekly plans are now loaded.");
        return;
      }

      const failedAreaNames = failedResults.map((result) =>
        result.areaId.charAt(0).toLocaleUpperCase() + result.areaId.slice(1),
      );
      const failedAreaLabel = failedAreaNames.join(" and ") || "The weekly plans";

      if (savedResults.length > 0) {
        setWeeklyUpdateState("partial");
        setWeeklyUpdateMessage(
          `${failedAreaLabel} could not be updated. The other weekly plan is ready.`,
        );
        return;
      }

      setWeeklyUpdateState("error");
      setWeeklyUpdateMessage(`${failedAreaLabel} could not be updated. Try the update again.`);
    } catch {
      // Network and non-successful HTTP responses use stable interface copy.
      // Detailed provider or database failures remain in the Go server logs.
      setWeeklyUpdateState("error");
      setWeeklyUpdateMessage("The weekly plans could not be updated. Check the server and try again.");
    }
  }

  /**
   * Regenerates one plan when its section-level Refresh button is clicked.
   *
   * `areaId` identifies either the Health workout document or the Meals
   * recommendations document. The request remains pending while Go loads that
   * area's prompt, calls OpenRouter, validates the JSON, and saves the new
   * revision. A successful result reloads `/api/areas`, which replaces the
   * visible section entries with the newly saved plan.
   */
  async function updateAreaPlan(areaId: WeeklyPlanAreaId) {
    // Disabled controls normally prevent overlap. This guard also protects
    // programmatic calls made before React has rendered the disabled state.
    if (updateInProgress) return;

    const areaName = weeklyPlanAreaNames[areaId];
    setAreaUpdateFeedback((currentFeedback) => ({
      ...currentFeedback,
      [areaId]: {
        state: "updating",
        message: `Updating ${areaName}. This can take a minute.`,
      },
    }));

    try {
      const result = await requestAreaContentUpdate(areaId);

      // A per-area failure is a completed HTTP response, but Go did not save a
      // new revision. Leave the current entries on screen and explain the result
      // beside the button that started the request.
      if (result.status === "failed") {
        setAreaUpdateFeedback((currentFeedback) => ({
          ...currentFeedback,
          [areaId]: {
            state: "error",
            message: `${areaName} could not be updated. Try the update again.`,
          },
        }));
        return;
      }

      // Incrementing this counter reruns the existing areas GET effect. The
      // section then renders the database revision that Go just saved.
      setLoadAttempt((currentAttempt) => currentAttempt + 1);
      setAreaUpdateFeedback((currentFeedback) => ({
        ...currentFeedback,
        [areaId]: {
          state: "success",
          message: `${areaName} updated. The latest plan is now loaded.`,
        },
      }));
    } catch {
      // Detailed database and provider failures remain in the Go server logs;
      // the visible message stays useful without exposing internal information.
      setAreaUpdateFeedback((currentFeedback) => ({
        ...currentFeedback,
        [areaId]: {
          state: "error",
          message: `${areaName} could not be updated. Check the server and try again.`,
        },
      }));
    }
  }

  /**
   * Shows one selected area instead of the full cross-area queue.
   *
   * Sidebar buttons and cadence-rail dots call this function. For example,
   * choosing Mail stores `"mail"` in `activeArea`. The `visibleAreas`
   * calculation above then keeps Mail and removes Home, Health, and the other
   * areas from the main view. Passing `null` selects "Review queue" and shows
   * every area again.
   *
   * The second state change also closes the mobile sidebar after a selection.
   * On desktop the sidebar stays visible because CSS controls that layout.
   */
  function chooseArea(areaId: AreaId | null) {
    setActiveArea(areaId);
    closeNavigation();
  }

  /**
   * Moves focus before a filtered completion control disappears.
   *
   * Completing an Open row, or restoring a Done row, immediately removes that
   * row from the current filtered view. If the activated control owns keyboard
   * focus, move to the next visible completion control, then the previous one.
   * When no sibling remains, the empty-state effect focuses "Show all entries"
   * after React mounts it.
   */
  function preserveFocusBeforeRemoval(trigger: HTMLButtonElement) {
    if (document.activeElement !== trigger) return;

    const completionControls = Array.from(document.querySelectorAll<HTMLButtonElement>(".check-control"));
    const currentIndex = completionControls.indexOf(trigger);
    if (currentIndex < 0) return;

    const siblingControl = completionControls[currentIndex + 1] ?? completionControls[currentIndex - 1];
    if (siblingControl) {
      siblingControl.focus();
      return;
    }

    focusEmptyStateAfterUpdateRef.current = true;
  }

  /**
   * Marks one entry done, or restores a completed entry to open.
   *
   * The checkbox-style button in each entry row calls this function with the
   * area's id and the entry's id. For example, clicking the passkey entry sends
   * `"mail"` and `"security-alert"`. The function finds that exact entry,
   * changes only its `state`, and leaves every other area and entry unchanged.
   *
   * React then recalculates the open/completed counts and redraws the filtered
   * queue. When the Open filter is active, an entry that was marked done
   * disappears from that view. This is currently an in-memory change; a future
   * data-service module will save the same change to the real backend.
   */
  function toggleEntry(areaId: AreaId, entryId: string, trigger: HTMLButtonElement) {
    // Resolve the current entry before updating so both the next state and the
    // accessibility announcement describe the exact same transition.
    const entry = areas.find((area) => area.id === areaId)?.entries.find((candidate) => candidate.id === entryId);
    if (!entry) return;

    const nextState: EntryState = entry.state === "open" ? "done" : "open";

    // Open and Done filters remove an entry when its state changes. Move focus
    // while the current DOM still provides reliable next/previous siblings.
    if (filter !== "all") {
      preserveFocusBeforeRemoval(trigger);
    }

    setAreas((currentAreas) =>
      // Return the existing object for every area and entry that did not change.
      // Only the selected entry gets a new copy with its new state. This avoids
      // mutating the loaded API snapshot and helps React skip unnecessary updates.
      currentAreas.map((area) => {
        if (area.id !== areaId) return area;

        return {
          ...area,
          entries: area.entries.map((entry) => {
            if (entry.id !== entryId) return entry;
            return { ...entry, state: nextState };
          }),
        };
      }),
    );

    setAnnouncement(`${entry.title} marked ${nextState}.`);
  }

  return (
    <div className="app-frame">
      {/* This dark overlay behind the mobile sidebar is called `nav-scrim`.
          Clicking its large background area closes the menu; keyboard users
          have a separately labeled close button inside the sidebar. */}
      <div
        className={`nav-scrim ${navOpen ? "is-visible" : ""}`}
        onClick={closeNavigation}
        aria-hidden="true"
      />

      {/* Desktop navigation is persistent. On smaller screens, CSS moves this
          same sidebar off the left edge and shows it when the menu button is
          pressed. The darkened `nav-scrim` behind it can be clicked to close it. */}
      <aside
        id="primary-navigation"
        className={`sidebar ${navOpen ? "is-open" : ""}`}
        aria-label="Primary navigation"
        aria-modal={navOpen || undefined}
        role={navOpen ? "dialog" : undefined}
      >
        <div className="brand-row">
          {/* These three empty spans are drawing hooks for the small brand mark.
              CSS positions them as three dots on one vertical line and colors
              the middle dot saffron. They contain no text because the entire
              mark is decorative and `aria-hidden` keeps it from being read. */}
          <div className="cadence-mark" aria-hidden="true">
            <span />
            <span />
            <span />
          </div>
          <div className="brand-copy">
            <span>Personal systems</span>
            <strong>Review</strong>
          </div>
          <button
            ref={closeNavButtonRef}
            className="icon-button close-nav"
            type="button"
            onClick={closeNavigation}
            aria-label="Close menu"
          >
            <X size={20} aria-hidden="true" />
          </button>
        </div>

        <nav className="nav-stack" aria-label="Review areas">
          {/* Null means the aggregate queue; an AreaId means a focused view. */}
          <p className="nav-label">Workspace</p>
          <button
            className={`nav-item ${activeArea === null ? "is-active" : ""}`}
            type="button"
            onClick={() => chooseArea(null)}
          >
            <span className="nav-icon"><ListChecks size={19} aria-hidden="true" /></span>
            <span>Review queue</span>
            <span className="nav-count">{openCount}</span>
          </button>

          <p className="nav-label area-label">Areas</p>
          {areas.map((area) => {
            // Sidebar counts intentionally ignore the active content filter and
            // always answer the stable question: how much remains open here?
            const areaOpenCount = area.entries.filter((entry) => entry.state === "open").length;
            return (
              <button
                className={`nav-item ${activeArea === area.id ? "is-active" : ""}`}
                type="button"
                onClick={() => chooseArea(area.id)}
                key={area.id}
              >
                <span className={`nav-icon accent-${area.accent}`}><AreaGlyph areaId={area.id} /></span>
                <span>{area.name}</span>
                <span className="nav-count">{areaOpenCount}</span>
              </button>
            );
          })}
        </nav>

      </aside>

      {/* This compact header exists only below the desktop breakpoint and keeps
          the current open count visible without consuming review space. */}
      <header className="mobile-header" inert={navOpen ? true : undefined}>
        <button
          ref={menuButtonRef}
          className="icon-button"
          type="button"
          onClick={openNavigation}
          aria-label="Open menu"
          aria-expanded={navOpen}
          aria-controls="primary-navigation"
        >
          <SidebarSimple size={22} aria-hidden="true" />
        </button>
        <span>Personal review</span>
        <span className="mobile-open-count">{openCount} open</span>
      </header>

      <main className="main-content" inert={navOpen ? true : undefined}>
        <div className="main-inner">
          {/* The page header states the session's job and adapts its copy when a
              single area is selected from navigation or the cadence rail. */}
          <header className="page-header">
            <div>
              <p className="eyebrow">Daily review · {dateLabel}</p>
              <h1>{activeAreaName ? `${activeAreaName}, at a glance.` : "What needs your attention."}</h1>
              <p className="page-summary">
                {areaLoadState === "loading"
                  ? "Loading your current Home, Health, Reading, and Meals review."
                  : areaLoadState === "error"
                    ? "Your saved review data could not be loaded. You can retry below."
                    : activeAreaName
                      ? `A focused view of the open work in ${activeAreaName.toLocaleLowerCase()}.`
                      : `${openCount} entries are open across ${openAreaCount} ${openAreaCount === 1 ? "area" : "areas"}. Start at the top or choose an area.`}
              </p>
            </div>
            {/* This compact action column keeps the expensive weekly refresh
                separate from ordinary queue filters. Its message remains visible
                after completion so success or partial failure is not conveyed
                only through the temporary button label. */}
            <div className="page-actions">
              <button
                className="weekly-update-button"
                type="button"
                onClick={updateWeeklyPlans}
                disabled={updateInProgress}
                aria-busy={weeklyUpdateState === "updating"}
              >
                <ArrowsClockwise
                  className={weeklyUpdateState === "updating" ? "is-spinning" : ""}
                  size={18}
                  aria-hidden="true"
                />
                {weeklyUpdateState === "updating"
                  ? "Updating weekly plans…"
                  : "Refresh weekly plans"}
              </button>

              <div className="cleared-note" aria-label={`${doneCount} completed entries`}>
                <CheckCircle size={20} weight="duotone" aria-hidden="true" />
                <span><strong>{doneCount} cleared</strong><small>done</small></span>
              </div>

              {weeklyUpdateMessage && (
                <p
                  className={`weekly-update-message is-${weeklyUpdateState}`}
                  role={weeklyUpdateState === "error" ? "alert" : "status"}
                >
                  {weeklyUpdateMessage}
                </p>
              )}
            </div>
          </header>

          {/* Controls remain sticky while scrolling long queues. Button pressed
              states and a real search input preserve keyboard semantics. */}
          <section className="queue-tools" aria-label="Review controls">
            <div className="filter-group" aria-label="Filter entries">
              {(Object.keys(filterLabels) as QueueFilter[]).map((filterId) => (
                <button
                  className={filter === filterId ? "is-selected" : ""}
                  type="button"
                  aria-pressed={filter === filterId}
                  onClick={() => setFilter(filterId)}
                  key={filterId}
                >
                  {filterLabels[filterId]}
                </button>
              ))}
            </div>

            <label className="search-field">
              <MagnifyingGlass size={17} aria-hidden="true" />
              <span className="sr-only">Search entries</span>
              <input
                type="search"
                value={query}
                onChange={(event) => setQuery(event.target.value)}
                placeholder="Search entries"
              />
            </label>
          </section>

          {/* The queue is grouped by recognizable life areas rather than by an
              implementation-specific loop or file structure. Loading and error
              states occupy this same region so the page does not briefly claim
              that an empty queue was returned while the request is in flight. */}
          <div className="queue">
            {areaLoadState === "loading" ? (
              <section className="empty-state" role="status" aria-live="polite">
                <h2>Loading your review…</h2>
                <p>Getting the latest saved areas from the server.</p>
              </section>
            ) : areaLoadState === "error" ? (
              <section className="empty-state" role="alert">
                <h2>Unable to load your review.</h2>
                <p>{areaLoadError}</p>
                <button type="button" onClick={retryAreaLoad}>Try again</button>
              </section>
            ) : areas.length === 0 ? (
              <section className="empty-state">
                <h2>No review areas are available yet.</h2>
                <p>Add area content in Supabase, then reload this page.</p>
              </section>
            ) : visibleAreas.length > 0 ? (
              visibleAreas.map((area) => {
                // This count describes the currently visible copy in open mode;
                // other filters switch the label to a neutral "shown" count.
                const areaOpenCount = area.entries.filter((entry) => entry.state === "open").length;
                const areaUpdatedAt = updatedAtByArea[area.id];

                // Only Health and Meals currently have stored update prompts.
                // Narrowing here lets the rest of the section render normally
                // for every area while adding controls only to those two.
                const weeklyPlanAreaId = isWeeklyPlanAreaId(area.id) ? area.id : null;
                const updateFeedback = weeklyPlanAreaId
                  ? areaUpdateFeedback[weeklyPlanAreaId]
                  : undefined;
                return (
                  <section className={`area-section accent-${area.accent}`} aria-labelledby={`area-${area.id}`} key={area.id}>
                    {/* The rail stop is functional navigation, not decoration.
                        Hover/focus treatment reveals the same focus action. */}
                    <button
                      className="rail-stop"
                      type="button"
                      onClick={() => chooseArea(area.id)}
                      aria-label={`Show only ${area.name}`}
                    >
                      <span className="rail-core" />
                      <span className="rail-hint">Focus</span>
                    </button>

                    <div className="area-heading">
                      <span className="area-icon"><AreaGlyph areaId={area.id} size={20} /></span>
                      <div className="area-heading-copy">
                        <div className="area-title-line">
                          <h2 id={`area-${area.id}`}>{area.name}</h2>
                          <span className="area-count">
                            {filter === "open" ? `${areaOpenCount} open` : `${area.entries.length} shown`}
                          </span>
                        </div>
                        <p>{area.description}</p>
                        {areaUpdatedAt && (
                          // The machine-readable value preserves the exact
                          // backend instant while the text uses local time.
                          <p>
                            <time dateTime={areaUpdatedAt}>
                              Last updated at {formatAreaUpdatedAt(areaUpdatedAt)}
                            </time>
                          </p>
                        )}
                      </div>

                      {weeklyPlanAreaId && (
                        <div className="area-update-actions">
                          {/* This secondary action refreshes only the section
                              whose heading contains it. All refresh controls are
                              disabled during the request to avoid revision races. */}
                          <button
                            className="area-update-button"
                            type="button"
                            onClick={() => updateAreaPlan(weeklyPlanAreaId)}
                            disabled={updateInProgress}
                            aria-busy={updateFeedback?.state === "updating"}
                          >
                            <ArrowsClockwise
                              className={updateFeedback?.state === "updating" ? "is-spinning" : ""}
                              size={16}
                              aria-hidden="true"
                            />
                            {updateFeedback?.state === "updating"
                              ? `Updating ${area.name}…`
                              : `Refresh ${area.name}`}
                          </button>

                          {updateFeedback && (
                            <p
                              className={`area-update-message is-${updateFeedback.state}`}
                              role={updateFeedback.state === "error" ? "alert" : "status"}
                            >
                              {updateFeedback.message}
                            </p>
                          )}
                        </div>
                      )}
                    </div>

                    <div className="entry-list">
                      {area.entries.map((entry) => (
                        <article className={`entry-row ${entry.state === "done" ? "is-done" : ""}`} key={entry.id}>
                          {/* A button is used instead of a native checkbox because
                              this action will eventually save to the backend,
                              update the screen immediately, and report a saving
                              error if the backend rejects the change. */}
                          <button
                            className="check-control"
                            type="button"
                            onClick={(event) => toggleEntry(area.id, entry.id, event.currentTarget)}
                            aria-label={`${entry.state === "open" ? "Mark" : "Restore"} ${entry.title}`}
                            aria-pressed={entry.state === "done"}
                          >
                            <span className="check-dot">
                              {entry.state === "done" && <Check size={14} weight="bold" aria-hidden="true" />}
                            </span>
                          </button>

                          <div className="entry-copy">
                            <h3>{entry.title}</h3>
                            {entry.details && <p>{entry.details}</p>}
                            <div className="entry-metadata" aria-label="Entry details">
                              {entry.metadata.map((item) => (
                                // The attention class adds a dot, while the label
                                // and value continue to carry semantic meaning.
                                <span className={item.attention ? "needs-attention" : ""} key={`${item.label}-${item.value}`}>
                                  <b>{item.label}</b> {item.value}
                                </span>
                              ))}
                            </div>
                          </div>

                          {entry.href ? (
                            // Source links open separately so the review session
                            // remains intact when a person follows supporting context.
                            <a className="entry-link" href={entry.href} target="_blank" rel="noreferrer" aria-label={`Open ${entry.title}`}>
                              <ArrowSquareOut size={18} aria-hidden="true" />
                            </a>
                          ) : (
                            // Preserve the grid column when an entry has no link,
                            // keeping text alignment stable across sibling rows.
                            <span className="entry-link-spacer" />
                          )}
                        </article>
                      ))}
                    </div>
                  </section>
                );
              })
            ) : (
              // Empty copy distinguishes a search miss from an Open/Done
              // filter result and always offers a concrete way back to visible work.
              <section className="empty-state">
                <span className="empty-check"><Check size={22} weight="bold" aria-hidden="true" /></span>
                <h2>No entries match this view.</h2>
                <p>{query ? "Try a different search or clear the current filter." : "Show all entries to review completed work."}</p>
                <button
                  ref={emptyStateActionRef}
                  type="button"
                  onClick={() => { setFilter("all"); setQuery(""); }}
                >
                  Show all entries
                </button>
              </section>
            )}
          </div>

          {/* Announce only the concise completion result after a toggle. */}
          <p className="sr-only" aria-live="polite">{announcement}</p>
        </div>
      </main>

    </div>
  );
}
