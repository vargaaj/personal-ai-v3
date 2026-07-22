/**
 * Regression coverage for the stateful Review screen.
 *
 * These component tests exercise the interactions a keyboard user experiences
 * without depending on the unfinished backend boundary. They intentionally use
 * visible names and roles so changes that weaken the interface's accessibility
 * contract fail alongside changes to filtering, counts, or focus management.
 */
import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";
import ReviewApp from "./App";

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
function renderReview() {
  const user = userEvent.setup();
  render(<ReviewApp />);
  return user;
}

describe("ReviewApp", () => {
  it("composes text search with status filters and exposes a useful reset", async () => {
    const user = renderReview();
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
    const user = renderReview();
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
    const user = renderReview();

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
    const user = renderReview();

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
      const user = renderReview();
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
});
