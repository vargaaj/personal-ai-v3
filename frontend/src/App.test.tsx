/**
 * Regression coverage for the stateful Review screen.
 *
 * These component tests exercise the interactions a keyboard user experiences
 * while replacing the real API call with deterministic domain data. They use
 * visible names and roles so changes that weaken the interface's accessibility
 * contract fail alongside changes to loading, filtering, counts, or focus.
 */
import "@testing-library/jest-dom/vitest";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import ReviewApp from "./App";
import {
  fetchAreaContentPrompt,
  fetchReviewAreaSnapshot,
  requestAreaContentUpdate,
  requestWeeklyAreaUpdate,
  saveAreaContentPrompt,
  saveAreaEntryState,
} from "./data/areaContentApi";
import { initialAreas } from "./fixtures/reviewAreas";

// The adapter has its own tests for translating real Go response shapes. These
// component tests replace only the network boundary with resolved domain data,
// keeping interaction coverage deterministic and independent of Supabase.
vi.mock("./data/areaContentApi", () => ({
  fetchAreaContentPrompt: vi.fn(),
  fetchReviewAreaSnapshot: vi.fn(),
  requestAreaContentUpdate: vi.fn(),
  requestWeeklyAreaUpdate: vi.fn(),
  saveAreaContentPrompt: vi.fn(),
  saveAreaEntryState: vi.fn(),
}));

const fetchAreaContentPromptMock = vi.mocked(fetchAreaContentPrompt);
const fetchReviewAreaSnapshotMock = vi.mocked(fetchReviewAreaSnapshot);
const requestAreaContentUpdateMock = vi.mocked(requestAreaContentUpdate);
const requestWeeklyAreaUpdateMock = vi.mocked(requestWeeklyAreaUpdate);
const saveAreaContentPromptMock = vi.mocked(saveAreaContentPrompt);
const saveAreaEntryStateMock = vi.mocked(saveAreaEntryState);

