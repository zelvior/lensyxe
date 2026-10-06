# Dashboard build output

Node and Next.js artifacts are not committed. The compiled Go binary is the
artifact: `internal/server/assets/` holds the static export at compile time, and
that directory is copied from `dashboard/out/`.

## Build

```bash
cd dashboard
npm install
npm run build          # emits dashboard/out/
cd ..

# Copy the export into the embed directory, then build the binary.
cp -r dashboard/out/. internal/server/assets/
go build ./cmd/lensyxe
```

On Windows (PowerShell):

```powershell
cd dashboard; npm install; npm run build; cd ..
Copy-Item -Recurse -Force dashboard\out\* internal\server\assets\
go build ./cmd/lensyxe
```

## How embedding works

`internal/server/embed.go` declares:

```go
//go:embed all:assets
var assets embed.FS
```

The `all:` prefix is required. Without it, `go:embed` skips any path beginning
with `_` or `.`, which is exactly where Next.js puts its build output
(`_next/static/...`). Omitting it produces a binary whose JavaScript is
silently missing, and a dashboard that renders but never hydrates.

A committed `internal/server/assets/.gitkeep` keeps `go build` working on a
clean checkout: `go:embed` fails at compile time when the matched directory is
empty or absent. With only the placeholder present the server serves a real
message explaining how to build the dashboard, rather than a blank page or a
404.

## Why `output: 'export'`

The static export is what makes embedding possible at all. With the default
Node.js server runtime, Next.js needs a long-lived Node process to serve
requests, and a Go binary embedding that would be a lie: the process would die
with the first request. The export produces plain files, so the Go process is
the only server and there is no second runtime to keep alive.

## Why the page is a server component

`page.tsx` fetches from `/api/v1/*` during render rather than in the browser.
That is what lets `dynamic = 'force-dynamic'` produce a fresh page per request
against live data, while still exporting to static HTML. The alternative,
fetching in `useEffect`, would ship an empty shell and then fill it in, showing
"no data" to anyone whose JavaScript had not run yet.

## Contract

`src/lib/types.ts` mirrors the JSON tags in `pkg/models` and the response
structs in `internal/server/api.go`. The two are checked against each other:
`internal/server/dashboard_test.go` asserts that every field the TypeScript
type declares is actually present in a real JSON response, so adding a field to
one side without the other fails the Go test suite.