# Personal Review Console — frontend direction

## Product thesis

This is a private operating surface for one person who needs to notice, decide, and move on. The first screen is **Review**: a live queue assembled from recurring areas such as health, meals, reading, home, money, and mail. The interface should feel closer to a well-kept field instrument than a business dashboard—quiet until something needs attention, precise when it does.

User-facing language describes recognizable areas and actions. “Loop” remains an implementation-neutral concept where useful, but it is not the product's default noun. The main vocabulary is **Review**, **Areas**, **Entries**, **Open**, and **Done**.

## Architecture for a replaceable backend

The React application will live entirely under `frontend/` and will not import Python-owned shapes or assume FastAPI routes.

```text
frontend/src/
  app/          router, providers, shell
  domain/       explicit TypeScript models and repository contracts
  data/         mock repository now; HTTP repository and DTO mappers later
  fixtures/     realistic review-area data
  features/     review queue, area detail, entry editing, preferences
  components/   shared accessible controls and text-safe primitives
  styles/       tokens, reset, global layout, motion preferences
```

The domain boundary will use `ReviewArea`, `ReviewSection`, and `ReviewEntry` models. Components will call an asynchronous `ReviewRepository` interface (`listAreas`, `getArea`, `setEntryState`, `createEntry`, `updateEntry`) rather than loading JSON directly. A mock implementation can persist a small overlay to `localStorage`; a future Go adapter can translate versioned API DTOs into the same domain models. Transport details, timestamps used for conflict detection, and backend-specific metadata stay inside the data layer.

Initial routes:

- `/` — Review queue across all areas; this is the usable default screen.
- `/areas/:areaId` — focused area with open and completed entries grouped by section.
- `/preferences` — data source and display preferences, kept intentionally small.

## Design system: Cadence

### Color

- **Graphite** `#18221F` — primary text and the app's visual anchor.
- **Milkglass** `#F3F6F2` — cool, low-glare workspace background.
- **Porcelain** `#FCFDFC` — focused surfaces and editable controls.
- **Juniper** `#2F6B5F` — completion, active navigation, and primary actions.
- **Iris** `#5B63A6` — keyboard focus, selection, and linked information.
- **Saffron** `#D7A63E` — sparingly marks time-sensitive attention.

Status is never communicated by color alone. Borders, labels, icons, and copy carry the same meaning.

### Type

- **Display — Literata Variable:** restrained use for the current date, page titles, and area headings. Its reading-oriented forms make reflection feel deliberate without turning the interface into an editorial layout.
- **Body — Public Sans Variable:** highly readable at compact sizes and neutral enough for long task details, email summaries, and workout instructions.
- **Utility — IBM Plex Mono:** dates, counts, shortcuts, source labels, and machine-adjacent metadata. Tabular numerals keep changing counts stable.

The default body size is 16px with a generous 1.55 line height. Entry titles cap line length rather than truncate; details preserve line breaks and use `overflow-wrap: anywhere` for hostile URLs and identifiers.

### Layout concepts considered

**A — Three-pane inspector**

```text
┌ areas ┐┌──────── review queue ────────┐┌ entry detail ┐
│       ││ grouped items and filters    ││ notes/actions│
└───────┘└──────────────────────────────┘└──────────────┘
```

Efficient on a large monitor, but too close to an admin console and too fragile on mobile. It also makes ordinary entries feel like records to administer.

**B — Cadence rail (chosen)**

```text
┌ quiet area nav ┐ ┌ date + review controls ─────────────┐
│ Review         │ │ ┃ Mail · 3                           │
│ Health         │ │ ┣━ actionable entries               │
│ Meals          │ │ ┃ Home · 2                           │
│ Reading        │ │ ┣━ actionable entries               │
│ …              │ │ ┗━ clear stopping point             │
└────────────────┘ └──────────────────────────────────────┘
```

Desktop uses a narrow persistent navigation column and one generous reading canvas (roughly 68–78 characters for entry text). The main canvas is intentionally asymmetric: the cadence rail occupies the left edge while entries align to a calm text grid. Mobile collapses navigation into a compact top drawer and rotates the rail into a horizontally scrollable area strip; entry controls remain full-width and thumb reachable.

### Signature interaction — the cadence rail

A single continuous rail stitches the review groups together. Each area is an actual navigation stop with an open count, not decoration. The active stop expands into a short labeled notch; completing the final open entry closes that segment with one restrained contraction. Pointer hover and keyboard focus expose the same information. Under `prefers-reduced-motion: reduce`, the state changes instantly with no spatial animation.

## Main-screen behavior

The Review screen opens with the date and a plain status sentence such as “12 entries need a decision across 5 areas.” A compact filter row offers **Open**, **All**, and **Done**, plus a text search. “Done” intentionally describes completion state without implying a recency window until the domain model includes completion timestamps. Area sections follow the cadence rail. Each entry supports completion, readable details, relevant metadata, and an external link when present. Add/edit interactions use a focused sheet on desktop and a full-height dialog on mobile; raw JSON is not part of the everyday workflow.

Empty states give a next action (“Nothing needs review. Show done entries”) rather than praise or filler. Loading uses stable skeleton rows, errors identify the affected area, and optimistic completion remains reversible until persistence succeeds.

## Critique and revision

The first instinct—an almanac-like cream canvas with serif headings—fit recurring personal rituals but landed too close to a common editorial-dashboard pattern. The direction was revised to a cool mineral palette, an accessibility-oriented sans for working text, and the functional cadence rail as the single aesthetic risk. Literata remains only as a small reflective accent.

This fits the app because recurrence is represented as cadence, not as KPI cards or productivity gamification. The rail answers “where does my attention move next,” while the uninterrupted reading canvas handles genuinely long health routines, news summaries, and mail context. There is no hero, metric-card grid, ornamental chart, or business-facing sidebar taxonomy. The result should feel personal and composed without making routine maintenance theatrical.

## Quality floor

- Fully keyboard-operable navigation, filters, completion controls, dialogs, and menus.
- `:focus-visible` treatment uses Iris with a two-layer outline that survives light surfaces.
- Touch targets are at least 44×44px where space allows.
- Responsive from 320px upward with no clipped identifiers, URLs, or preserved-line details.
- Motion is concentrated in cadence state changes and disabled for reduced-motion users.
- Semantic HTML and visible labels precede ARIA; icon-only controls always have accessible names.
- The production build must pass TypeScript checks and Vite compilation before handoff.
