/**
 * Regression coverage for the stateful Review screen.
 *
 * These component tests exercise the interactions a keyboard user experiences
 * while replacing the real API call with deterministic domain data. They use
 * visible names and roles so changes that weaken the interface's accessibility
 * contract fail alongside changes to loading, filtering, counts, or focus.
 */
import "@testing-library/jest-dom/vitest";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import ReviewApp from "./App";
import {
  fetchReviewAreaSnapshot,
  requestAreaContentUpdate,
  requestWeeklyAreaUpdate,
} from "./data/areaContentApi";
import { initialAreas } from "./fixtures/reviewAreas";

// The adapter has its own tests for translating real Go response shapes. These
// component tests replace only the network boundary with resolved domain data,
// keeping interaction coverage deterministic and independent of Supabase.
vi.mock("./data/areaContentApi", () => ({
  fetchReviewAreaSnapshot: vi.fn(),
  requestAreaContentUpdate: vi.fn(),
  requestWeeklyAreaUpdate: vi.fn(),
}));

const fetchReviewAreaSnapshotMock = vi.mocked(fetchReviewAreaSnapshot);
const requestAreaContentUpdateMock = vi.mocked(requestAreaContentUpdate);
const requestWeeklyAreaUpdateMock = vi.mocked(requestWeeklyAreaUpdate);

beforeEach(() => {
  fetchReviewAreaSnapshotMock.mockReset();
  fetchReviewAreaSnapshotMock.mockResolvedValue({
    areas: initialAreas,
    updatedAtByArea: {
      health: "2026-07-26T12:15:00Z",
      meals: "2026-07-25T22:45:00Z",
    },
  });
  requestAreaContentUpdateMock.mockReset();
  requestAreaContentUpdateMock.mockImplementation(async (areaId) => ({
    areaId,
    contentType:
      areaId === "health"
        ? "weekly_workout_routine"
        : "weekly_meal_recommendations",
    status: "updated",
    revision: 2,
  }));
  requestWeeklyAreaUpdateMock.mockReset();
  requestWeeklyAreaUpdateMock.mockResolvedValue([
    {
      areaId: "health",
      contentType: "weekly_workout_routine",
      status: "updated",
      revision: 2,
    },
    {
      areaId: "meals",
      contentType: "weekly_meal_recommendations",
      status: "updated",
      revision: 2,
    },
  ]);
});

// Vitest does not enable Testing Library's global cleanup hook automatically.
// Removing each rendered app keeps fixture state and focused elements isolated
// so one completion interaction cannot affect the next test.
afterEach(() => cleanup());

/**
 * Renders a fresh Review screen and returns a user-event controller.
 *
 * user-event models browser interaction more closely than calling handlers
 * directly: clicks move focus, keyboard activation dispatches the expected
 * events, and React updates are awaited before assertions continue.
 */
async function renderReview() {
  const user = userEvent.setup();
  render(<ReviewApp />);

  // App.tsx deliberately renders a loading state first. Waiting for a fixture
  // heading ensures each interaction starts after the mocked API promise has
  // populated React state, matching how a person waits for the screen to load.
  await screen.findByRole("heading", { name: "Mail" });
  return user;
}

