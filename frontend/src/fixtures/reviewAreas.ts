/**
 * Realistic in-memory data for the first frontend-only Review experience.
 *
 * The fixture intentionally covers urgent metadata, long text, completed
 * entries, external links, and different area sizes so the interface can be
 * designed and tested before a permanent backend contract exists. Components
 * should consume this through the same domain shape a later repository returns.
 */
import type { ReviewArea } from "../domain/review";

export const initialAreas: ReviewArea[] = [
  // Mail appears first because its entries are time-sensitive decisions rather
  // than a chronological copy of an email inbox.
  {
    id: "mail",
    name: "Mail",
    description: "Messages that need a decision, reply, or closer look.",
    accent: "saffron",
    entries: [
      // The attention flag exercises the due/urgent marker without relying on
      // color alone; the visible "Received Today" text carries the meaning.
      {
        id: "security-alert",
        title: "Confirm the new passkey added to your Google account",
        details:
          "Google reported a new passkey. Check the device and remove it if you do not recognize the change.",
        state: "open",
        metadata: [
          { label: "From", value: "Google Accounts" },
          { label: "Received", value: "Today", attention: true },
        ],
      },
      // This deliberately long title and explanation verify wrapping in the
      // highest-priority section of both desktop and mobile layouts.
      {
        id: "schwab-authorization",
        title: "Review the change to your investment account authorization",
        details:
          "Schwab ended the investment advisor authorization on account ending in 739 and moved the account to individual investor pricing.",
        state: "open",
        metadata: [
          { label: "From", value: "Charles Schwab" },
          { label: "Received", value: "Today" },
        ],
      },
      {
        id: "shipping-delay",
        title: "Decide whether the delayed Pupsicle refill order can wait",
        details: "Amazon has not provided a new delivery date for order 113-9499343-8552251.",
        state: "open",
        metadata: [
          { label: "From", value: "Amazon" },
          { label: "Received", value: "Yesterday" },
        ],
      },
      // A completed entry keeps the Done filter populated from the first
      // render and tests the restored-entry interaction.
      {
        id: "newsletter-archive",
        title: "Archive last week's household newsletter",
        state: "done",
        metadata: [{ label: "Completed", value: "8:12 AM" }],
      },
    ],
  },
  // Home provides short and long maintenance work plus a visible due date.
  {
    id: "home",
    name: "Home",
    description: "Maintenance and records that keep the house running quietly.",
    accent: "juniper",
    entries: [
      // The multiplication symbols and dimensions are intentional hostile-ish
      // text for typography and line-wrapping checks.
      {
        id: "replace-hvac-filter",
        title: "Replace the HVAC filter",
        details: "Use the 16 × 25 × 1 filter in the utility-room cabinet. Log the date after replacing it.",
        state: "open",
        metadata: [
          { label: "Category", value: "Maintenance" },
          { label: "Due", value: "Friday", attention: true },
        ],
      },
      {
        id: "smoke-detectors",
        title: "Check the smoke detector batteries",
        details: "Test the upstairs hallway and basement units first; both were last checked in January.",
        state: "open",
        metadata: [{ label: "Category", value: "Safety" }],
      },
      {
        id: "warranty-pdfs",
        title: "Attach appliance warranty PDFs to the home folder",
        state: "open",
        metadata: [{ label: "Category", value: "Records" }],
      },
      {
        id: "water-heater",
        title: "Log the water heater inspection date",
        state: "done",
        metadata: [{ label: "Completed", value: "Yesterday" }],
      },
    ],
  },
  // Health carries the densest prose so workout instructions shape the design
  // instead of being treated as an afterthought beneath short task titles.
  {
    id: "health",
    name: "Health",
    description: "The next physical routines, kept specific and easy to start.",
    accent: "iris",
    entries: [
      // Dot-separated exercises exercise long inline content without forcing
      // the frontend to understand workout-specific schema fields.
      {
        id: "day-3-conditioning",
        title: "Day 3 — conditioning and mobility",
        details:
          "Kettlebell clean and press · farmer carries · world's greatest stretch · cat-cow · bird-dog · mountain climbers · bear crawls · Cossack squats",
        state: "open",
        metadata: [
          { label: "Plan", value: "Workout routine" },
          { label: "Duration", value: "35–45 min" },
        ],
      },
      {
        id: "day-5-functional",
        title: "Day 5 — functional strength and core",
        details:
          "Goblet lunges · kettlebell snatch · mountain climbers · dead bug · squat hold · plank taps · superman",
        state: "open",
        metadata: [
          { label: "Plan", value: "Workout routine" },
          { label: "Duration", value: "30–40 min" },
        ],
      },
      {
        id: "upper-body",
        title: "Day 1 — upper body strength",
        state: "done",
        metadata: [{ label: "Completed", value: "Monday" }],
      },
    ],
  },
  // Reading includes outbound links so the row action and accessible link
  // labels are exercised by the mock data.
  {
    id: "reading",
    name: "Reading",
    description: "News and research worth keeping, without becoming another feed.",
    accent: "iris",
    entries: [
      // This external destination opens in a separate tab and therefore must
      // be rendered with rel="noreferrer" by the component.
      {
        id: "agentic-workflows",
        title: "Read the deep dive on agentic software workflows",
        details:
          "A long-form analysis of how coding agents are changing planning, review, and the shape of development cycles.",
        href: "https://www.youtube.com/results?search_query=agentic+workflows+ai",
        state: "open",
        metadata: [
          { label: "Source", value: "Watch later" },
          { label: "Saved", value: "Yesterday" },
        ],
      },
      {
        id: "gemini-reasoning",
        title: "Skim the Gemini reasoning update",
        details: "Focus on the changes that affect complex developer and research workflows.",
        href: "https://blog.google/technology/ai/",
        state: "open",
        metadata: [
          { label: "Source", value: "Google AI" },
          { label: "Published", value: "Jun 14" },
        ],
      },
    ],
  },
  // Meals demonstrates scheduled personal planning using ordinary metadata
  // rather than creating a one-off meal-plan component.
  {
    id: "meals",
    name: "Meals",
    description: "Small decisions that make the rest of the week easier.",
    accent: "saffron",
    entries: [
      {
        id: "taco-prep",
        title: "Set out the black beans for taco night",
        details: "Thursday dinner: sweet potato tacos with avocado-lime crema.",
        state: "open",
        metadata: [{ label: "Day", value: "Thursday" }],
      },
      {
        id: "sunday-dinner",
        title: "Confirm Sunday's dinner before the grocery run",
        details: "The current plan is baked salmon with lemon, dill, and roasted asparagus.",
        state: "open",
        metadata: [{ label: "Day", value: "Sunday" }],
      },
    ],
  },
  // Money keeps a small area in the queue so empty, one-item, and multi-item
  // section proportions can all be judged in the same screen.
  {
    id: "money",
    name: "Money",
    description: "A short watchlist for household spending and renewals.",
    accent: "juniper",
    entries: [
      {
        id: "subscription-renewal",
        title: "Confirm whether the annual subscription renews this month",
        state: "open",
        metadata: [
          { label: "Category", value: "Watchlist" },
          { label: "Due", value: "This month" },
        ],
      },
      {
        id: "checking-balance",
        title: "Check the checking account balance",
        state: "done",
        metadata: [{ label: "Completed", value: "7:46 AM" }],
      },
    ],
  },
];
