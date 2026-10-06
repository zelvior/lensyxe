'use client';

import { useCallback, useEffect, useRef, useState } from 'react';
import {
  fetchOverview,
  fetchHistory,
  ApiError,
} from 'lib/api';
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
 * LoadState distinguishes "still loading" from "failed".
 *
 * These must not be collapsed into one flag. A dashboard that renders zeros
 * while loading is indistinguishable from a dashboard reporting a score of
 * zero, which is the single most misleading state this page could be in.
 */
export type LoadState =
  | { status: 'loading' }
  | { status: 'ready'; data: OverviewData }
  | { status: 'error'; message: string };

/**
 * useOverview fetches the dashboard data.
 *
 * The fetch runs in the browser rather than during render because the export is
 * static: there is no server process to ask at build time or at request time,
 * so the Go API is necessarily the client's to call.
 *
 * An AbortController guards every effect. Without it, a reload that races a
 * slow in-flight request can have its result overwritten by the stale one, and
 * the page ends up showing a score for a commit that is no longer checked out.
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
      setState({ status: 'ready', data: { ...overview, history } });
    } catch (err) {
      if (ctrl.signal.aborted) return;
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

  return { state, reload: load };
}