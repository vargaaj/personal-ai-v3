/**
 * The first usable Review screen and its temporary client-side behavior.
 *
 * This component composes the responsive shell, derives the cross-area queue,
 * and applies in-memory completion updates. It depends only on frontend domain
 * models. A later data-service module can replace the fixture and save changes
 * without changing the words or structures the interface renders.
 */
import { useEffect, useMemo, useRef, useState } from "react";
import {
  ArrowSquareOut,
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
import type { AreaId, EntryState, QueueFilter } from "./domain/review";
import { initialAreas } from "./fixtures/reviewAreas";

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
  // `areas` is the temporary data source stored in browser memory. When an
  // entry changes, the code creates updated copies instead of editing the
  // imported fixture itself. A backend response can replace this state later.
  const [areas, setAreas] = useState(initialAreas);

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
      // changing the imported fixture and helps React skip unnecessary updates.
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

        <div className="sidebar-foot">
          <p><span className="sync-dot" /> Last reviewed today at 8:32 AM</p>
        </div>
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
              <h1>{activeAreaName ? `${activeAreaName}, at a glance.` : "What needs a decision."}</h1>
              <p className="page-summary">
                {activeAreaName
                  ? `A focused view of the open work in ${activeAreaName.toLocaleLowerCase()}.`
                  : `${openCount} entries are open across ${openAreaCount} ${openAreaCount === 1 ? "area" : "areas"}. Start at the top or choose an area.`}
              </p>
            </div>
            <div className="cleared-note" aria-label={`${doneCount} completed entries`}>
              <CheckCircle size={20} weight="duotone" aria-hidden="true" />
              <span><strong>{doneCount} cleared</strong><small>done</small></span>
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
              implementation-specific loop or file structure. */}
          <div className="queue">
            {visibleAreas.length > 0 ? (
              visibleAreas.map((area) => {
                // This count describes the currently visible copy in open mode;
                // other filters switch the label to a neutral "shown" count.
                const areaOpenCount = area.entries.filter((entry) => entry.state === "open").length;
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
                      <div>
                        <div className="area-title-line">
                          <h2 id={`area-${area.id}`}>{area.name}</h2>
                          <span className="area-count">
                            {filter === "open" ? `${areaOpenCount} open` : `${area.entries.length} shown`}
                          </span>
                        </div>
                        <p>{area.description}</p>
                      </div>
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
