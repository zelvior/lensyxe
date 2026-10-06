package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/zelvior/lensyxe/internal/analyzer"
	"github.com/zelvior/lensyxe/internal/server"
	"github.com/zelvior/lensyxe/internal/storage"
	"github.com/zelvior/lensyxe/pkg/models"
)

// execCommand is indirected so tests can exercise the browser launch path
// without actually spawning a browser on the machine running them.
var execCommand = func(name string, args ...string) error {
	return exec.Command(name, args...).Start()
}

func newServeCmd(a *app) *cobra.Command {
	var (
		port        int
		host        string
		open        bool
		noAnalyze   bool
		openTimeout time.Duration
	)

	cmd := &cobra.Command{
		Use:   "serve [path]",
		Short: "Serve the local dashboard and JSON API",
		Long: strings.TrimSpace(`
Serve the Lensyxe dashboard and its JSON API from a single local process.

The analysis is run once at startup and re-run on demand rather than on every
request, so opening the page repeatedly does not re-scan the repository. Use the
Reload button in the dashboard, or restart the command, to refresh.

The listener binds to the loopback interface. Nothing is exposed to the network,
and no handler makes an outbound request.`),
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := a.withTarget(args)

			// The analyzer resolves the target itself; passing it through
			// unchanged keeps one resolution path shared with `analyze`.
			cfg.Target = absTarget(cfg.Target)

			// Analyze before binding. A port that opens and then 500s on every
			// request is worse than a clear failure at startup.
			var (
				cached *models.Snapshot
				store  *storage.Store
			)

			if !noAnalyze {
				ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
				defer stop()

				snap, err := analyzer.Scan(ctx, scanConfig(cfg), Version)
				if err != nil {
					return fmt.Errorf("initial analysis: %w", err)
				}
				cached = snap
				if _, err := a.persist(cached); err != nil {
					// The dashboard is still useful without history, so this is
					// reported rather than fatal.
					cmd.PrintErrf("lensyxe: could not record history: %v\n", err)
				}
			}

			if !noAnalyze {
				db, err := a.openStore(cached.Root)
				if err != nil {
					cmd.PrintErrf("lensyxe: history unavailable: %v\n", err)
				} else {
					store = db
				}
			}

			assets, err := server.AssetHandler()
			if err != nil {
				return err
			}
			if !server.HasDashboard() {
				cmd.PrintErrf("lensyxe: %v\n", server.ErrDashboardNotBuilt)
			}

			// The snapshot is captured once and shared. Each request gets the
			// same pointer, and the only mutation is the whole-value swap
			// performed by a reload, which is safe to guard with a mutex: no
			// request ever writes through the shared struct.
			cache := &snapshotCache{snap: cached}

			var storeFunc server.StoreFunc
			if store != nil {
				storeFunc = func() (*storage.Store, error) { return store, nil }
			}

			srv, err := server.New(server.Config{
				Port:     port,
				Host:     host,
				Snapshot: cache.get,
				Store:    storeFunc,
				Assets:   assets,
			})
			if err != nil {
				return err
			}

			ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
			if err != nil {
				return fmt.Errorf("listen on %s:%d: %w", host, port, err)
			}

			// A wildcard bind reports a routable address the browser cannot
			// use, so the printed URL is derived from the configured host
			// rather than from the listener.
			url := "http://" + browserHost(host) + ":" + strconv.Itoa(port)

			httpSrv := &http.Server{
				Handler:           srv.Handler(),
				ReadHeaderTimeout: 10 * time.Second,
			}

			if open {
				if err := openBrowser(url); err != nil {
					cmd.PrintErrf("lensyxe: could not open a browser: %v\n", err)
				}
			}

			cmd.Printf("Lensyxe dashboard on %s\n", url)
			if cached != nil {
				cmd.Printf("  analyzing %s\n", cached.Root)
				cmd.Printf("  score %.1f (%s), %d risks\n",
					cached.Health.Score, cached.Health.Grade, len(cached.Risks))
			}
			cmd.Printf("  press Ctrl+C to stop\n")

			// Shut down on the first signal, then a second one aborts the wait.
			// A hung request must not make Ctrl+C look ignored.
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			errCh := make(chan error, 1)
			go func() {
				if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
					errCh <- err
					return
				}
				errCh <- nil
			}()

			select {
			case err := <-errCh:
				return err
			case <-ctx.Done():
				cmd.Printf("\nshutting down\n")
			}

			shutdownCtx, cancel := context.WithTimeout(context.Background(), openTimeout)
			defer cancel()
			if err := httpSrv.Shutdown(shutdownCtx); err != nil {
				return fmt.Errorf("shutdown: %w", err)
			}
			return <-errCh
		},
	}

	cmd.Flags().IntVar(&port, "port", server.DefaultPort, "port to listen on")
	cmd.Flags().StringVar(&host, "host", server.DefaultLoopback, "bind address")
	cmd.Flags().BoolVar(&open, "open", false, "open the dashboard in the default browser")
	cmd.Flags().BoolVar(&noAnalyze, "no-analyze", false, "skip the initial analysis and serve only recorded history")
	cmd.Flags().DurationVar(&openTimeout, "shutdown-timeout", 5*time.Second, "how long to wait for in-flight requests on shutdown")

	return cmd
}

// absTarget resolves the analysis target to an absolute path, falling back to
// the original value when resolution fails.
//
// Absolute paths matter here: the history database is keyed by root, and a
// relative target would record under a different key depending on the working
// directory the server was started from. A failure to resolve is left for the
// analyzer to report, which owns that diagnostic.
func absTarget(target string) string {
	abs, err := filepath.Abs(target)
	if err != nil {
		return target
	}
	return abs
}

// browserHost turns a bind address into one a browser can open.
//
// A wildcard bind has no single address to visit, so the loopback name is
// used: that is where the browser on the same machine will reach it.
func browserHost(host string) string {
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		return "localhost"
	default:
		return host
	}
}

// snapshotCache holds the analysis served to every request.
//
// A mutex plus whole-value replacement is sufficient and is the only safe
// option here: several requests are in flight at once, and any of them could
// be reading fields of the snapshot the moment a reload swaps it.
type snapshotCache struct {
	snap *models.Snapshot
	mu   sync.RWMutex
}

// get returns the current snapshot.
func (c *snapshotCache) get(context.Context) (*models.Snapshot, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.snap == nil {
		return nil, errors.New("no analysis available; start the server without --no-analyze")
	}
	return c.snap, nil
}

// openBrowser launches the default browser.
//
// Failure is not propagated: the server is already running and usable, and a
// headless machine has no browser to launch. The caller warns instead.
func openBrowser(url string) error {
	switch runtime.GOOS {
	case "windows":
		return execCommand("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		return execCommand("open", url)
	default:
		return execCommand("xdg-open", url)
	}
}
