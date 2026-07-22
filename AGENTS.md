# Agent Instructions

Work in very small, reviewable increments.

## Commenting Standard

- Comment code generously. Err toward including a useful explanation when a future reader could reasonably ask what a block is responsible for, why it exists, or how it works.
- Prefer more comments over fewer comments. Assume the next reader is intelligent but may be new to this codebase, React, the product vocabulary, or the framework feature being used.
- Write comments in plain English. Define or replace terms such as "immutable update," "optimistic update," "adapter," "render tree," and "scrim" unless the nearby code makes their meaning obvious.
- Connect code concepts to the interface a person sees. For example, explain that a `ReviewArea` represents one top-level group such as Mail and that it renders as one `.area-section` on the Review screen.
- For event handlers and other functions, explain what causes the function to run, what inputs identify, what state changes, and what visible result follows. Include a short example when it makes the flow easier to understand.
- For React state, explain how a state setter affects derived values and rendering. For example: "Selecting Mail stores `mail` as the active area; the filtered-area calculation then removes Home, Health, and the other areas from the main view."
- For repeated decorative markup, explain why multiple elements are required and how CSS uses each one. Do not assume the visual result is obvious from empty elements such as `<span />`.
- For framework setup code, explain whether the pattern is standard and identify the files or DOM elements it connects. For example, describe how React's `createRoot` attaches the application to `<div id="root">` in `index.html`.
- Begin every non-trivial implementation file with a short overview describing the file's purpose, its place in the architecture, and any important assumptions or boundaries.
- Add section comments around major responsibilities, render regions, data transformations, state transitions, side effects, and multi-step control flow.
- Explain the intent and mechanism of non-obvious behavior, especially accessibility decisions, responsive behavior, saving/persistence boundaries, updates shown before a server confirms them, error handling, concurrency, compatibility workarounds, and places where backend data is translated for the frontend.
- Use JSDoc or equivalent documentation for exported types, interfaces, functions, and fields when their purpose is not completely self-evident.
- Document fixtures and mock data in terms of the product states and edge cases they are intended to exercise.
- Organize stylesheets with comments that identify tokens, layout systems, component groups, interactions, breakpoints, and reduced-motion behavior.
- Prefer comments that explain purpose, constraints, and reasoning over comments that merely translate one line of syntax into English. Concise syntax-level comments are still welcome when they improve scanning.
- Keep comments accurate as behavior changes. Remove or revise stale comments in the same increment as the code they describe.

## Default Change Scope

- Change one implementation file at a time by default.
- Include the directly related test file in the same increment when a test change is needed.
- Keep each diff focused on one clear purpose.
- Avoid bundling unrelated cleanup, refactors, formatting, or behavior changes with the requested work.
- If a change needs more than one implementation file or more than the direct test file, pause and explain why before editing it.

## Review Rhythm

After each implementation-and-test increment:

- Summarize what changed.
- Point to the exact implementation file and test file, when both were changed.
- Mention any verification performed or still needed.
- Wait for the user's review or explicit go-ahead before expanding to another implementation file.

## When To Expand Scope

Only increase beyond one file at a time when:

- The user explicitly asks for a broader pass.
- A change cannot work without another implementation file.
- A failing test or runtime error identifies the next necessary file.

When expanding scope, state the next implementation file and why it is needed before editing it.

## Verification

- Prefer focused checks that match the small change.
- Run broader tests only when the change affects shared behavior or the user asks for extra confidence.
- Report exact commands and results concisely.
