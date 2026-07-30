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
  requestAreaContentUpdate,
  requestWeeklyAreaUpdate,
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

  it("returns translated areas and their timestamps from the Go endpoint", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: vi.fn().mockResolvedValue([
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
      areas: [{ id: "health" }],
      updatedAtByArea: {
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

  it("requests a manual weekly update and translates its safe results", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: vi.fn().mockResolvedValue([
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
