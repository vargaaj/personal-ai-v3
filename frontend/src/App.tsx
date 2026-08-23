/**
 * The interactive Review screen backed by the Go areas API.
 *
 * This component loads translated `ReviewArea` objects through the frontend
 * data module, composes the responsive shell, derives the cross-area queue, and
 * applies completion updates, and persists the Home and Health checkbox states
 * through the data module. Loading saved JSON and generating later LLM updates
 * remain separate responsibilities; this screen never reads Supabase or
 * understands database document shapes directly.
 */
import { useEffect, useMemo, useRef, useState } from "react";
import type { FormEvent } from "react";
import {
  ArrowSquareOut,
  ArrowsClockwise,
  Check,
  CheckCircle,
  EnvelopeSimple,
  ForkKnife,
  Heart,
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
  addThoughtfulFavorite,
  fetchAreaContentPrompt,
  fetchReviewAreaSnapshot,
  removeThoughtfulFavorite,
  requestAreaContentUpdate,
  requestWeeklyAreaUpdate,
  saveAreaContentPrompt,
  saveAreaEntryState,
} from "./data/areaContentApi";
import type { PersistedEntryAreaId, RefreshableAreaId } from "./data/areaContentApi";
import type {
  AreaId,
  EntryState,
  QueueFilter,
  ReviewArea,
  ThoughtfulFavorite,
  ThoughtfulSuggestionsContent,
} from "./domain/review";

/** The three visible phases of the initial `/api/areas` request. */
type AreaLoadState = "loading" | "ready" | "error";

/** The visible phases of a person-triggered Home, Health, and Meals refresh. */
type WeeklyUpdateState = "idle" | "updating" | "success" | "partial" | "error";

/** The prompt dialog's request phases, kept separate from content refresh state. */
type PromptDialogState = "loading" | "editing" | "saving" | "error";

/** The visible phases and explanatory copy for one section-level update. */
interface AreaUpdateFeedback {
  state: "updating" | "success" | "skipped" | "error";
  message: string;
}

/**
 * Supplies the user-facing name for every visible database-backed area.
 * Keeping this map exhaustive means a future prompt or refresh target must
 * choose its visible button and status wording before it reaches this screen.
 */
const refreshableAreaNames: Record<RefreshableAreaId, string> = {
  home: "Home",
  health: "Health",
  meals: "Meals",
};

/**
 * A skipped update may mean another request is still saving the document.
 * These three waits provide a bounded window for that save to appear without
 * polling forever or making the Review screen look continuously busy.
 */
const SNAPSHOT_RECONCILIATION_DELAYS_MS = [1_500, 3_000, 6_000] as const;

/**
 * Narrows a general Review area to one backed by the current prompt and refresh API.
 *
 * TypeScript calls this a "type guard." After this function returns true,
 * App.tsx knows the value is specifically `"home"`, `"health"`, or `"meals"` and permits
 * it to be passed to `requestAreaContentUpdate`.
 */
function isRefreshableAreaId(areaId: AreaId): areaId is RefreshableAreaId {
  return areaId === "home" || areaId === "health" || areaId === "meals";
}

/** Narrows a Review area to the Home and Health entry-state PUT contract. */
function isPersistedEntryAreaId(areaId: AreaId): areaId is PersistedEntryAreaId {
  return areaId === "home" || areaId === "health";
}

/** Creates one collision-safe identity for a checkbox save in progress. */
function entrySaveKey(areaId: AreaId, entryId: string): string {
  return `${areaId}\u0000${entryId}`;
}

