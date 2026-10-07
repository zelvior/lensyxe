# Dashboard

The Lensyxe dashboard is one build with two front doors.

## `lensyxe serve` — live

The binary serves the dashboard and `/api/v1` from one process, reading a local
repository. The page probes `/api/v1/health` on load; a 2xx means it is live and
re-analyzes on demand when you press Reload.

## <https://lensyxe.vercel.app> — snapshot

On a static host there is no process behind the page and no repository to read.
The probe 404s, and instead of reporting an error the page asks for a file:

```bash
lensyxe analyze . --format json > lensyxe.json
```

Drag it onto the page, or pick it with the button. `src/lib/snapshot.ts`
validates and reshapes it into the four API response shapes the components
already render, so the two modes share one set of components.

## What it does not do

**No upload.** The file is read with `File.text()` and parsed in memory. Nothing
is sent anywhere. This is the only way a hosted page can honour a tool whose
first promise is local-first analysis with no telemetry, and it is why there is
no backend and no account.

**No fabricated history.** A snapshot embeds no timeline, so `history` is an
empty series and the timeline says "no snapshots recorded yet". Rendering a flat
line there would imply a stability that was never measured.

**No invented counts.** The API computes `risk_count`, `critical_risk_count` and
`hotspot_count` server-side and the snapshot document does not carry them as
top-level fields, so `snapshot.ts` recomputes them from the arrays rather than
defaulting to zero. Zeroing them would misreport a document full of findings as
a clean one.

**No silent tolerance of bad input.** A missing `health` or `code` is an error
naming the field. An unknown severity is coerced to `info` rather than dropping
the whole report, because the score is still valid and one unfamiliar severity is
genuinely less important than the rest.

## Accessibility

Lighthouse scores 1.00 for accessibility, best practices and SEO on both routes,
with no failing audits. Reaching it required three real fixes rather than
suppressing the rules:

- `text-slate-600` measured 2.55:1 and `text-slate-500` 4.06:1 against the card
  background, both under the 4.5:1 minimum. Both moved up a tier; `slate-300`
  and `slate-400` keep the hierarchy while passing.
- Card titles were `<p class="card-title">`, so the `<h3>` inside the risk list
  had no `h2` ancestor and heading order was invalid. They are real `<h2>`
  elements now.
- Table cells needed associated headers: `scope="col"` on every column header,
  and the file column in the hotspot table became `<th scope="row">` because it
  is the key each row is about.

The primary button also moved from `bg-sky-600` to `bg-sky-700`; white on
`sky-600` is 4.09:1.

## Responsive behaviour

The shell steps padding at `sm` and `lg` rather than fixing it, and declares a
viewport — without one, mobile browsers lay out at ~980px and scale down, which
makes every size in the stylesheet wrong by that factor. Section grids collapse
`lg:grid-cols-*` to a single column, the header wraps, and every wide table sits
in an `overflow-x-auto` container with `min-w-0` so a wide table scrolls inside
its card rather than dragging the whole page sideways.

## Deploying

`dashboard/vercel.json` declares `framework: nextjs` and the security headers.
The framework line is load-bearing: without it Vercel falls back to treating the
project as a static site and fails with `The Output Directory "public" is empty`,
because `dashboard/public/` is genuinely empty.

The export is a normal `next build` static export. The Go binary embeds the same
output through `internal/server/assets`; copying `dashboard/out/` into that
directory is what the release workflow does, and nothing here changes that path.

Web Analytics and Speed Insights are **off**. Vercel analytics is opt-in and
disabled for the project; the CLI's `disable` command refuses to run
non-interactively, so it was verified rather than assumed.