beforeEach(() => {
  fetchReviewAreaSnapshotMock.mockReset();
  fetchReviewAreaSnapshotMock.mockResolvedValue({
    areas: initialAreas,
    updatedAtByArea: {
      health: "2026-07-26T12:15:00Z",
      meals: "2026-07-25T22:45:00Z",
    },
  });
  fetchAreaContentPromptMock.mockReset();
  fetchAreaContentPromptMock.mockResolvedValue(null);
  saveAreaContentPromptMock.mockReset();
  saveAreaContentPromptMock.mockImplementation(async (areaId, prompt) => ({
    areaId,
    contentType:
      areaId === "home"
        ? "maintenance_tasks"
        : areaId === "health"
          ? "weekly_workout_routine"
          : "weekly_meal_recommendations",
    prompt,
  }));
  saveAreaEntryStateMock.mockReset();
  saveAreaEntryStateMock.mockImplementation(async (areaId, entryId, state) => ({
    areaId,
    contentType: areaId === "home" ? "maintenance_tasks" : "weekly_workout_routine",
    entryId,
    state,
    revision: 2,
  }));
  requestAreaContentUpdateMock.mockReset();
  requestAreaContentUpdateMock.mockImplementation(async (areaId) => ({
    areaId,
    contentType:
      areaId === "home"
        ? "maintenance_tasks"
        : areaId === "health"
          ? "weekly_workout_routine"
          : "weekly_meal_recommendations",
    status: "updated",
    revision: 2,
  }));
  requestWeeklyAreaUpdateMock.mockReset();
  requestWeeklyAreaUpdateMock.mockResolvedValue([
    {
      areaId: "home",
      contentType: "maintenance_tasks",
      status: "updated",
      revision: 2,
    },
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
afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.restoreAllMocks();
});

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
  it("shows Health exercises as bullets without changing other area prose", async () => {
    await renderReview();

    // The first fixture workout uses the same middle-dot separator produced by
    // stored Health content. Its accessible list proves each exercise now has a
    // real item boundary instead of appearing as one dense paragraph.
    const healthRegion = screen.getByRole("region", { name: "Health" });
    const workoutList = within(healthRegion).getByRole("list", {
      name: "Exercises for Day 3 — conditioning and mobility",
    });
    expect(within(workoutList).getAllByRole("listitem")).toHaveLength(8);
    expect(within(workoutList).getByText("Kettlebell clean and press")).toBeInTheDocument();
    expect(within(workoutList).getByText("Cossack squats")).toBeInTheDocument();

    // Home still renders its explanatory sentence as a paragraph, confirming
    // that the separator parsing is deliberately limited to Health entries.
    expect(
      screen.getByText(
        "Use the 16 × 25 × 1 filter in the utility-room cabinet. Log the date after replacing it.",
      ).tagName,
    ).toBe("P");
  });

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

  it("refreshes Home with Health and Meals in the bulk action, then reloads the review", async () => {
    let finishUpdate: (
      results: Awaited<ReturnType<typeof requestWeeklyAreaUpdate>>,
    ) => void = () => {};
    requestWeeklyAreaUpdateMock.mockReturnValueOnce(
      new Promise((resolve) => {
        finishUpdate = resolve;
      }),
    );

    const user = await renderReview();
    const updateButton = screen.getByRole("button", { name: "Refresh Home, Health, and Meals" });

    await user.click(updateButton);

    expect(requestWeeklyAreaUpdateMock).toHaveBeenCalledTimes(1);
    expect(screen.getByRole("button", { name: "Updating Home, Health, and Meals…" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Edit Home prompt" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Edit Health prompt" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Edit Meals prompt" })).toBeDisabled();
    expect(
      screen.getByText("Updating Home, Health, and Meals. This can take a minute."),
    ).toBeInTheDocument();

    await act(async () => {
      finishUpdate([
        {
          areaId: "home",
          contentType: "maintenance_tasks",
          status: "updated",
          revision: 3,
        },
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
      await screen.findByText("Home, Health, and Meals updated. The latest content is now loaded."),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Refresh Home, Health, and Meals" })).toBeEnabled();
    expect(fetchReviewAreaSnapshotMock).toHaveBeenCalledTimes(2);
  });

  it("shows which bulk area failed while reloading the areas that actually updated", async () => {
    requestWeeklyAreaUpdateMock.mockResolvedValueOnce([
      {
        areaId: "home",
        contentType: "maintenance_tasks",
        status: "updated",
        revision: 3,
      },
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
    await user.click(screen.getByRole("button", { name: "Refresh Home, Health, and Meals" }));

    expect(
      await screen.findByText("Home and Health updated. Meals could not be updated."),
    ).toBeInTheDocument();
    expect(fetchReviewAreaSnapshotMock).toHaveBeenCalledTimes(2);
  });

  it("reports skipped bulk areas honestly and reloads only the updated sections", async () => {
    requestWeeklyAreaUpdateMock.mockResolvedValueOnce([
      {
        areaId: "home",
        contentType: "maintenance_tasks",
        status: "skipped",
      },
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

    const user = await renderReview();
    await user.click(screen.getByRole("button", { name: "Refresh Home, Health, and Meals" }));

    expect(
      await screen.findByText(
        "Health and Meals updated. Home was not refreshed; an update may already be in progress.",
      ),
    ).toBeInTheDocument();
    expect(fetchReviewAreaSnapshotMock).toHaveBeenCalledTimes(2);
  });

  it("does not reload or claim latest content when every bulk area is skipped", async () => {
    requestWeeklyAreaUpdateMock.mockResolvedValueOnce([
      { areaId: "home", contentType: "maintenance_tasks", status: "skipped" },
      { areaId: "health", contentType: "weekly_workout_routine", status: "skipped" },
      { areaId: "meals", contentType: "weekly_meal_recommendations", status: "skipped" },
    ]);

    const user = await renderReview();
    await user.click(screen.getByRole("button", { name: "Refresh Home, Health, and Meals" }));

    expect(
      await screen.findByText(
        "Home, Health, and Meals were not refreshed; an update may already be in progress.",
      ),
    ).toBeInTheDocument();
    expect(fetchReviewAreaSnapshotMock).toHaveBeenCalledTimes(1);
  });

  it("reconciles a skipped bulk result after a delay without changing its honest message", async () => {
    fetchReviewAreaSnapshotMock
      .mockResolvedValueOnce({
        areas: initialAreas,
        updatedAtByArea: { home: "2026-07-26T09:00:00Z" },
      })
      .mockResolvedValueOnce({
        areas: initialAreas,
        updatedAtByArea: { home: "2026-07-26T09:05:00Z" },
      });
    requestWeeklyAreaUpdateMock.mockResolvedValueOnce([
      { areaId: "home", contentType: "maintenance_tasks", status: "skipped" },
      { areaId: "health", contentType: "weekly_workout_routine", status: "failed" },
      { areaId: "meals", contentType: "weekly_meal_recommendations", status: "failed" },
    ]);

    await renderReview();
    vi.useFakeTimers();
    fireEvent.click(screen.getByRole("button", { name: "Refresh Home, Health, and Meals" }));
    await act(async () => Promise.resolve());

    expect(fetchReviewAreaSnapshotMock).toHaveBeenCalledTimes(1);
    await act(async () => vi.advanceTimersByTimeAsync(1_500));

    expect(fetchReviewAreaSnapshotMock).toHaveBeenCalledTimes(2);
    expect(screen.getByText(
      "Home was not refreshed; an update may already be in progress. Health and Meals could not be updated.",
    )).toBeInTheDocument();
    expect(screen.queryByText(/latest content is now loaded/i)).not.toBeInTheDocument();
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
    expect(screen.getByRole("button", { name: "Edit Home prompt" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Edit Health prompt" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Edit Meals prompt" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Refresh Home" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Refresh Meals" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Refresh Home, Health, and Meals" })).toBeDisabled();
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
      await screen.findByText("Health updated. The latest content is now loaded."),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Refresh Health" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "Refresh Home" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "Refresh Meals" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "Refresh Home, Health, and Meals" })).toBeEnabled();
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

  it("does not reload or claim success when an individual refresh is skipped", async () => {
    requestAreaContentUpdateMock.mockResolvedValueOnce({
      areaId: "home",
      contentType: "maintenance_tasks",
      status: "skipped",
    });

    const user = await renderReview();
    await user.click(screen.getByRole("button", { name: "Refresh Home" }));

    expect(
      await screen.findByText("Home was not refreshed; an update may already be in progress."),
    ).toBeInTheDocument();
    expect(fetchReviewAreaSnapshotMock).toHaveBeenCalledTimes(1);
  });

  it("bounds delayed reconciliation after an individual skipped result", async () => {
    requestAreaContentUpdateMock.mockResolvedValueOnce({
      areaId: "health",
      contentType: "weekly_workout_routine",
      status: "skipped",
    });

    await renderReview();
    vi.useFakeTimers();
    fireEvent.click(screen.getByRole("button", { name: "Refresh Health" }));
    await act(async () => Promise.resolve());

    // The unchanged Health timestamp makes all three bounded checks run. No
    // fourth request appears even when considerably more virtual time passes.
    await act(async () => vi.advanceTimersByTimeAsync(20_000));
    expect(fetchReviewAreaSnapshotMock).toHaveBeenCalledTimes(4);
    await act(async () => vi.advanceTimersByTimeAsync(20_000));
    expect(fetchReviewAreaSnapshotMock).toHaveBeenCalledTimes(4);
    expect(screen.getByText(
      "Health was not refreshed; an update may already be in progress.",
    )).toBeInTheDocument();
  });

  it("cleans up a pending skipped-result reconciliation when the screen unmounts", async () => {
    requestAreaContentUpdateMock.mockResolvedValueOnce({
      areaId: "home",
      contentType: "maintenance_tasks",
      status: "skipped",
    });
    const { unmount } = render(<ReviewApp />);
    await screen.findByRole("heading", { name: "Mail" });
    vi.useFakeTimers();
    fireEvent.click(screen.getByRole("button", { name: "Refresh Home" }));
    await act(async () => Promise.resolve());

    unmount();
    await vi.advanceTimersByTimeAsync(20_000);
    expect(fetchReviewAreaSnapshotMock).toHaveBeenCalledTimes(1);
  });

  it("exposes an accessible Edit prompt control for every visible database-backed area", async () => {
    await renderReview();

    // Fixture-only Mail and Money deliberately have no backend prompt route.
    // The three persisted Review areas each expose a uniquely named button.
    expect(screen.getByRole("button", { name: "Edit Home prompt" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "Edit Health prompt" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "Edit Meals prompt" })).toBeEnabled();
    expect(screen.queryByRole("button", { name: "Edit Mail prompt" })).not.toBeInTheDocument();
  });

  it("loads an existing prompt into a keyboard-accessible dialog and restores focus on cancel", async () => {
    fetchAreaContentPromptMock.mockResolvedValueOnce({
      areaId: "home",
      contentType: "maintenance_tasks",
      prompt: "Keep the list practical and seasonal.",
    });

    const user = await renderReview();
    const editHome = screen.getByRole("button", { name: "Edit Home prompt" });
    await user.click(editHome);

    const dialog = await screen.findByRole("dialog", { name: "Edit Home prompt" });
    const textarea = await screen.findByDisplayValue("Keep the list practical and seasonal.");
    expect(dialog).toHaveAttribute("aria-modal", "true");
    expect(textarea).toHaveValue("Keep the list practical and seasonal.");
    expect(textarea).toHaveFocus();
    expect(screen.getByRole("main")).toHaveAttribute("inert");

    // The visible Cancel action returns focus to its opener.
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    expect(screen.queryByRole("dialog", { name: "Edit Home prompt" })).not.toBeInTheDocument();
    expect(editHome).toHaveFocus();

    // Escape follows the same keyboard-accessible Cancel path.
    await user.click(editHome);
    await screen.findByRole("dialog", { name: "Edit Home prompt" });
    await user.keyboard("{Escape}");
    expect(editHome).toHaveFocus();
  });

  it("shows a blank create editor for a missing prompt and saves without refreshing content", async () => {
    fetchAreaContentPromptMock.mockResolvedValueOnce(null);
    const user = await renderReview();

    await user.click(screen.getByRole("button", { name: "Edit Meals prompt" }));
    const textarea = await screen.findByRole("textbox", { name: "Prompt instructions" });
    await waitFor(() => expect(textarea).toBeEnabled());
    const saveButton = screen.getByRole("button", { name: "Save prompt" });

    expect(textarea).toHaveValue("");
    expect(saveButton).toBeDisabled();

    await user.type(textarea, "Use approachable meals with leftovers.");
    await user.click(saveButton);

    expect(saveAreaContentPromptMock).toHaveBeenCalledWith(
      "meals",
      "Use approachable meals with leftovers.",
      expect.any(AbortSignal),
    );
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    // Saving prompt instructions must not invoke a second area snapshot GET.
    expect(fetchReviewAreaSnapshotMock).toHaveBeenCalledTimes(1);
    expect(requestAreaContentUpdateMock).not.toHaveBeenCalled();
  });

  it("keeps entered prompt text visible after a save failure", async () => {
    fetchAreaContentPromptMock.mockResolvedValueOnce(null);
    saveAreaContentPromptMock.mockRejectedValueOnce(new Error("Unable to save health prompt (HTTP 500)."));
    const user = await renderReview();

    await user.click(screen.getByRole("button", { name: "Edit Health prompt" }));
    const textarea = await screen.findByRole("textbox", { name: "Prompt instructions" });
    await waitFor(() => expect(textarea).toBeEnabled());
    await user.type(textarea, "Keep sessions low impact.");
    await user.click(screen.getByRole("button", { name: "Save prompt" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Unable to save health prompt (HTTP 500). Your entered prompt is still available to edit.",
    );
    expect(textarea).toHaveValue("Keep sessions low impact.");
    expect(fetchReviewAreaSnapshotMock).toHaveBeenCalledTimes(1);
  });

  it("keeps Save focusable while prompt Cancel and close remain available during a PUT", async () => {
    fetchAreaContentPromptMock.mockResolvedValueOnce(null);
    let finishSave: () => void = () => {};
    saveAreaContentPromptMock.mockReturnValueOnce(
      new Promise((resolve) => {
        finishSave = () => resolve({
          areaId: "home",
          contentType: "maintenance_tasks",
          prompt: "Keep a seasonal checklist.",
        });
      }),
    );

    const user = await renderReview();
    await user.click(screen.getByRole("button", { name: "Edit Home prompt" }));
    const textarea = await screen.findByRole("textbox", { name: "Prompt instructions" });
    await waitFor(() => expect(textarea).toBeEnabled());
    await user.type(textarea, "Keep a seasonal checklist.");

    const saveButton = screen.getByRole("button", { name: "Save prompt" });
    await user.click(saveButton);

    const busySaveButton = screen.getByRole("button", { name: "Saving prompt…" });
    expect(busySaveButton).toHaveAttribute("aria-disabled", "true");
    expect(busySaveButton).toBeEnabled();
    expect(busySaveButton).toHaveFocus();
    expect(textarea).toBeDisabled();
    expect(screen.getByRole("button", { name: "Cancel" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "Cancel editing prompt" })).toBeEnabled();

    // Tab wraps from the last form control to the dialog's close icon instead
    // of escaping to the inert Review screen behind it.
    await user.tab();
    expect(screen.getByRole("button", { name: "Cancel editing prompt" })).toHaveFocus();

    await act(async () => finishSave());
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it.each([
    { closePath: "Cancel", accessibleName: "Cancel" },
    { closePath: "close", accessibleName: "Cancel editing prompt" },
    { closePath: "Escape", accessibleName: null },
  ])("aborts a stalled prompt PUT through $closePath without announcing an error", async ({ accessibleName }) => {
    let saveSignal: AbortSignal | undefined;
    saveAreaContentPromptMock.mockImplementationOnce((_areaId, _prompt, signal) => {
      saveSignal = signal;
      return new Promise((_resolve, reject) => {
        signal?.addEventListener("abort", () => {
          const abortError = new Error("The prompt request was canceled.");
          abortError.name = "AbortError";
          reject(abortError);
        });
      });
    });

    const user = await renderReview();
    const editHome = screen.getByRole("button", { name: "Edit Home prompt" });
    await user.click(editHome);
    const textarea = await screen.findByRole("textbox", { name: "Prompt instructions" });
    await waitFor(() => expect(textarea).toBeEnabled());
    await user.type(textarea, "Keep the checklist seasonal.");
    await user.click(screen.getByRole("button", { name: "Save prompt" }));

    expect(saveSignal?.aborted).toBe(false);
    if (accessibleName) {
      await user.click(screen.getByRole("button", { name: accessibleName }));
    } else {
      await user.keyboard("{Escape}");
    }

    expect(saveSignal?.aborted).toBe(true);
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(editHome).toHaveFocus();
  });

  it("locks prompt text after a GET failure until Retry succeeds, while Cancel remains available", async () => {
    fetchAreaContentPromptMock
      .mockRejectedValueOnce(new Error("Unable to load home prompt (HTTP 500)."))
      .mockResolvedValueOnce(null);
    const user = await renderReview();
    const editHome = screen.getByRole("button", { name: "Edit Home prompt" });

    await user.click(editHome);
    const textarea = await screen.findByRole("textbox", { name: "Prompt instructions" });
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Unable to load home prompt (HTTP 500). Try again or cancel without making changes.",
    );
    expect(textarea).toBeDisabled();

    await user.click(screen.getByRole("button", { name: "Try again" }));
    await waitFor(() => expect(textarea).toBeEnabled());
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    expect(editHome).toHaveFocus();
  });

  it("optimistically saves a Home checkbox and blocks a duplicate toggle while PUT is pending", async () => {
    let finishSave: () => void = () => {};
    saveAreaEntryStateMock.mockReturnValueOnce(
      new Promise((resolve) => {
        finishSave = () => resolve({
          areaId: "home",
          contentType: "maintenance_tasks",
          entryId: "replace-hvac-filter",
          state: "done",
          revision: 3,
        });
      }),
    );

    const user = await renderReview();
    await user.click(screen.getByRole("button", { name: "All" }));
    await user.click(screen.getByRole("button", { name: "Mark Replace the HVAC filter" }));

    const optimisticControl = screen.getByRole("button", { name: "Restore Replace the HVAC filter" });
    expect(optimisticControl).toHaveAttribute("aria-pressed", "true");
    expect(optimisticControl).toHaveAttribute("aria-busy", "true");
    expect(optimisticControl).toBeDisabled();
    expect(saveAreaEntryStateMock).toHaveBeenCalledWith(
      "home",
      "replace-hvac-filter",
      "done",
    );

    // A second browser click cannot reach the handler while the native button
    // is disabled, so only the original write remains in flight.
    await user.click(optimisticControl);
    expect(saveAreaEntryStateMock).toHaveBeenCalledTimes(1);

    await act(async () => finishSave());
    expect(optimisticControl).toBeEnabled();
    expect(optimisticControl).not.toHaveAttribute("aria-busy", "true");
  });

  it("rolls back a failed Health checkbox save and announces a safe error", async () => {
    let failSave: () => void = () => {};
    saveAreaEntryStateMock.mockReturnValueOnce(
      new Promise((_resolve, reject) => {
        failSave = () => reject(new Error("private database detail"));
      }),
    );

    const user = await renderReview();
    await user.click(screen.getByRole("button", { name: "All" }));
    await user.click(screen.getByRole("button", { name: "Mark Day 3 — conditioning and mobility" }));
    expect(
      screen.getByRole("button", { name: "Restore Day 3 — conditioning and mobility" }),
    ).toBeDisabled();

    await act(async () => failSave());

    expect(
      screen.getByRole("button", { name: "Mark Day 3 — conditioning and mobility" }),
    ).toBeEnabled();
    expect(screen.getByText(
      "Could not save Day 3 — conditioning and mobility. Its previous state was restored.",
    )).toBeInTheDocument();
    expect(screen.queryByText(/private database detail/i)).not.toBeInTheDocument();
  });

  it("keeps Meals and fixture-only checkbox changes in browser memory", async () => {
    const user = await renderReview();
    await user.click(screen.getByRole("button", { name: "All" }));

    await user.click(screen.getByRole("button", {
      name: "Mark Confirm Sunday's dinner before the grocery run",
    }));
    await user.click(screen.getByRole("button", {
      name: "Mark Confirm the new passkey added to your Google account",
    }));

    expect(saveAreaEntryStateMock).not.toHaveBeenCalled();
    expect(screen.getByRole("button", {
      name: "Restore Confirm Sunday's dinner before the grocery run",
    })).toBeEnabled();
    expect(screen.getByRole("button", {
      name: "Restore Confirm the new passkey added to your Google account",
    })).toBeEnabled();
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