/** Recognizes browser fetch cancellation without displaying it as a failure. */
function isAbortError(error: unknown): boolean {
  return error instanceof Error && error.name === "AbortError";
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

/**
 * Turns backend area ids into natural-language labels for refresh feedback.
 *
 * The bulk API validator guarantees Home, Health, and Meals, but this fallback
 * keeps an error message legible if a mocked or future caller supplies another
 * id. Joining the names here prevents success copy from silently mentioning an
 * area that was skipped or failed.
 */
function formatRefreshAreaNames(areaIds: string[]): string {
  const names = areaIds.map(
    (areaId) => refreshableAreaNames[areaId as RefreshableAreaId] ?? areaId,
  );
  if (names.length <= 1) return names[0] ?? "The selected area";
  if (names.length === 2) return `${names[0]} and ${names[1]}`;
  return `${names.slice(0, -1).join(", ")}, and ${names[names.length - 1]}`;
}

/**
 * Separates a Health entry's compact workout instructions into scannable items.
 *
 * Stored and generated workout details currently use middle dots, bullet glyphs,
 * or line breaks between exercises. Leading Markdown-style dashes and asterisks
 * are removed because the interface supplies the real HTML list marker. A single
 * uninterrupted sentence returns an empty array so the caller can preserve the
 * ordinary paragraph presentation used by recovery days and legacy content.
 *
 * Called while ReviewApp renders each Health `ReviewEntry` below.
 */
function readWorkoutDetailItems(details: string): string[] {
  const items = details
    .split(/\s*(?:·|•|\r?\n)\s*/)
    .map((item) => item.replace(/^[-*]\s+/, "").trim())
    .filter((item) => item.length > 0);

  return items.length > 1 ? items : [];
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
    case "thoughtful":
      return <Heart {...props} />;
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
  // Reconciliation timers run after the click handler that scheduled them has
  // returned. Keeping the newest timestamp map in a ref lets those delayed
  // checks compare against the latest loaded snapshot instead of an older
  // render's closed-over `updatedAtByArea` value.
  const updatedAtByAreaRef = useRef<Partial<Record<AreaId, string>>>({});

  // Load state distinguishes a legitimate empty database from a request that
  // is still running or failed. Incrementing `loadAttempt` after an error reruns
  // the effect below without coupling App.tsx to fetch implementation details.
  const [areaLoadState, setAreaLoadState] = useState<AreaLoadState>("loading");
  const [areaLoadError, setAreaLoadError] = useState("");
  const [loadAttempt, setLoadAttempt] = useState(0);

  // The weekly update has its own state because it is a longer POST request,
  // separate from loading the currently saved areas. While it is "updating,"
  // the button is disabled so a double-click cannot start overlapping model
  // requests for the same Home, Health, and Meals documents.
  const [weeklyUpdateState, setWeeklyUpdateState] = useState<WeeklyUpdateState>("idle");
  const [weeklyUpdateMessage, setWeeklyUpdateMessage] = useState("");

  // Home, Health, and Meals keep separate feedback so a person who clicks Home
  // sees the result beside Home rather than in the page-level bulk-refresh
  // message. A missing key means that section has not been refreshed during
  // this browser session.
  const [areaUpdateFeedback, setAreaUpdateFeedback] = useState<
    Partial<Record<RefreshableAreaId, AreaUpdateFeedback>>
  >({});

  // Only one model update may run at a time. This prevents an individual Home,
  // Health, or Meals request and the combined request from loading the same revision and
  // racing to save different replacements.
  const individualUpdateRunning =
    areaUpdateFeedback.home?.state === "updating" ||
    areaUpdateFeedback.health?.state === "updating" ||
    areaUpdateFeedback.meals?.state === "updating";
  const updateInProgress = weeklyUpdateState === "updating" || individualUpdateRunning;

  // Opening an editor stores both which section owns it and the exact text in
  // the textarea. Saving never changes `areas` or `loadAttempt`: prompt edits
  // affect only a later explicit refresh, so the current review stays stable.
  const [promptDialogArea, setPromptDialogArea] = useState<RefreshableAreaId | null>(null);
  const [promptDialogState, setPromptDialogState] = useState<PromptDialogState>("loading");
  const [promptText, setPromptText] = useState("");
  const [promptDialogError, setPromptDialogError] = useState("");

  // Home and Health checkboxes save optimistically. React state drives the
  // visible disabled controls, while the matching ref blocks a second event in
  // the brief interval before React has rendered that disabled state.
  const [savingEntryKeys, setSavingEntryKeys] = useState<Set<string>>(() => new Set());
  const savingEntryKeysRef = useRef<Set<string>>(new Set());

  // Thoughtful Favorites share one document revision, unlike Home and Health
  // entry states. A single page-wide lock therefore prevents an add on one
  // card and a remove on another from both sending the same stale revision.
  // The ref closes the short double-click window before React paints disabled
  // hearts; the state makes that locked condition visible and accessible.
  const [thoughtfulFavoriteMutationRunning, setThoughtfulFavoriteMutationRunning] = useState(false);
  const thoughtfulFavoriteMutationRunningRef = useRef(false);

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

  // The editor remembers the control that opened it, then returns keyboard
  // focus there when Cancel, Escape, or a successful Save closes the dialog.
  const promptDialogRef = useRef<HTMLDivElement>(null);
  const promptTextareaRef = useRef<HTMLTextAreaElement>(null);
  const promptDialogCloseRef = useRef<HTMLButtonElement>(null);
  const promptEditTriggerRef = useRef<HTMLButtonElement>(null);
  const restorePromptFocusRef = useRef(false);
  const promptRequestControllerRef = useRef<AbortController | null>(null);

  // The Favorites heading remains mounted when a Favorite card is removed.
  // Giving it a programmatic focus target lets keyboard focus land somewhere
  // stable instead of falling back to the document body after React removes
  // the activated card from the DOM.
  const thoughtfulFavoritesHeadingRef = useRef<HTMLHeadingElement>(null);

  // Skipped update results start an unobtrusive snapshot check after a delay.
  // A run number prevents an older timer or response from replacing a newer
  // reconciliation, and both timer and fetch are canceled during unmount.
  const snapshotReconciliationTimerRef = useRef<number | null>(null);
  const snapshotReconciliationControllerRef = useRef<AbortController | null>(null);
  const snapshotReconciliationRunRef = useRef(0);

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

  // Mirror the visible timestamp state for delayed skipped-refresh
  // reconciliation. Updating this ref does not cause a render; it only gives
  // async callbacks a current baseline to compare against.
  useEffect(() => {
    updatedAtByAreaRef.current = updatedAtByArea;
  }, [updatedAtByArea]);

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
  // filter task entries by status and text, then remove empty groups. Thoughtful
  // cards are browse-and-save content rather than tasks, so status never changes
  // them; text still searches their visible title, details, category, and saved
  // date. The complete `areas` state is retained, so clearing controls restores
  // every card in its server-provided order.
  const visibleAreas = useMemo(
    () =>
      areas
        .filter((area) => !activeArea || area.id === activeArea)
        .map((area) => {
          const entries = area.entries.filter((entry) => {
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
          });

          if (!area.thoughtfulSuggestions) return { ...area, entries };

          // A Favorite deliberately keeps its saved date in search. That lets a
          // person find an older saved gesture by the day they remember using it.
          const matchesThoughtfulSearch = (card: {
            title: string;
            details: string;
            category: string;
            savedAt?: string;
          }) =>
            !normalizedQuery ||
            [card.title, card.details, card.category, card.savedAt ?? ""]
              .join(" ")
              .toLocaleLowerCase()
              .includes(normalizedQuery);
          const thoughtfulSuggestions = {
            ...area.thoughtfulSuggestions,
            suggestions: area.thoughtfulSuggestions.suggestions.filter(matchesThoughtfulSearch),
            favorites: area.thoughtfulSuggestions.favorites.filter(matchesThoughtfulSearch),
          };
          return { ...area, entries, thoughtfulSuggestions };
        })
        .filter(
          (area) =>
            area.entries.length > 0 ||
            (area.thoughtfulSuggestions !== undefined &&
              (area.thoughtfulSuggestions.suggestions.length > 0 ||
                area.thoughtfulSuggestions.favorites.length > 0)),
        ),
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

  // The prompt editor is a true modal interaction. Background regions become
  // inert in the render below, while this effect moves focus into the textarea,
  // loops Tab within the dialog, handles Escape, and restores focus to the Edit
  // prompt button that opened it. Cancel, close, and Escape remain available
  // during a stalled PUT; closing aborts that request before removing the modal.
  useEffect(() => {
    if (!promptDialogArea) {
      if (restorePromptFocusRef.current) {
        restorePromptFocusRef.current = false;
        promptEditTriggerRef.current?.focus();
      }
      return;
    }

    // Loading keeps the textarea disabled until its stored value is known, so
    // focus the usable close control first. Once the editor becomes writable,
    // shift focus to the text itself so a keyboard user can begin immediately.
    if (promptDialogState === "editing") {
      promptTextareaRef.current?.focus();
    } else if (promptDialogState !== "saving") {
      promptDialogCloseRef.current?.focus();
    }

    function handlePromptDialogKeyDown(event: KeyboardEvent) {
      if (event.key === "Escape") {
        event.preventDefault();
        closePromptEditor();
        return;
      }

      if (event.key !== "Tab") return;

      const dialog = promptDialogRef.current;
      if (!dialog) return;
      const dialogControls = Array.from(
        dialog.querySelectorAll<HTMLElement>("textarea:not([disabled]), button:not([disabled])"),
      );
      const firstControl = dialogControls[0];
      const lastControl = dialogControls[dialogControls.length - 1];
      if (!firstControl || !lastControl) return;

      if (event.shiftKey && document.activeElement === firstControl) {
        event.preventDefault();
        lastControl.focus();
      } else if (!event.shiftKey && document.activeElement === lastControl) {
        event.preventDefault();
        firstControl.focus();
      } else if (!dialog.contains(document.activeElement)) {
        event.preventDefault();
        firstControl.focus();
      }
    }

    document.addEventListener("keydown", handlePromptDialogKeyDown);
    return () => document.removeEventListener("keydown", handlePromptDialogKeyDown);
  }, [promptDialogArea, promptDialogState]);

  // Cancel in-flight prompt and reconciliation work on unmount. Clearing the
  // delayed callback also guarantees a skipped refresh cannot issue a later GET
  // after the Review screen has been removed.
  useEffect(
    () => () => {
      promptRequestControllerRef.current?.abort();
      snapshotReconciliationControllerRef.current?.abort();
      if (snapshotReconciliationTimerRef.current !== null) {
        window.clearTimeout(snapshotReconciliationTimerRef.current);
      }
    },
    [],
  );

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
   * Stops a delayed snapshot reconciliation before a newer refresh begins.
   *
   * Only one refresh request can run at once, but a completed skipped request
   * may still have timers waiting. Canceling them prevents an older snapshot
   * from replacing content loaded by the person's newer refresh.
   */
  function cancelSnapshotReconciliation() {
    snapshotReconciliationRunRef.current += 1;
    snapshotReconciliationControllerRef.current?.abort();
    snapshotReconciliationControllerRef.current = null;
    if (snapshotReconciliationTimerRef.current !== null) {
      window.clearTimeout(snapshotReconciliationTimerRef.current);
      snapshotReconciliationTimerRef.current = null;
    }
  }

  /**
   * Checks for a newer saved snapshot after one or more update results skip.
   *
   * Go uses `skipped` when another request may already own the same update. The
   * page keeps its honest skipped message, waits, then loads a snapshot without
   * showing a success state. A changed timestamp ends the checks; otherwise the
   * fixed delay list bounds both request count and total lifetime.
   */
  function scheduleSnapshotReconciliation(areaIds: RefreshableAreaId[]) {
    cancelSnapshotReconciliation();

    const targetAreaIds = [...new Set(areaIds)];
    const baselineUpdatedAt = Object.fromEntries(
      targetAreaIds.map((areaId) => [areaId, updatedAtByAreaRef.current[areaId]]),
    ) as Partial<Record<RefreshableAreaId, string>>;
    const reconciliationRun = snapshotReconciliationRunRef.current;

    function scheduleAttempt(attemptIndex: number) {
      const delay = SNAPSHOT_RECONCILIATION_DELAYS_MS[attemptIndex];
      if (delay === undefined || snapshotReconciliationRunRef.current !== reconciliationRun) return;

      snapshotReconciliationTimerRef.current = window.setTimeout(async () => {
        snapshotReconciliationTimerRef.current = null;
        const requestController = new AbortController();
        snapshotReconciliationControllerRef.current = requestController;

        try {
          const snapshot = await fetchReviewAreaSnapshot(requestController.signal);
          if (
            requestController.signal.aborted ||
            snapshotReconciliationRunRef.current !== reconciliationRun
          ) {
            return;
          }

          // Content and timestamps always move together, just as they do during
          // the initial load, but reconciliation does not replace the page with
          // a loading screen or rewrite the existing skipped status message.
          setAreas(snapshot.areas);
          setUpdatedAtByArea(snapshot.updatedAtByArea);

          const allTargetsChanged = targetAreaIds.every(
            (areaId) => snapshot.updatedAtByArea[areaId] !== baselineUpdatedAt[areaId],
          );
          if (!allTargetsChanged) scheduleAttempt(attemptIndex + 1);
        } catch (error: unknown) {
          if (requestController.signal.aborted || isAbortError(error)) return;
          scheduleAttempt(attemptIndex + 1);
        } finally {
          if (snapshotReconciliationControllerRef.current === requestController) {
            snapshotReconciliationControllerRef.current = null;
          }
        }
      }, delay);
    }

    scheduleAttempt(0);
  }

  /**
   * Requests the saved instructions for an open prompt editor.
   *
   * The GET endpoint returns 404 when no enabled prompt exists. The API helper
   * turns that expected case into `null`, so the textarea becomes an empty
   * create form. Other failures leave the dialog open with a retry action and
   * never turn a failed load into an accidental blank overwrite.
   */
  async function loadAreaPrompt(areaId: RefreshableAreaId) {
    promptRequestControllerRef.current?.abort();
    const requestController = new AbortController();
    promptRequestControllerRef.current = requestController;
    setPromptDialogState("loading");
    setPromptDialogError("");

    try {
      const savedPrompt = await fetchAreaContentPrompt(areaId, requestController.signal);
      if (requestController.signal.aborted) return;

      setPromptText(savedPrompt?.prompt ?? "");
      setPromptDialogState("editing");
    } catch (error: unknown) {
      if (requestController.signal.aborted || isAbortError(error)) return;

      const message = error instanceof Error ? error.message : "Unable to load the prompt.";
      setPromptDialogError(`${message} Try again or cancel without making changes.`);
      setPromptDialogState("error");
    } finally {
      if (promptRequestControllerRef.current === requestController) {
        promptRequestControllerRef.current = null;
      }
    }
  }

  /**
   * Opens the named area's prompt dialog from its section heading.
   *
   * Capturing `trigger` before React renders the dialog gives Cancel, Escape,
   * and successful Save one reliable place to restore focus. Starting with a
   * blank local value also prevents the previously opened area's instructions
   * from appearing while this area's GET request is loading.
   */
  function openPromptEditor(areaId: RefreshableAreaId, trigger: HTMLButtonElement) {
    // Native disabled controls block ordinary clicks during refresh. This guard
    // also rejects a programmatic call before React paints that disabled state.
    if (updateInProgress) return;

    promptEditTriggerRef.current = trigger;
    restorePromptFocusRef.current = false;
    setPromptDialogArea(areaId);
    setPromptText("");
    void loadAreaPrompt(areaId);
  }

  /**
   * Closes the prompt editor without saving and returns focus to its trigger.
   *
   * Cancel, the close icon, and Escape call this function. If a GET or PUT has
   * stalled, aborting it first ensures a late response cannot change state or
   * announce an error after the dialog has already disappeared.
   */
  function closePromptEditor() {
    if (!promptDialogArea) return;

    promptRequestControllerRef.current?.abort();
    promptRequestControllerRef.current = null;
    restorePromptFocusRef.current = true;
    setPromptDialogArea(null);
    setPromptDialogError("");
  }

  /**
   * Saves a non-blank prompt without regenerating the area's content.
   *
   * Submitting the dialog form calls this function. Whitespace-only text is
   * rejected even if a caller bypasses the disabled Save button. On success the
   * dialog closes and focus returns to Edit prompt; `loadAttempt` is untouched,
   * proving that saving instructions does not refresh the Review queue.
   */
  async function savePromptEditor(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!promptDialogArea || promptDialogState === "saving") return;

    if (promptText.trim() === "") {
      setPromptDialogError("Enter prompt instructions before saving.");
      setPromptDialogState("editing");
      promptTextareaRef.current?.focus();
      return;
    }

    const areaId = promptDialogArea;
    promptRequestControllerRef.current?.abort();
    const requestController = new AbortController();
    promptRequestControllerRef.current = requestController;
    setPromptDialogState("saving");
    setPromptDialogError("");

    try {
      await saveAreaContentPrompt(areaId, promptText, requestController.signal);
      if (requestController.signal.aborted) return;

      // Do not increment loadAttempt here. The API contract persists only the
      // prompt, and preserving the visible entries confirms no regeneration ran.
      restorePromptFocusRef.current = true;
      setPromptDialogArea(null);
    } catch (error: unknown) {
      if (requestController.signal.aborted || isAbortError(error)) return;

      const message = error instanceof Error ? error.message : "Unable to save the prompt.";
      setPromptDialogError(`${message} Your entered prompt is still available to edit.`);
      setPromptDialogState("editing");
    } finally {
      if (promptRequestControllerRef.current === requestController) {
        promptRequestControllerRef.current = null;
      }
    }
  }

  /**
   * Regenerates the saved Home, Health, and Meals content after a button click.
   *
   * The POST request does not finish until Go has called OpenRouter, validated
   * the returned JSON, and attempted to save all three documents. Only an
   * `updated` result increments `loadAttempt`. A `skipped` result does not claim
   * success; it schedules the bounded background reconciliation described above
   * in case another request is still finishing the same document.
   */
  async function updateWeeklyPlans() {
    // The disabled button normally prevents a second call. This guard also
    // protects the function if it is invoked programmatically while one request
    // is already in progress.
    if (updateInProgress) return;

    cancelSnapshotReconciliation();
    setWeeklyUpdateState("updating");
    setWeeklyUpdateMessage("Updating Home, Health, and Meals. This can take a minute.");

    try {
      const results = await requestWeeklyAreaUpdate();
      const failedResults = results.filter((result) => result.status === "failed");
      const updatedResults = results.filter((result) => result.status === "updated");
      const skippedResults = results.filter((result) => result.status === "skipped");

      if (skippedResults.length > 0) {
        scheduleSnapshotReconciliation(
          skippedResults.map((result) => result.areaId as RefreshableAreaId),
        );
      }

      // A skipped response reports that no fresh content was saved. Reload only
      // when at least one target actually produced a new revision to display.
      if (updatedResults.length > 0) {
        setLoadAttempt((currentAttempt) => currentAttempt + 1);
      }

      if (failedResults.length === 0 && skippedResults.length === 0) {
        setWeeklyUpdateState("success");
        setWeeklyUpdateMessage("Home, Health, and Meals updated. The latest content is now loaded.");
        return;
      }

      const feedbackSentences: string[] = [];
      if (updatedResults.length > 0) {
        feedbackSentences.push(`${formatRefreshAreaNames(updatedResults.map((result) => result.areaId))} updated.`);
      }
      if (skippedResults.length > 0) {
        const skippedNames = formatRefreshAreaNames(skippedResults.map((result) => result.areaId));
        feedbackSentences.push(
          `${skippedNames} ${skippedResults.length === 1 ? "was" : "were"} not refreshed; an update may already be in progress.`,
        );
      }
      if (failedResults.length > 0) {
        feedbackSentences.push(
          `${formatRefreshAreaNames(failedResults.map((result) => result.areaId))} could not be updated.`,
        );
      }

      setWeeklyUpdateState(updatedResults.length > 0 || skippedResults.length > 0 ? "partial" : "error");
      setWeeklyUpdateMessage(
        `${feedbackSentences.join(" ")}${updatedResults.length === 0 && skippedResults.length === 0 ? " Try the update again." : ""}`,
      );
    } catch {
      // Network and non-successful HTTP responses use stable interface copy.
      // Detailed provider or database failures remain in the Go server logs.
      setWeeklyUpdateState("error");
      setWeeklyUpdateMessage("Home, Health, and Meals could not be updated. Check the server and try again.");
    }
  }

  /**
   * Regenerates one plan when its section-level Refresh button is clicked.
   *
   * `areaId` identifies the Home maintenance, Health workout, or Meals
   * recommendations document. The request remains pending while Go loads that
   * area's prompt, calls OpenRouter, validates the JSON, and saves the new
   * revision. A successful result reloads `/api/areas`, which replaces the
   * visible section entries with the newly saved plan.
   */
  async function updateAreaPlan(areaId: RefreshableAreaId) {
    // Disabled controls normally prevent overlap. This guard also protects
    // programmatic calls made before React has rendered the disabled state.
    if (updateInProgress) return;

    cancelSnapshotReconciliation();
    const areaName = refreshableAreaNames[areaId];
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

      // A skipped individual request did not save new content. Keep the current
      // rows and say why instead of treating a concurrency safeguard as success.
      if (result.status === "skipped") {
        scheduleSnapshotReconciliation([areaId]);
        setAreaUpdateFeedback((currentFeedback) => ({
          ...currentFeedback,
          [areaId]: {
            state: "skipped",
            message: `${areaName} was not refreshed; an update may already be in progress.`,
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
          message: `${areaName} updated. The latest content is now loaded.`,
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
   * queue. Home and Health immediately send the same state to Go; a failed PUT
   * restores the previous state and announces the safe failure. Meals and the
   * fixture-only areas intentionally remain browser-only because their rendered
   * entries do not have the confirmed entry-state persistence contract.
   */
  async function toggleEntry(areaId: AreaId, entryId: string, trigger: HTMLButtonElement) {
    // Resolve the current entry before updating so both the next state and the
    // accessibility announcement describe the exact same transition.
    const entry = areas.find((area) => area.id === areaId)?.entries.find((candidate) => candidate.id === entryId);
    if (!entry) return;

    const persistsEntryState = isPersistedEntryAreaId(areaId);
    const saveKey = entrySaveKey(areaId, entryId);
    if (persistsEntryState) {
      // The ref changes synchronously, closing the double-click window before
      // the disabled button produced by React can receive its next render.
      if (savingEntryKeysRef.current.has(saveKey)) return;
      savingEntryKeysRef.current.add(saveKey);
      setSavingEntryKeys(new Set(savingEntryKeysRef.current));
    }

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

    // Meals recommendations and fixture-only entries have no matching backend
    // entry-state route. Their checkboxes deliberately last for this browser
    // session only, matching the data adapter's generated local ids.
    if (!persistsEntryState) return;

    try {
      await saveAreaEntryState(areaId, entryId, nextState);
    } catch {
      // Restore only the state this request applied. This condition avoids
      // overwriting a newer snapshot if another screen-level load replaced the
      // entry while the PUT was pending.
      setAreas((currentAreas) =>
        currentAreas.map((area) => {
          if (area.id !== areaId) return area;

          return {
            ...area,
            entries: area.entries.map((currentEntry) =>
              currentEntry.id === entryId && currentEntry.state === nextState
                ? { ...currentEntry, state: entry.state }
                : currentEntry,
            ),
          };
        }),
      );
      setAnnouncement(`Could not save ${entry.title}. Its previous state was restored.`);
    } finally {
      savingEntryKeysRef.current.delete(saveKey);
      setSavingEntryKeys(new Set(savingEntryKeysRef.current));
    }
  }

  /**
   * Saves or removes one Thoughtful Favorite from a heart button.
   *
   * An empty weekly heart adds its source as a Favorite; a filled weekly heart
   * and a Favorite's own heart both remove the durable Favorite. The handler
   * captures the exact Thoughtful content and revision before its optimistic
   * edit. If Go rejects the conditional write, a fresh snapshot wins; only if
   * that GET also fails do we put this captured content back for a safe retry.
   */
  async function toggleThoughtfulFavorite(
    sourceSuggestionId: string,
    favoriteId?: string,
    trigger?: HTMLButtonElement,
    focusTarget?: HTMLElement,
  ) {
    // The rendered disabled state is useful feedback, but React applies it on
    // the next render. Set this ref first so two rapid clicks, including on two
    // different cards, cannot issue concurrent writes against one revision.
    if (thoughtfulFavoriteMutationRunningRef.current) return;

    const thoughtfulArea = areas.find((area) => area.id === "thoughtful");
    const previousContent = thoughtfulArea?.thoughtfulSuggestions;
    if (!thoughtfulArea || !previousContent) return;

    const sourceSuggestion = previousContent.suggestions.find(
      (suggestion) => suggestion.id === sourceSuggestionId,
    );
    const durableFavorite = favoriteId
      ? previousContent.favorites.find((favorite) => favorite.id === favoriteId)
      : previousContent.favorites.find((favorite) => favorite.sourceSuggestionId === sourceSuggestionId);
    const isRemoving = durableFavorite !== undefined;
    if (!isRemoving && !sourceSuggestion) return;

    // Copy the current content before the optimistic state update. Objects from
    // the adapter are never mutated, so this remains an exact rollback value.
    const priorThoughtfulContent: ThoughtfulSuggestionsContent = previousContent;
    thoughtfulFavoriteMutationRunningRef.current = true;
    setThoughtfulFavoriteMutationRunning(true);

    if (isRemoving) {
      // A Favorite-card heart disappears with its card, so move focus before
      // the optimistic render removes that button. Weekly hearts stay mounted
      // after a remove and deliberately keep their ordinary button focus.
      if (trigger && focusTarget && document.activeElement === trigger) {
        focusTarget.focus();
      }

      // Removing a Favorite immediately unfills the matching weekly heart and
      // removes its saved card. The server response supplies only a new revision.
      setAreas((currentAreas) =>
        currentAreas.map((area) =>
          area.id !== "thoughtful" || !area.thoughtfulSuggestions
            ? area
            : {
                ...area,
                thoughtfulSuggestions: {
                  ...area.thoughtfulSuggestions,
                  favorites: area.thoughtfulSuggestions.favorites.filter(
                    (favorite) => favorite.id !== durableFavorite.id,
                  ),
                },
              },
        ),
      );
    } else {
      // The temporary id exists only until PUT returns the server-owned durable
      // Favorite. Prepending follows the backend's newest-first Favorite order.
      // The guard above guarantees a source for this add-only branch. A remove
      // may legitimately target an older Favorite whose weekly source expired.
      const source = sourceSuggestion!;
      const optimisticFavorite: ThoughtfulFavorite = {
        id: `optimistic-${source.id}`,
        sourceSuggestionId: source.id,
        title: source.title,
        details: source.details,
        category: source.category,
        savedAt: new Date().toISOString().slice(0, 10),
      };
      setAreas((currentAreas) =>
        currentAreas.map((area) =>
          area.id !== "thoughtful" || !area.thoughtfulSuggestions
            ? area
            : {
                ...area,
                thoughtfulSuggestions: {
                  ...area.thoughtfulSuggestions,
                  favorites: [optimisticFavorite, ...area.thoughtfulSuggestions.favorites],
                },
              },
        ),
      );
    }

    try {
      const result = isRemoving
        ? await removeThoughtfulFavorite(durableFavorite.id, priorThoughtfulContent.revision)
        : await addThoughtfulFavorite(sourceSuggestion!.id, priorThoughtfulContent.revision);

      setAreas((currentAreas) =>
        currentAreas.map((area) => {
          if (area.id !== "thoughtful" || !area.thoughtfulSuggestions) return area;

          const nextContent = {
            ...area.thoughtfulSuggestions,
            revision: result.revision,
          };
          if (isRemoving) {
            // A concurrent whole-snapshot reload may have reintroduced the
            // Favorite while DELETE was pending. The successful mutation wins,
            // so remove that durable id from whatever content is current now.
            return {
              ...area,
              thoughtfulSuggestions: {
                ...nextContent,
                favorites: nextContent.favorites.filter(
                  (favorite) => favorite.id !== durableFavorite.id,
                ),
              },
            };
          }

          // A concurrent reload may have removed the optimistic record entirely
          // or reintroduced an older copy. Upsert by source id so the returned
          // Favorite is present exactly once with every server-confirmed field.
          return {
            ...area,
            thoughtfulSuggestions: {
              ...nextContent,
              favorites: [
                result.favorite,
                ...nextContent.favorites.filter(
                  (favorite) => favorite.sourceSuggestionId !== result.favorite.sourceSuggestionId,
                ),
              ],
            },
          };
        }),
      );
      setAnnouncement(
        isRemoving
          ? `Removed ${durableFavorite.title} from Favorites.`
          : `Saved ${sourceSuggestion!.title} to Favorites.`,
      );
    } catch {
      try {
        // A conflict is not safely recoverable from local assumptions. Reloading
        // both content and timestamps keeps the page's next revision aligned
        // with the authoritative document before the person tries another heart.
        const snapshot = await fetchReviewAreaSnapshot();
        setAreas(snapshot.areas);
        setUpdatedAtByArea(snapshot.updatedAtByArea);
        setAnnouncement("Could not save that Favorite change. The latest saved Favorites are shown.");
      } catch {
        // A second network failure must not leave an optimistic card pretending
        // to be durable. Restore only Thoughtful so unrelated local task work
        // and any newer timestamp map from a concurrent reload are preserved.
        setAreas((currentAreas) =>
          currentAreas.map((area) =>
            area.id === "thoughtful"
              ? { ...area, thoughtfulSuggestions: priorThoughtfulContent }
              : area,
          ),
        );
        setAnnouncement("Could not save that Favorite change. Your previous Favorites were restored.");
      }
    } finally {
      thoughtfulFavoriteMutationRunningRef.current = false;
      setThoughtfulFavoriteMutationRunning(false);
    }
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
        inert={promptDialogArea ? true : undefined}
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
            // always answer the stable question: how much remains open here.
            // Thoughtful has no open/done work, so omitting its count avoids
            // wrongly presenting its browse-and-save cards as "0 open" tasks.
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
                {area.id !== "thoughtful" && <span className="nav-count">{areaOpenCount}</span>}
              </button>
            );
          })}
        </nav>

      </aside>

      {/* This compact header exists only below the desktop breakpoint and keeps
          the current open count visible without consuming review space. */}
      <header className="mobile-header" inert={navOpen || promptDialogArea ? true : undefined}>
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

      <main className="main-content" inert={navOpen || promptDialogArea ? true : undefined}>
        <div className="main-inner">
          {/* The page header states the session's job and adapts its copy when a
              single area is selected from navigation or the cadence rail. */}
          <header className="page-header">
            <div>
              <p className="eyebrow">Daily review · {dateLabel}</p>
              <h1>{activeAreaName ? `${activeAreaName}, at a glance.` : "What needs your attention."}</h1>
              <p className="page-summary">
                {areaLoadState === "loading"
                  ? "Loading your current Home, Health, Meals, and Thoughtful Suggestions review."
                  : areaLoadState === "error"
                    ? "Your saved review data could not be loaded. You can retry below."
                    : activeAreaName
                      ? activeArea === "thoughtful"
                        ? "A focused view for browsing this week's caring ideas and saving Favorites."
                        : `A focused view of the open work in ${activeAreaName.toLocaleLowerCase()}.`
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
                  ? "Updating Home, Health, and Meals…"
                  : "Refresh Home, Health, and Meals"}
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
                // Thoughtful instead names its Favorites because its cards are
                // deliberately outside the task completion model.
                const areaOpenCount = area.entries.filter((entry) => entry.state === "open").length;
                const areaUpdatedAt = updatedAtByArea[area.id];
                const thoughtfulContent = area.thoughtfulSuggestions;
                // `area` comes from `visibleAreas`, so its Favorites may have
                // been removed by the search query. Weekly heart state must
                // still use the complete Favorites list from `areas`; otherwise
                // a visible weekly idea can look unsaved merely because the
                // saved card's title or details do not match the query.
                const unfilteredThoughtfulContent = areas.find(
                  (candidate) => candidate.id === area.id,
                )?.thoughtfulSuggestions;

                // Mail and Money are fixture-only review areas. Home, Health,
                // and Meals are backed by the configured API, so every rendered
                // section in that set receives both Edit prompt and Refresh.
                const refreshableAreaId = isRefreshableAreaId(area.id) ? area.id : null;
                const updateFeedback = refreshableAreaId
                  ? areaUpdateFeedback[refreshableAreaId]
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
                            {thoughtfulContent
                              ? `${thoughtfulContent.favorites.length} ${thoughtfulContent.favorites.length === 1 ? "Favorite" : "Favorites"}`
                              : filter === "open"
                                ? `${areaOpenCount} open`
                                : `${area.entries.length} shown`}
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

                      {refreshableAreaId && (
                        <div className="area-update-actions">
                          {/* Edit prompt is disabled during every content refresh.
                              A bulk request processes areas sequentially, so a
                              prompt changed mid-run could otherwise affect only
                              the targets Go has not reached yet. */}
                          <button
                            className="area-prompt-button"
                            type="button"
                            onClick={(event) => openPromptEditor(refreshableAreaId, event.currentTarget)}
                            disabled={updateInProgress}
                          >
                            Edit {area.name} prompt
                          </button>

                          {/* This secondary action refreshes only the section
                              whose heading contains it. All refresh controls are
                              disabled during the request to avoid revision races. */}
                          <button
                            className="area-update-button"
                            type="button"
                            onClick={() => updateAreaPlan(refreshableAreaId)}
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

                    {thoughtfulContent ? (
                      /* Thoughtful cards are semantically separate from task rows:
                         neither weekly ideas nor Favorites receive check controls,
                         and their headings make the browse-and-save grouping clear. */
                      <div className="thoughtful-content">
                        <section className="thoughtful-region" aria-labelledby={`thoughtful-week-${area.id}`}>
                          <h3 id={`thoughtful-week-${area.id}`}>This Week</h3>
                          {thoughtfulContent.suggestions.map((suggestion) => {
                            const favorite = unfilteredThoughtfulContent?.favorites.find(
                              (candidate) => candidate.sourceSuggestionId === suggestion.id,
                            );
                            const isFavorite = favorite !== undefined;
                            return (
                              <article className="thoughtful-card thoughtful-suggestion-card" key={suggestion.id}>
                                <div className="thoughtful-card-copy">
                                  <h4>{suggestion.title}</h4>
                                  <p>{suggestion.details}</p>
                                  <p className="thoughtful-category"><b>Category</b> {suggestion.category}</p>
                                </div>
                                <button
                                  className="thoughtful-favorite-action"
                                  type="button"
                                  onClick={(event) =>
                                    void toggleThoughtfulFavorite(
                                      suggestion.id,
                                      favorite?.id,
                                      event.currentTarget,
                                    )
                                  }
                                  disabled={thoughtfulFavoriteMutationRunning}
                                  aria-pressed={isFavorite}
                                  aria-busy={thoughtfulFavoriteMutationRunning}
                                  aria-label={`${isFavorite ? "Remove" : "Add"} ${suggestion.title} ${isFavorite ? "from" : "to"} Favorites`}
                                >
                                  <Heart size={20} weight={isFavorite ? "fill" : "regular"} aria-hidden="true" />
                                </button>
                              </article>
                            );
                          })}
                        </section>

                        <section className="thoughtful-region thoughtful-favorites-region" aria-labelledby={`thoughtful-favorites-${area.id}`}>
                          <h3
                            id={`thoughtful-favorites-${area.id}`}
                            ref={thoughtfulFavoritesHeadingRef}
                            tabIndex={-1}
                          >
                            Favorites
                          </h3>
                          {thoughtfulContent.favorites.length > 0 ? (
                            thoughtfulContent.favorites.map((favorite) => (
                              <article className="thoughtful-card thoughtful-favorite-card" key={favorite.id}>
                                <div className="thoughtful-card-copy">
                                  <h4>{favorite.title}</h4>
                                  <p>{favorite.details}</p>
                                  <p className="thoughtful-category"><b>Category</b> {favorite.category}</p>
                                  <p className="thoughtful-saved-at"><b>Saved</b> <time dateTime={favorite.savedAt}>{favorite.savedAt}</time></p>
                                </div>
                                <button
                                  className="thoughtful-favorite-action"
                                  type="button"
                                  onClick={(event) =>
                                    void toggleThoughtfulFavorite(
                                      favorite.sourceSuggestionId,
                                      favorite.id,
                                      event.currentTarget,
                                      thoughtfulFavoritesHeadingRef.current ?? undefined,
                                    )
                                  }
                                  disabled={thoughtfulFavoriteMutationRunning}
                                  aria-pressed="true"
                                  aria-busy={thoughtfulFavoriteMutationRunning}
                                  aria-label={`Remove ${favorite.title} from Favorites`}
                                >
                                  <Heart size={20} weight="fill" aria-hidden="true" />
                                </button>
                              </article>
                            ))
                          ) : (
                            <p className="thoughtful-empty-favorites">
                              {normalizedQuery
                                ? "No saved suggestions match this search."
                                : "Save a suggestion from This Week to keep it in Favorites."}
                            </p>
                          )}
                        </section>
                      </div>
                    ) : (
                      <div className="entry-list">
                        {area.entries.map((entry) => {
                        // Only Health interprets separators as workout boundaries.
                        // Mail summaries, Home instructions, and meal descriptions
                        // keep their original prose even if they contain line breaks.
                        const workoutDetailItems =
                          area.id === "health" && entry.details
                            ? readWorkoutDetailItems(entry.details)
                            : [];

                        return (
                          <article
                            className={`entry-row ${entry.state === "done" ? "is-done" : ""}`}
                            key={entry.id}
                          >
                            {/* A button is used instead of a native checkbox so the
                                interface can update immediately, expose a saving
                                state for persisted Home and Health rows, and roll
                                back if the backend rejects either change. */}
                            <button
                              className="check-control"
                              type="button"
                              onClick={(event) => void toggleEntry(area.id, entry.id, event.currentTarget)}
                              disabled={savingEntryKeys.has(entrySaveKey(area.id, entry.id))}
                              aria-label={`${entry.state === "open" ? "Mark" : "Restore"} ${entry.title}`}
                              aria-pressed={entry.state === "done"}
                              aria-busy={savingEntryKeys.has(entrySaveKey(area.id, entry.id))}
                            >
                              <span className="check-dot">
                                {entry.state === "done" && <Check size={14} weight="bold" aria-hidden="true" />}
                              </span>
                            </button>

                            <div className="entry-copy">
                              <h3>{entry.title}</h3>
                              {workoutDetailItems.length > 0 ? (
                                // A real list gives screen-reader and keyboard-tool
                                // users the same item boundaries visible as bullets.
                                <ul
                                  className="entry-workout-list"
                                  aria-label={`Exercises for ${entry.title}`}
                                >
                                  {workoutDetailItems.map((item, itemIndex) => (
                                    <li key={`${entry.id}-workout-${itemIndex}`}>{item}</li>
                                  ))}
                                </ul>
                              ) : (
                                entry.details && <p>{entry.details}</p>
                              )}
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
                        );
                        })}
                      </div>
                    )}
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

      {promptDialogArea && (
        /* This sibling follows the inert background in DOM order. Its labelled
           dialog role, modal semantics, focus loop, and Escape path make the
           prompt editor usable without a mouse on both desktop and mobile. */
        <div className="prompt-dialog-backdrop">
          <div
            ref={promptDialogRef}
            className="prompt-dialog"
            role="dialog"
            aria-modal="true"
            aria-labelledby="prompt-dialog-title"
            aria-describedby="prompt-dialog-description"
          >
            <div className="prompt-dialog-heading">
              <div>
                <p className="eyebrow">Generation instructions</p>
                <h2 id="prompt-dialog-title">Edit {refreshableAreaNames[promptDialogArea]} prompt</h2>
              </div>
              <button
                ref={promptDialogCloseRef}
                className="prompt-dialog-close"
                type="button"
                onClick={closePromptEditor}
                aria-label="Cancel editing prompt"
              >
                <X size={20} aria-hidden="true" />
              </button>
            </div>

            <p id="prompt-dialog-description">
              These instructions apply the next time {refreshableAreaNames[promptDialogArea]} is refreshed.
              Saving does not refresh the content shown now.
            </p>

            <form onSubmit={savePromptEditor}>
              <label className="prompt-textarea-label" htmlFor="area-prompt-text">
                Prompt instructions
              </label>
              <textarea
                ref={promptTextareaRef}
                id="area-prompt-text"
                className="prompt-textarea"
                value={promptText}
                onChange={(event) => {
                  setPromptText(event.target.value);
                  if (promptDialogError) setPromptDialogError("");
                }}
                disabled={
                  promptDialogState === "loading" ||
                  promptDialogState === "saving" ||
                  promptDialogState === "error"
                }
                aria-invalid={promptDialogError ? true : undefined}
                aria-describedby={promptDialogError ? "prompt-dialog-error" : undefined}
                rows={14}
              />

              {promptDialogState === "loading" && (
                <p className="prompt-dialog-message" role="status">Loading the saved prompt…</p>
              )}
              {promptDialogError && (
                <p id="prompt-dialog-error" className="prompt-dialog-message is-error" role="alert">
                  {promptDialogError}
                </p>
              )}

              <div className="prompt-dialog-actions">
                <button
                  type="button"
                  className="prompt-cancel-button"
                  onClick={closePromptEditor}
                >
                  Cancel
                </button>
                {promptDialogState === "error" ? (
                  <button
                    type="button"
                    className="prompt-save-button"
                    onClick={() => void loadAreaPrompt(promptDialogArea)}
                  >
                    Try again
                  </button>
                ) : (
                  <button
                    type="submit"
                    className="prompt-save-button"
                    disabled={
                      promptDialogState === "loading" ||
                      (promptDialogState === "editing" && promptText.trim() === "")
                    }
                    aria-disabled={promptDialogState === "saving" ? true : undefined}
                    aria-busy={promptDialogState === "saving"}
                  >
                    {promptDialogState === "saving" ? "Saving prompt…" : "Save prompt"}
                  </button>
                )}
              </div>
            </form>
          </div>
        </div>
      )}

    </div>
  );
}
