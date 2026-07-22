/**
 * Frontend-owned domain vocabulary for the personal review experience.
 *
 * These models deliberately describe what the interface needs instead of
 * mirroring the current Python payloads. A small translator in the data layer
 * (often called an adapter) can convert mock, FastAPI, or Go responses into
 * these stable types before the UI receives them.
 */

/** Stable identifiers used for navigation, filtering, and data-source lookups. */
export type AreaId = "mail" | "home" | "health" | "reading" | "meals" | "money";

/** The intentionally small set of open/done states currently shown by the Review screen. */
export type EntryState = "open" | "done";

/** User-selectable queue views. `all` includes both open and completed work. */
export type QueueFilter = "open" | "all" | "done";

/** Restrained palette roles that visually distinguish areas without encoding status alone. */
export type Accent = "juniper" | "iris" | "saffron";

/** A compact label/value pair rendered beneath an entry's primary explanation. */
export interface EntryMetadata {
  /** Short utility label such as "Due", "From", or "Category". */
  label: string;
  /** Human-readable value associated with the label. */
  value: string;
  /** Adds a visual attention marker while preserving the text label as the semantic signal. */
  attention?: boolean;
}

/** A single actionable or completed piece of personal work. */
export interface ReviewEntry {
  /** Stable identity used when updating one entry without relying on display text. */
  id: string;
  /** Scannable action-oriented summary shown as the row heading. */
  title: string;
  /** Optional long-form context; the UI must preserve wrapping and remain readable. */
  details?: string;
  /** Optional source or destination opened in a separate browser tab. */
  href?: string;
  /** Current completion state controlled by the row's checkbox-style button. */
  state: EntryState;
  /** Secondary details kept explicit instead of accepting arbitrary backend fields in components. */
  metadata: EntryMetadata[];
}

/**
 * One top-level group on the Review screen, such as Mail, Home, or Health.
 *
 * The component renders each `ReviewArea` as one `<section>` with the CSS class
 * `.area-section`. In that sense, the domain object supplies the data for the
 * visible area section, but it is not a CSS class or React component itself.
 *
 * The current mock keeps entries directly inside an area. If Mail later needs
 * smaller groups such as "Needs attention" and "Waiting on", those would become
 * a separate `ReviewSection` model nested inside this `ReviewArea`.
 */
export interface ReviewArea {
  /** Stable identifier shared by navigation and entry mutation calls. */
  id: AreaId;
  /** User-facing name; avoids exposing implementation language such as "loop". */
  name: string;
  /** One-sentence explanation of why this area appears in the review queue. */
  description: string;
  /** Visual role used by icons and the cadence rail. */
  accent: Accent;
  /** Entries currently known to the frontend data source. */
  entries: ReviewEntry[];
}
