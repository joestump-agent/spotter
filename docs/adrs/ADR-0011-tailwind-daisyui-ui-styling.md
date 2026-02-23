---
status: accepted
date: 2026-02-21
decision-makers: joestump
---

# ADR-0011: Tailwind CSS with DaisyUI over Traditional CSS Frameworks for Server-Rendered UI Styling

## Context and Problem Statement

Spotter's UI is server-rendered using Go Templ templates with HTMX for interactivity (ADR-0001). The application needs a styling approach that provides a consistent design system, supports theming for user personalization, and works naturally with server-rendered HTML rather than requiring a JavaScript component framework. How should Spotter style its UI components?

## Decision Drivers

* Templates are rendered server-side in `.templ` files — CSS classes must work without a JavaScript framework runtime
* HTMX swaps HTML fragments — styling must be self-contained in class attributes, not dependent on component state or JavaScript initialization
* Users should be able to choose from multiple themes (configured via `SPOTTER_THEME_AVAILABLE` and `SPOTTER_THEME_DEFAULT`)
* The build pipeline must be simple — CSS compilation at build time, no client-side CSS-in-JS processing
* DaisyUI's semantic component classes (e.g., `btn`, `card`, `alert`) should reduce verbose utility class repetition in templates
* Iconography needs (Heroicons, Simple Icons) should integrate into the same toolchain

## Considered Options

* **Tailwind CSS + DaisyUI** — utility-first CSS framework with semantic component plugin, compiled at build time via Tailwind CLI
* **Bootstrap** — opinionated component framework with jQuery dependency and pre-built JavaScript components
* **Plain CSS with BEM** — manual CSS using Block-Element-Modifier naming convention, no framework
* **Shadcn/Radix UI** — React-based headless component library with Tailwind styling primitives

## Decision Outcome

Chosen option: **Tailwind CSS + DaisyUI**, because it provides a complete design system that works purely through CSS classes — no JavaScript runtime required. DaisyUI's semantic component classes (`btn`, `card`, `modal`, `drawer`, `alert`, `badge`, `menu`, `table`, `form-control`) map directly to UI patterns used throughout Spotter, while Tailwind's utility classes handle layout and spacing. The `data-theme` attribute on `<html>` enables instant theme switching with zero JavaScript framework overhead, consistent with the HTMX-first approach documented in ADR-0001. The Tailwind CLI compiles and tree-shakes CSS at build time, producing a single minified `output.css` file.

### Consequences

* Good, because DaisyUI semantic classes keep Templ templates readable — `class="btn btn-primary"` instead of `class="px-4 py-2 bg-blue-500 text-white rounded hover:bg-blue-600"`
* Good, because built-in theming via `data-theme` attribute supports all 32+ DaisyUI themes with zero additional CSS — configured in `tailwind.config.js` with `daisyui: { themes: true }`
* Good, because Tailwind's content scanning (`content: ["./cmd/**/*.{html,templ,go}", "./internal/**/*.{html,templ,go}"]`) tree-shakes unused classes, keeping the production CSS small
* Good, because `@iconify/tailwind` plugin integrates Heroicons and Simple Icons as CSS classes (e.g., `icon-[heroicons--musical-note]`, `icon-[simple-icons--spotify]`), avoiding separate icon libraries
* Good, because the Tailwind CLI runs as a single `npx tailwindcss` command — no Webpack, Vite, or bundler configuration needed
* Bad, because Node.js is required in the build pipeline solely for CSS compilation — the Go application itself has no Node.js dependency at runtime
* Bad, because DaisyUI v4 ships all theme CSS when `themes: true` — this includes themes that may never be used, slightly inflating the CSS bundle
* Bad, because Tailwind utility classes in Templ templates create long `class` attributes that can reduce template readability for complex layouts

### Confirmation

Compliance is confirmed by `tailwind.config.js` containing `require("daisyui")` in the plugins array. All `.templ` files in `internal/views/` should use DaisyUI semantic classes (e.g., `card`, `btn`, `alert`, `badge`, `drawer`, `menu`) rather than raw HTML elements with only utility classes. The CSS input file at `static/css/input.css` should contain only the three Tailwind directives (`@tailwind base; @tailwind components; @tailwind utilities`). No inline `<style>` blocks should appear in Templ templates (except the minimal `[x-cloak]` rule in the base layout).

## Pros and Cons of the Options

### Tailwind CSS + DaisyUI

