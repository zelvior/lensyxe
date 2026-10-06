// Static asset embedding for the Lensyxe dashboard.
//
// The dashboard is a Next.js static export copied into ./assets before the Go
// binary is compiled. Embedding it is what lets `lensyxe serve` ship as one
// file: there is no asset directory to deploy alongside the executable and no
// second runtime process to keep alive.
//
// The `all:` prefix on the embed directive is load-bearing. Next.js writes its
// build output to `_next/`, and without `all:` the embed package silently skips
// every path starting with `_` or `.`. The result is a binary whose HTML loads
// and whose JavaScript is missing, which fails as a blank page rather than a
// build error.
package server

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// assets holds the static export. The `all:` prefix includes the `_next/`
// directory Next.js relies on.
//
//go:embed all:assets
var assets embed.FS

// indexName is the entry document of the export.
const indexName = "index.html"

// DashboardFS returns the embedded dashboard rooted at the export's top level.
//
// The returned FS is a view of assets minus the "assets/" prefix, so callers
// see the same paths the export has on disk rather than the internal layout of
// the binary.
func DashboardFS() (fs.FS, error) {
	sub, err := fs.Sub(assets, "assets")
	if err != nil {
		return nil, err
	}
	// A build with only the committed placeholder has no dashboard to serve.
	// That is reported as an error rather than as an empty filesystem, because
	// the two produce very different pages and only one of them is correct.
	if _, err := fs.Stat(sub, indexName); err != nil {
		return nil, ErrDashboardNotBuilt
	}
	return sub, nil
}

// ErrDashboardNotBuilt reports that the binary contains no dashboard export.
var ErrDashboardNotBuilt = errNotBuilt{}

type errNotBuilt struct{}

func (errNotBuilt) Error() string {
	return "this binary was built without dashboard assets; " +
		"run `npm run build` in dashboard/, copy the output to internal/server/assets/, " +
		"then rebuild the binary"
}

// HasDashboard reports whether this binary carries a dashboard export.
//
// A caller uses this to decide whether to suggest `npm run build` rather than
// serving a confusing 404.
func HasDashboard() bool {
	sub, err := fs.Sub(assets, "assets")
	if err != nil {
		return false
	}
	_, err = fs.Stat(sub, indexName)
	return err == nil
}

// AssetHandler returns an HTTP handler serving the embedded dashboard.
//
// It is an SPA handler: unknown paths that are not files fall back to
// index.html, so a client-side route survives a page reload. API paths are
// never rewritten, because falling back to HTML there would turn a JSON error
// into a confusing 200 with the wrong body.
//
// A build with no embedded export serves a plain-text explanation of how to
// produce one, which is more useful than an empty directory listing.
func AssetHandler() (http.Handler, error) {
	sub, err := DashboardFS()
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(ErrDashboardNotBuilt.Error() + "\n"))
		}), nil
	}

	files := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The API must never fall through to the SPA: a JSON client asking
		// for /api/v1/risks would otherwise receive HTML with a 200.
		if strings.HasPrefix(r.URL.Path, APIPrefix) {
			http.NotFound(w, r)
			return
		}

		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name == "" {
			name = indexName
		}

		if _, statErr := fs.Stat(sub, name); statErr != nil {
			if strings.HasPrefix(name, "_next/") {
				// A missing build asset is a build problem, not a route.
				// Silently returning index.html would produce a module-load
				// error far from its cause.
				http.Error(w, "dashboard asset not found: "+name, http.StatusNotFound)
				return
			}
			r = r.Clone(r.Context())
			r.URL.Path = "/" + indexName
		}

		// Hashed assets under _next are immutable by construction; the entry
		// document must not be cached or a deploy would never be picked up.
		if strings.HasPrefix(name, "_next/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}

		files.ServeHTTP(w, r)
	}), nil
}
