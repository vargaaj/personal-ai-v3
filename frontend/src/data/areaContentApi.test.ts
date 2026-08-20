/**
 * Focused tests for translating Go API responses into Review screen data.
 * These tests use plain JSON values and a mocked fetch response, so they do not
 * require Supabase, the Go server, or a rendered React component.
 */
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  adaptAreaContentSnapshot,
  adaptAreaContents,
  fetchReviewAreaSnapshot,
  fetchReviewAreas,
  fetchAreaContentPrompt,
  requestAreaContentUpdate,
  requestWeeklyAreaUpdate,
  saveAreaContentPrompt,
  saveAreaEntryState,
} from "./areaContentApi";

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("areaContentApi", () => {
  it("maps entry documents and weekly meal recommendations into review areas", () => {
    const areas = adaptAreaContents([
      {
        area_id: "health",
        content_type: "weekly_workout_routine",
        revision: 2,
        updated_at: "2026-07-22T12:00:00Z",
        content: {
          last_updated: "2026-07-22",
          entries: [
            {
              id: "monday-strength",
              title: "Monday strength workout",
              details: "Goblet squats and rows",
              state: "open",
              metadata: [{ label: "Duration", value: "35 min" }],
            },
          ],
        },
      },
      {
        area_id: "meals",
        content_type: "weekly_meal_recommendations",
        revision: 1,
        updated_at: "2026-07-22T12:00:00Z",
        content: {
          week_starting: "2026-07-20",
          recommendations: [
            {
              day: "Monday",
              title: "Lemon-herb chicken",
              description: "Serve with roasted potatoes.",
              tags: ["chicken", "sheet pan"],
            },
          ],
        },
      },
      // The frontend does not yet define a Mail database document. Ignoring it
      // is intentional and keeps future backend data from creating a broken UI.
      {
        area_id: "mail",
        content_type: "inbox_summary",
        revision: 1,
        updated_at: "2026-07-22T12:00:00Z",
        content: { entries: [] },
      },
    ]);

    expect(areas.map((area) => area.id)).toEqual(["health", "meals"]);
    expect(areas[0]?.entries[0]).toMatchObject({
      id: "monday-strength",
      title: "Monday strength workout",
      state: "open",
    });
    expect(areas[1]?.entries[0]).toEqual({
      id: "meal-monday",
      title: "Lemon-herb chicken",
      details: "Serve with roasted potatoes.",
      state: "open",
      metadata: [
        { label: "Day", value: "Monday" },
        { label: "Tags", value: "chicken, sheet pan" },
      ],
    });
  });

  it("retains an independent stored update time for each review area", () => {
    const snapshot = adaptAreaContentSnapshot([
      {
        area_id: "health",
        content_type: "weekly_workout_routine",
        revision: 2,
        updated_at: "2026-07-22T12:00:00Z",
        content: {
          last_updated: "2026-07-22",
          entries: [],
        },
      },
      {
        area_id: "meals",
        content_type: "weekly_meal_recommendations",
        revision: 3,
        updated_at: "2026-07-22T09:30:00-04:00",
        content: {
          week_starting: "2026-07-20",
          recommendations: [],
        },
      },
    ]);

    expect(snapshot.areas.map((area) => area.id)).toEqual(["health", "meals"]);
    expect(snapshot.updatedAtByArea).toEqual({
      health: "2026-07-22T12:00:00Z",
      meals: "2026-07-22T09:30:00-04:00",
    });
  });

  it("ignores the stored Reading document while that area is retired", () => {
    const snapshot = adaptAreaContentSnapshot([
      {
        area_id: "reading",
        content_type: "reading_queue",
        revision: 4,
        updated_at: "2026-07-22T13:15:00Z",
        content: {
          entries: [
            {
              id: "saved-article",
              title: "Read a saved article",
              state: "open",
              metadata: [],
            },
          ],
        },
      },
    ]);

    // Go may continue returning the existing database row. With no frontend
    // definition, it creates neither a visible section nor a stale timestamp
    // that could make the retired area look active elsewhere in the interface.
    expect(snapshot).toEqual({
      areas: [],
      updatedAtByArea: {},
    });
  });

  it("returns translated areas and their timestamps from the Go endpoint", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: vi.fn().mockResolvedValue([
        {
          area_id: "home",
          content_type: "maintenance_tasks",
          revision: 2,
          updated_at: "2026-07-26T09:45:00Z",
          content: {
            entries: [],
          },
        },
        {
          area_id: "health",
          content_type: "weekly_workout_routine",
          revision: 2,
          updated_at: "2026-07-22T12:00:00Z",
          content: {
            last_updated: "2026-07-22",
            entries: [],
          },
        },
      ]),
    });
    vi.stubGlobal("fetch", fetchMock);

    await expect(fetchReviewAreaSnapshot()).resolves.toMatchObject({
      areas: [{ id: "home" }, { id: "health" }],
      updatedAtByArea: {
        home: "2026-07-26T09:45:00Z",
        health: "2026-07-22T12:00:00Z",
      },
    });
    expect(fetchMock).toHaveBeenCalledWith("/api/areas", {
      headers: { Accept: "application/json" },
      signal: undefined,
    });
  });

  it("requests the Go endpoint and reports non-successful responses", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: false,
      status: 503,
      json: vi.fn(),
    });
    vi.stubGlobal("fetch", fetchMock);

    await expect(fetchReviewAreas()).rejects.toThrow("Unable to load review areas (HTTP 503).");
    expect(fetchMock).toHaveBeenCalledWith("/api/areas", {
      headers: { Accept: "application/json" },
      signal: undefined,
    });
  });

  it("loads an existing Home prompt through its encoded database route", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: vi.fn().mockResolvedValue({
        area_id: "home",
        content_type: "maintenance_tasks",
        prompt: "Keep maintenance tasks practical.",
      }),
    });
    vi.stubGlobal("fetch", fetchMock);

    await expect(fetchAreaContentPrompt("home")).resolves.toEqual({
      areaId: "home",
      contentType: "maintenance_tasks",
      prompt: "Keep maintenance tasks practical.",
    });
    expect(fetchMock).toHaveBeenCalledWith("/api/areas/home/maintenance_tasks/prompt", {
      headers: { Accept: "application/json" },
      signal: undefined,
    });
  });

  it("treats a missing enabled prompt as a blank create state", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: false,
      status: 404,
      json: vi.fn(),
    });
    vi.stubGlobal("fetch", fetchMock);

    await expect(fetchAreaContentPrompt("meals")).resolves.toBeNull();
  });

  it("saves a prompt without requesting any content regeneration route", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: vi.fn().mockResolvedValue({
        area_id: "health",
        content_type: "weekly_workout_routine",
        prompt: "Prioritize low-impact strength sessions.",
      }),
    });
    vi.stubGlobal("fetch", fetchMock);

    await expect(
      saveAreaContentPrompt("health", "Prioritize low-impact strength sessions."),
    ).resolves.toEqual({
      areaId: "health",
      contentType: "weekly_workout_routine",
      prompt: "Prioritize low-impact strength sessions.",
    });
    expect(fetchMock).toHaveBeenCalledWith("/api/areas/health/weekly_workout_routine/prompt", {
      method: "PUT",
      headers: {
        Accept: "application/json",
        "Content-Type": "application/json",
      },
      body: JSON.stringify({ prompt: "Prioritize low-impact strength sessions." }),
      signal: undefined,
    });
    expect(fetchMock.mock.calls[0]?.[0]).not.toContain("/update");
  });

  it("rejects blank prompt text before it sends a request", async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);

    await expect(saveAreaContentPrompt("home", " \n ")).rejects.toThrow("A prompt cannot be blank.");
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("reports a prompt save failure with the existing HTTP error convention", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: false,
      status: 500,
      json: vi.fn(),
    });
    vi.stubGlobal("fetch", fetchMock);

    await expect(saveAreaContentPrompt("meals", "Use seasonal ingredients.")).rejects.toThrow(
      "Unable to save meals prompt (HTTP 500).",
    );
  });

  it("saves and validates one Home entry state through its encoded route", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: vi.fn().mockResolvedValue({
        area_id: "home",
        content_type: "maintenance_tasks",
        entry_id: "filter / upstairs",
        state: "done",
        revision: 7,
      }),
    });
    vi.stubGlobal("fetch", fetchMock);

    await expect(saveAreaEntryState("home", "filter / upstairs", "done")).resolves.toEqual({
      areaId: "home",
      contentType: "maintenance_tasks",
      entryId: "filter / upstairs",
      state: "done",
      revision: 7,
    });
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/areas/home/maintenance_tasks/entries/filter%20%2F%20upstairs/state",
      {
        method: "PUT",
        headers: {
          Accept: "application/json",
          "Content-Type": "application/json",
        },
        body: JSON.stringify({ state: "done" }),
        signal: undefined,
      },
    );
  });

  it("rejects an entry-state response that does not confirm the requested entry", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: vi.fn().mockResolvedValue({
        area_id: "health",
        content_type: "weekly_workout_routine",
        entry_id: "tuesday-cardio",
        state: "done",
        revision: 4,
      }),
    });
    vi.stubGlobal("fetch", fetchMock);

    await expect(saveAreaEntryState("health", "monday-strength", "done")).rejects.toThrow(
      "The health entry state API returned an unexpected entry.",
    );
  });

  it("requires a revision in a successful entry-state response", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: vi.fn().mockResolvedValue({
        area_id: "health",
        content_type: "weekly_workout_routine",
        entry_id: "monday-strength",
        state: "open",
      }),
    });
    vi.stubGlobal("fetch", fetchMock);

    await expect(saveAreaEntryState("health", "monday-strength", "open")).rejects.toThrow(
      "Entry state field revision must be an integer.",
    );
  });

  it("requests a manual weekly update and translates its safe results", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: vi.fn().mockResolvedValue([
        {
          area_id: "home",
          content_type: "maintenance_tasks",
          status: "updated",
          revision: 2,
          updated_at: "2026-07-26T09:45:00Z",
        },
        {
          area_id: "health",
          content_type: "weekly_workout_routine",
          status: "updated",
          revision: 4,
          updated_at: "2026-07-26T10:00:00Z",
        },
        {
          area_id: "meals",
          content_type: "weekly_meal_recommendations",
          status: "failed",
        },
      ]),
    });
    vi.stubGlobal("fetch", fetchMock);

    await expect(requestWeeklyAreaUpdate()).resolves.toEqual([
      {
        areaId: "home",
        contentType: "maintenance_tasks",
        status: "updated",
        revision: 2,
        updatedAt: "2026-07-26T09:45:00Z",
      },
      {
        areaId: "health",
        contentType: "weekly_workout_routine",
        status: "updated",
        revision: 4,
        updatedAt: "2026-07-26T10:00:00Z",
      },
      {
        areaId: "meals",
        contentType: "weekly_meal_recommendations",
        status: "failed",
      },
    ]);
    expect(fetchMock).toHaveBeenCalledWith("/api/areas/weekly-update", {
      method: "POST",
      headers: { Accept: "application/json" },
      signal: undefined,
    });
  });

  it("rejects a bulk refresh response that omits one expected area", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: vi.fn().mockResolvedValue([
        { area_id: "home", content_type: "maintenance_tasks", status: "updated" },
        { area_id: "health", content_type: "weekly_workout_routine", status: "updated" },
      ]),
    });
    vi.stubGlobal("fetch", fetchMock);

    await expect(requestWeeklyAreaUpdate()).rejects.toThrow(
      "The bulk update API returned an unexpected result set.",
    );
  });

  it("rejects a bulk refresh response with a duplicate target", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: vi.fn().mockResolvedValue([
        { area_id: "home", content_type: "maintenance_tasks", status: "updated" },
        { area_id: "home", content_type: "maintenance_tasks", status: "skipped" },
        { area_id: "meals", content_type: "weekly_meal_recommendations", status: "updated" },
      ]),
    });
    vi.stubGlobal("fetch", fetchMock);

    await expect(requestWeeklyAreaUpdate()).rejects.toThrow(
      "The bulk update API returned an unexpected result set.",
    );
  });

  it("rejects a bulk refresh response with an unexpected area/content pair", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: vi.fn().mockResolvedValue([
        { area_id: "home", content_type: "maintenance_tasks", status: "updated" },
        { area_id: "health", content_type: "weekly_workout_routine", status: "updated" },
        { area_id: "mail", content_type: "inbox_summary", status: "updated" },
      ]),
    });
    vi.stubGlobal("fetch", fetchMock);

    await expect(requestWeeklyAreaUpdate()).rejects.toThrow(
      "The bulk update API returned an unexpected result set.",
    );
  });

  it("reports a failed manual weekly-update request", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: false,
      status: 500,
      json: vi.fn(),
    });
    vi.stubGlobal("fetch", fetchMock);

    await expect(requestWeeklyAreaUpdate()).rejects.toThrow(
      "Unable to update weekly plans (HTTP 500).",
    );
  });

  it("requests and validates one Meals update through its area-specific route", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: vi.fn().mockResolvedValue([
        {
          area_id: "meals",
          content_type: "weekly_meal_recommendations",
          status: "updated",
          revision: 5,
          updated_at: "2026-07-26T11:00:00Z",
        },
      ]),
    });
    vi.stubGlobal("fetch", fetchMock);

    await expect(requestAreaContentUpdate("meals")).resolves.toEqual({
      areaId: "meals",
      contentType: "weekly_meal_recommendations",
      status: "updated",
      revision: 5,
      updatedAt: "2026-07-26T11:00:00Z",
    });
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/areas/meals/weekly_meal_recommendations/update",
      {
        method: "POST",
        headers: { Accept: "application/json" },
        signal: undefined,
      },
    );
  });

  it("requests Home through the same section-refresh route as Health and Meals", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: vi.fn().mockResolvedValue([
        {
          area_id: "home",
          content_type: "maintenance_tasks",
          status: "updated",
          revision: 6,
          updated_at: "2026-07-26T13:00:00Z",
        },
      ]),
    });
    vi.stubGlobal("fetch", fetchMock);

    await expect(requestAreaContentUpdate("home")).resolves.toMatchObject({
      areaId: "home",
      contentType: "maintenance_tasks",
      status: "updated",
    });
    expect(fetchMock).toHaveBeenCalledWith("/api/areas/home/maintenance_tasks/update", {
      method: "POST",
      headers: { Accept: "application/json" },
      signal: undefined,
    });
  });

  it("rejects an individual update response for the wrong area", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: vi.fn().mockResolvedValue([
        {
          area_id: "meals",
          content_type: "weekly_meal_recommendations",
          status: "updated",
          revision: 5,
        },
      ]),
    });
    vi.stubGlobal("fetch", fetchMock);

    await expect(requestAreaContentUpdate("health")).rejects.toThrow(
      "The health update API returned an unexpected result.",
    );
  });
});