Tailwind CSS v3.4 with DaisyUI v4.12 plugin. Configuration in `tailwind.config.js` scans `.templ`, `.html`, and `.go` files under `cmd/` and `internal/` for class usage. DaisyUI adds 50+ semantic component classes. Iconify plugin adds dynamic icon classes from Heroicons and Simple Icons packages.

* Good, because DaisyUI components are pure CSS — `drawer`, `modal`, `dropdown` work without JavaScript initialization, compatible with HTMX fragment swaps
* Good, because `templ.KV("active", condition)` in Go templates conditionally applies DaisyUI state classes (e.g., adding `active` to menu items based on current path)
* Good, because theme switching is instant — `document.documentElement.setAttribute('data-theme', theme)` with `localStorage` persistence (seen in `dashboard.templ:183-189`)
* Good, because DaisyUI's color system uses semantic tokens (`primary`, `base-100`, `base-content`, `accent`) that automatically adapt across themes
* Good, because the `watch:css` npm script enables hot-reload during development alongside `templ generate --watch` and `air`
* Neutral, because DaisyUI v4 is a stable release but DaisyUI v5 may require migration effort
* Bad, because developers must know both DaisyUI semantic classes and Tailwind utility classes — two layers of abstraction
* Bad, because Tailwind's purge scanning can miss dynamically constructed class names (mitigated by using complete class strings in templates)

### Bootstrap

Bootstrap 5 component framework with pre-built JavaScript components (dropdowns, modals, tooltips).

* Good, because widely known — most web developers have Bootstrap experience
* Good, because comprehensive component library with built-in JavaScript behaviors
* Bad, because Bootstrap's JavaScript components (dropdowns, modals, collapse) conflict with HTMX's approach of swapping HTML fragments — Bootstrap expects components to be initialized via JavaScript after page load
* Bad, because theming requires Sass variable overrides and custom builds — no runtime theme switching via a single data attribute
* Bad, because opinionated grid system and component styles are harder to customize than Tailwind utilities
* Bad, because Bootstrap's CSS bundle includes all components regardless of usage — no tree-shaking

### Plain CSS with BEM

Hand-written CSS using Block-Element-Modifier naming convention (e.g., `.card__header--active`).

* Good, because zero dependencies — no build tools, no framework lock-in
* Good, because complete control over every CSS rule
* Bad, because no design system — every component's colors, spacing, and typography must be manually defined and maintained
* Bad, because theming requires a custom CSS variable system built from scratch
* Bad, because no tree-shaking — unused styles remain in the stylesheet unless manually cleaned
* Bad, because significantly more CSS to write and maintain for the number of components Spotter uses (50+ distinct UI patterns)

### Shadcn/Radix UI

Headless React component library with Tailwind CSS styling, providing accessible, composable primitives.

* Good, because accessible by default — ARIA attributes, keyboard navigation, and focus management built in
* Good, because Tailwind-based styling means shared knowledge with the current approach
* Bad, because requires React as a runtime dependency — fundamentally incompatible with Go Templ server-rendered templates and HTMX
* Bad, because components are React `.tsx` files — cannot be used in `.templ` files
* Bad, because would require a full architecture change away from server-rendered HTML to a React SPA with an API backend

## More Information

* Tailwind configuration: `tailwind.config.js` — content paths, DaisyUI plugin, Iconify plugin, theme settings
* CSS entry point: `static/css/input.css` — three Tailwind directives only
* CSS build output: `static/css/output.css` — compiled, minified production CSS
* CSS build command: `Makefile:58-61` — `npx tailwindcss -i ./static/css/input.css -o ./static/css/output.css --minify`
* Package versions: `package.json` — `tailwindcss: ^3.4.1`, `daisyui: ^4.12.24`
* Icon packages: `package.json` — `@iconify-json/heroicons`, `@iconify-json/simple-icons`, `@iconify/tailwind`
* Base layout: `internal/views/layouts/base.templ` — loads `/static/css/output.css`, sets `data-theme` attribute
* Dashboard layout: `internal/views/layouts/dashboard.templ` — demonstrates DaisyUI `drawer`, `navbar`, `menu`, `btn` components with theme switching script
* Login page: `internal/views/auth/login.templ` — demonstrates `hero`, `card`, `form-control`, `input`, `btn`, `alert`, `divider` components
* Toast component: `internal/views/components/toast.templ` — demonstrates `alert`, `toast` with conditional DaisyUI variant classes via `templ.KV()`
* Theme configuration: `internal/config/config.go:71-74` — `Theme.Available` and `Theme.Default` config fields
* HTMX + Templ UI decision: see ADR-0001