describe("ReviewApp", () => {
  it("shows each stored area's own update time and omits the old sidebar status", async () => {
    await renderReview();

    const healthUpdatedAt = screen
      .getByRole("region", { name: "Health" })
      .querySelector("time");
    const mealsUpdatedAt = screen
      .getByRole("region", { name: "Meals" })
      .querySelector("time");

    // The exact ISO values prove that each label belongs to its own database
    // row. Visible text is formatted in the machine's local timezone, so the
    // assertion checks the stable wording without assuming a test-runner zone.
    expect(healthUpdatedAt).toHaveAttribute("datetime", "2026-07-26T12:15:00Z");
    expect(healthUpdatedAt).toHaveTextContent(/^Last updated at /);
    expect(mealsUpdatedAt).toHaveAttribute("datetime", "2026-07-25T22:45:00Z");
    expect(mealsUpdatedAt).toHaveTextContent(/^Last updated at /);
    expect(screen.queryByText(/Last reviewed/i)).not.toBeInTheDocument();
  });

  it("disables the weekly update button until both plans finish and then reloads the review", async () => {
    let finishUpdate: (
      results: Awaited<ReturnType<typeof requestWeeklyAreaUpdate>>,
    ) => void = () => {};
    requestWeeklyAreaUpdateMock.mockReturnValueOnce(
      new Promise((resolve) => {
        finishUpdate = resolve;
      }),
    );

    const user = await renderReview();
    const updateButton = screen.getByRole("button", { name: "Refresh weekly plans" });

    await user.click(updateButton);

    expect(requestWeeklyAreaUpdateMock).toHaveBeenCalledTimes(1);
    expect(screen.getByRole("button", { name: "Updating weekly plans…" })).toBeDisabled();
    expect(
      screen.getByText("Updating Health and Meals. This can take a minute."),
    ).toBeInTheDocument();

    await act(async () => {
      finishUpdate([
        {
          areaId: "health",
          contentType: "weekly_workout_routine",
          status: "updated",
          revision: 3,
        },
        {
          areaId: "meals",
          contentType: "weekly_meal_recommendations",
          status: "updated",
          revision: 4,
        },
      ]);
    });

    expect(
      await screen.findByText("Health and Meals updated. The latest weekly plans are now loaded."),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Refresh weekly plans" })).toBeEnabled();
    expect(fetchReviewAreaSnapshotMock).toHaveBeenCalledTimes(2);
  });

  it("shows which weekly plan failed while reloading the successful plan", async () => {
    requestWeeklyAreaUpdateMock.mockResolvedValueOnce([
      {
        areaId: "health",
        contentType: "weekly_workout_routine",
        status: "updated",
        revision: 3,
      },
      {
        areaId: "meals",
        contentType: "weekly_meal_recommendations",
        status: "failed",
      },
    ]);

    const user = await renderReview();
    await user.click(screen.getByRole("button", { name: "Refresh weekly plans" }));

    expect(
      await screen.findByText("Meals could not be updated. The other weekly plan is ready."),
    ).toBeInTheDocument();
    expect(fetchReviewAreaSnapshotMock).toHaveBeenCalledTimes(2);
  });

  it("updates Health by itself and blocks every refresh control until the save finishes", async () => {
    // The successful POST triggers a second areas GET. Giving that response a
    // later Health timestamp verifies that the visible label follows the saved
    // refresh rather than remaining at its initial page-load value.
    fetchReviewAreaSnapshotMock
      .mockResolvedValueOnce({
        areas: initialAreas,
        updatedAtByArea: {
          health: "2026-07-26T12:15:00Z",
          meals: "2026-07-25T22:45:00Z",
        },
      })
      .mockResolvedValueOnce({
        areas: initialAreas,
        updatedAtByArea: {
          health: "2026-07-26T15:30:00Z",
          meals: "2026-07-25T22:45:00Z",
        },
      });

    let finishUpdate: (
      result: Awaited<ReturnType<typeof requestAreaContentUpdate>>,
    ) => void = () => {};
    requestAreaContentUpdateMock.mockReturnValueOnce(
      new Promise((resolve) => {
        finishUpdate = resolve;
      }),
    );

    const user = await renderReview();
    await user.click(screen.getByRole("button", { name: "Refresh Health" }));

    expect(requestAreaContentUpdateMock).toHaveBeenCalledWith("health");
    expect(screen.getByRole("button", { name: "Updating Health…" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Refresh Meals" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Refresh weekly plans" })).toBeDisabled();
    expect(screen.getByText("Updating Health. This can take a minute.")).toBeInTheDocument();

    await act(async () => {
      finishUpdate({
        areaId: "health",
        contentType: "weekly_workout_routine",
        status: "updated",
        revision: 3,
      });
    });

    expect(
      await screen.findByText("Health updated. The latest plan is now loaded."),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Refresh Health" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "Refresh Meals" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "Refresh weekly plans" })).toBeEnabled();
    expect(fetchReviewAreaSnapshotMock).toHaveBeenCalledTimes(2);
    expect(
      await screen.findByText(/^Last updated at /, {
        selector: 'time[datetime="2026-07-26T15:30:00Z"]',
      }),
    ).toBeInTheDocument();
  });

  it("keeps the saved Meals entries visible when its individual update fails", async () => {
    requestAreaContentUpdateMock.mockResolvedValueOnce({
      areaId: "meals",
      contentType: "weekly_meal_recommendations",
      status: "failed",
    });

    const user = await renderReview();
    await user.click(screen.getByRole("button", { name: "Refresh Meals" }));

    expect(requestAreaContentUpdateMock).toHaveBeenCalledWith("meals");
    expect(
      await screen.findByText("Meals could not be updated. Try the update again."),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Refresh Meals" })).toBeEnabled();

    // A failed result has no saved revision, so App.tsx does not issue another
    // GET or temporarily replace the existing Meals rows with a loading state.
    expect(fetchReviewAreaSnapshotMock).toHaveBeenCalledTimes(1);
    expect(
      screen.getByRole("heading", { name: "Confirm Sunday's dinner before the grocery run" }),
    ).toBeInTheDocument();
  });

  it("composes text search with status filters and exposes a useful reset", async () => {
    const user = await renderReview();
    const search = screen.getByRole("searchbox", { name: "Search entries" });

    // Area selection participates in the same pipeline as status and text, so
    // this narrows the later search to Mail before the other controls change.
    await user.click(screen.getByRole("button", { name: "Mail3" }));

    // Metadata is searchable, so this query keeps the Google entry even though
    // "Google Accounts" is not part of its title.
    await user.type(search, "Google Accounts");

    expect(
      screen.getByRole("heading", { name: "Confirm the new passkey added to your Google account" }),
    ).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Home" })).not.toBeInTheDocument();

    // No completed fixture matches the same search. The empty state makes the
    // composition visible and provides one action that clears both controls.
    await user.click(screen.getByRole("button", { name: "Done" }));

    expect(screen.getByRole("heading", { name: "No entries match this view." })).toBeInTheDocument();
    expect(screen.queryByText("Recently completed")).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Show all entries" }));

    expect(search).toHaveValue("");
    expect(screen.getByRole("button", { name: "All" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByRole("heading", { name: "Mail" })).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Home" })).not.toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Restore Archive last week's household newsletter" }),
    ).toBeInTheDocument();
  });

  it("updates the open-area summary and moves focus to a sibling before removing a row", async () => {
    const user = await renderReview();
    const moneyCompletion = screen.getByRole("button", {
      name: "Mark Confirm whether the annual subscription renews this month",
    });

    // Keyboard activation reproduces the regression: the default Open filter
    // removes this row immediately after its state changes.
    moneyCompletion.focus();
    await user.keyboard("{Enter}");

    expect(moneyCompletion).not.toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Mark Confirm Sunday's dinner before the grocery run" }),
    ).toHaveFocus();
    expect(
      screen.getByText("12 entries are open across 5 areas. Start at the top or choose an area."),
    ).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "Money" })).not.toBeInTheDocument();
  });

  it("focuses the empty-state action after completing the only visible entry", async () => {
    const user = await renderReview();

    // Selecting Money narrows the main queue to one open row while leaving the
    // rest of the configured areas available in navigation.
    await user.click(screen.getByRole("button", { name: "Money1" }));

    const onlyCompletion = screen.getByRole("button", {
      name: "Mark Confirm whether the annual subscription renews this month",
    });
    onlyCompletion.focus();
    await user.keyboard("{Enter}");

    expect(screen.getByRole("heading", { name: "No entries match this view." })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Show all entries" })).toHaveFocus();
  });

  it("preserves focus when restoring an entry removes it from the Done filter", async () => {
    const user = await renderReview();

    await user.click(screen.getByRole("button", { name: "Done" }));

    const firstRestore = screen.getByRole("button", {
      name: "Restore Archive last week's household newsletter",
    });
    firstRestore.focus();
    await user.keyboard("{Enter}");

    expect(firstRestore).not.toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Restore Log the water heater inspection date" }),
    ).toHaveFocus();
  });

  it("treats mobile navigation as a modal drawer and restores its trigger", async () => {
    const originalViewportWidth = window.innerWidth;
    Object.defineProperty(window, "innerWidth", { configurable: true, value: 320 });

    try {
      const user = await renderReview();
      const openMenu = screen.getByRole("button", { name: "Open menu" });

      expect(openMenu).toHaveAttribute("aria-expanded", "false");
      expect(openMenu).toHaveAttribute("aria-controls", "primary-navigation");

      await user.click(openMenu);

      const closeMenu = screen.getByRole("button", { name: "Close menu" });
      expect(closeMenu).toHaveFocus();
      expect(screen.getByRole("dialog", { name: "Primary navigation" })).toHaveAttribute(
        "aria-modal",
        "true",
      );
      expect(screen.getByRole("main")).toHaveAttribute("inert");
      expect(openMenu.closest("header")).toHaveAttribute("inert");

      // Tab and Shift+Tab cycle through only the drawer's eight controls while
      // the header and review workspace are inert.
      const drawerTabOrder = ["Review queue13", "Mail3", "Home3", "Health2", "Reading2", "Meals2", "Money1"];
      for (const accessibleName of drawerTabOrder) {
        await user.tab();
        expect(screen.getByRole("button", { name: accessibleName })).toHaveFocus();
      }
      await user.tab();
      expect(closeMenu).toHaveFocus();
      await user.tab({ shift: true });
      expect(screen.getByRole("button", { name: "Money1" })).toHaveFocus();

      // Escape closes the modal and returns the keyboard to the same trigger.
      await user.keyboard("{Escape}");

      expect(openMenu).toHaveFocus();
      expect(openMenu).toHaveAttribute("aria-expanded", "false");
      expect(screen.queryByRole("dialog", { name: "Primary navigation" })).not.toBeInTheDocument();

      // The visible Close action follows the same restoration path.
      await user.click(openMenu);
      await user.click(screen.getByRole("button", { name: "Close menu" }));
      expect(openMenu).toHaveFocus();

      // If a device rotates or the window widens into desktop layout, modal
      // semantics and the inert background must be removed automatically.
      await user.click(openMenu);
      Object.defineProperty(window, "innerWidth", { configurable: true, value: 1024 });
      fireEvent(window, new Event("resize"));

      expect(openMenu).toHaveAttribute("aria-expanded", "false");
      expect(screen.getByRole("main")).not.toHaveAttribute("inert");
      expect(screen.queryByRole("dialog", { name: "Primary navigation" })).not.toBeInTheDocument();
    } finally {
      Object.defineProperty(window, "innerWidth", { configurable: true, value: originalViewportWidth });
    }
  });

  it("shows a safe loading error and retries the areas request", async () => {
    fetchReviewAreaSnapshotMock
      .mockRejectedValueOnce(new Error("Unable to load review areas (HTTP 503)."))
      .mockResolvedValueOnce({
        areas: initialAreas,
        updatedAtByArea: {
          health: "2026-07-26T12:15:00Z",
          meals: "2026-07-25T22:45:00Z",
        },
      });

    const user = userEvent.setup();
    render(<ReviewApp />);

    expect(await screen.findByRole("heading", { name: "Unable to load your review." })).toBeInTheDocument();
    expect(screen.getByText("Unable to load review areas (HTTP 503).")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Try again" }));

    expect(await screen.findByRole("heading", { name: "Home" })).toBeInTheDocument();
    expect(fetchReviewAreaSnapshotMock).toHaveBeenCalledTimes(2);
  });
});
