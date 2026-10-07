'use client';

import { useCallback, useEffect, useRef, useState } from 'react';
import {
  fetchOverview,
  fetchHistory,
  ApiError,
} from 'lib/api';
import type { SnapshotOverview } from 'lib/snapshot';
import type {
  HealthResponse,
  HistoryResponse,
  HotspotsResponse,
  RisksResponse,
} from 'lib/types';

/** OverviewData is everything the overview page renders. */
export interface OverviewData {
  health: HealthResponse;
  risks: RisksResponse;
  hotspots: HotspotsResponse;
  history: HistoryResponse;
}

/**
 * Mode says where the figures came from, and it is part of the state rather than
 * a boolean because the distinction changes what the page may claim.
 *
 * `live` means a Go process answered /api/v1 and re-analyzed on request. A
 * snapshot means a file the user brought is being displayed, and it cannot be
 * refreshed: there is nothing behind it to refresh from. Collapsing the two
 * would let a static page imply that pressing reload re-reads a repository.
 */
export type Mode = 'live' | 'snapshot';

/**
 * LoadState distinguishes "still loading" from "failed" from "waiting for a file".
 *
 * These must not be collapsed into one flag. A dashboard that renders zeros
 * while loading is indistinguishable from a dashboard reporting a score of
 * zero, which is the single most misleading state this page could be in.
 */
export type LoadState =
  | { status: 'loading' }
  | { status: 'needs-snapshot' }
  | { status: 'ready'; mode: Mode; data: OverviewData; snapshot?: SnapshotOverview['meta'] }
  | { status: 'error'; message: string };

/**
 * useOverview loads the dashboard data from whichever source exists.
 *
 * The fetch runs in the browser rather than during render because the export is
 * static: there is no server process to ask at build time or at request time,
 * so the Go API is necessarily the client's to call.
 *
 * An AbortController guards every effect. Without it, a reload that races a
 * slow in-flight request can have its result overwritten by the stale one, and
 * the page ends up showing a score for a commit that is no longer checked out.
 *
 * When the API is absent -- which is every static host -- this does not report an
 * error. A missing API on a static host is not a failure, it is the expected
 * shape of that deployment, and telling the user to restart a server they never
 * started would be wrong.
 */
export function useOverview() {
  const [state, setState] = useState<LoadState>({ status: 'loading' });
  const abortRef = useRef<AbortController | null>(null);

  const load = useCallback(async () => {
    // A new request cancels the previous one: the newest click wins.
    abortRef.current?.abort();
    const ctrl = new AbortController();
    abortRef.current = ctrl;

    setState({ status: 'loading' });

    try {
      const [overview, history] = await Promise.all([
        fetchOverview(ctrl.signal),
        fetchHistory(ctrl.signal),
      ]);
      // A response that arrived after cancellation must not be applied.
      if (ctrl.signal.aborted) return;
      setState({
        status: 'ready',
        mode: 'live',
        data: { ...overview, history },
      });
    } catch (err) {
      if (ctrl.signal.aborted) return;

      // 404 and 405 mean the host answered but has no API here. A network
      // failure or a 5xx means something is actually wrong, and that deserves
      // the error state rather than a file picker.
      const missing =
        err instanceof ApiError && (err.status === 404 || err.status === 405);
      if (missing) {
        setState({ status: 'needs-snapshot' });
        return;
      }
      setState({
        status: 'error',
        message: err instanceof ApiError ? err.message : String(err),
      });
    }
  }, []);

  useEffect(() => {
    void load();
    return () => abortRef.current?.abort();
  }, [load]);

  /**
   * loadSnapshot replaces the state with a user-supplied snapshot.
   *
   * The empty history it carries is passed through unchanged: the timeline
   * component renders "no recorded runs", which is the truth for a file with no
   * history in it, rather than a flat line implying stability.
   */
  const loadSnapshot = useCallback((snap: SnapshotOverview) => {
    setState({
      status: 'ready',
      mode: 'snapshot',
      data: {
        health: snap.health,
        risks: snap.risks,
        hotspots: snap.hotspots,
        history: snap.history,
      },
      snapshot: snap.meta,
    });
  }, []);

  /**
   * backToLoader returns a snapshot-mode page to the file picker.
   *
   * Distinct from reload: reload asks an API that a static host does not have,
   * so on that deployment it would immediately land back here anyway. Going
   * straight to the picker makes the button do what its label says.
   */
  const backToLoader = useCallback(() => {
    abortRef.current?.abort();
    setState({ status: 'needs-snapshot' });
  }, []);

  return { state, reload: load, loadSnapshot, backToLoader };
}