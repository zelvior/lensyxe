'use client';

import { useCallback, useId, useRef, useState } from 'react';
import { parseSnapshot, SnapshotError, type SnapshotOverview } from 'lib/snapshot';

/**
 * SnapshotLoader is the hosted mode's only way in.
 *
 * It is shown when there is no /api/v1 to talk to, which is every static host.
 * The user brings a file the CLI produced; nothing is uploaded, because a tool
 * that promises local-first analysis cannot then post a repository's metrics to
 * a server to draw a chart.
 */
export function SnapshotLoader({ onLoad }: { onLoad: (o: SnapshotOverview) => void }) {
  const inputRef = useRef<HTMLInputElement>(null);
  const [error, setError] = useState<string | null>(null);
  const [dragging, setDragging] = useState(false);
  const [busy, setBusy] = useState(false);
  const inputId = useId();

  const read = useCallback(
    async (file: File) => {
      setBusy(true);
      setError(null);
      try {
        const text = await file.text();
        onLoad(parseSnapshot(text, file.name));
      } catch (err) {
        // SnapshotError messages are written to be read by a person deciding
        // what to do next; anything else is an unexpected failure and is
        // reported as such rather than hidden behind the nice message.
        setError(
          err instanceof SnapshotError
            ? err.message
            : `Could not read ${file.name}: ${String(err)}`,
        );
      } finally {
        setBusy(false);
      }
    },
    [onLoad],
  );

  const onDrop = useCallback(
    (e: React.DragEvent) => {
      e.preventDefault();
      setDragging(false);
      const file = e.dataTransfer.files?.[0];
      if (file) void read(file);
    },
    [read],
  );

  return (
    <div className="mx-auto w-full max-w-3xl px-4 py-8 sm:py-14">
      <header className="mb-6 sm:mb-8">
        <h2 className="text-xl font-semibold tracking-tight text-slate-50 sm:text-2xl">
          Open a Lensyxe snapshot
        </h2>
        <p className="mt-2 max-w-prose text-sm leading-relaxed text-slate-400">
          This page is served statically, so there is no repository behind it.
          Load a snapshot exported by the CLI and every figure below is rendered
          from that file.
        </p>
      </header>

      <div
        onDragOver={(e) => {
          e.preventDefault();
          setDragging(true);
        }}
        onDragLeave={() => setDragging(false)}
        onDrop={onDrop}
        className={`rounded-lg border-2 border-dashed p-6 text-center transition-colors sm:p-10 ${
          dragging
            ? 'border-sky-400 bg-sky-400/5'
            : 'border-slate-700 bg-slate-900/40'
        }`}
      >
        <label htmlFor={inputId} className="sr-only">
          Choose a Lensyxe snapshot
        </label>
        <input
          id={inputId}
          ref={inputRef}
          type="file"
          accept="application/json,.json"
          className="sr-only"
          onChange={(e) => {
            const f = e.target.files?.[0];
            if (f) void read(f);
            // Reset so choosing the same file twice fires onChange again.
            e.target.value = '';
          }}
        />

        <p className="text-sm text-slate-200">
          Drop <code className="rounded bg-slate-800 px-1.5 py-0.5 text-xs text-sky-200">
            lensyxe.json
          </code>{' '}
          here
        </p>
        <button
          type="button"
          disabled={busy}
          onClick={() => inputRef.current?.click()}
          className="mt-4 rounded-md bg-sky-700 px-4 py-2 text-sm font-medium text-white transition-colors hover:bg-sky-600 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-sky-400 disabled:opacity-60"
        >
          {busy ? 'Reading…' : 'Choose a file'}
        </button>
      </div>

      {error && (
        <div
          role="alert"
          className="mt-4 whitespace-pre-line rounded-md border border-red-500/40 bg-red-500/10 p-3 text-left font-mono text-xs leading-relaxed text-red-200"
        >
          {error}
        </div>
      )}

      <section className="mt-8 rounded-lg border border-slate-800 bg-slate-900/40 p-4 sm:p-5">
        <h3 className="text-sm font-semibold text-slate-200">
          Exporting a snapshot
        </h3>
        <pre className="mt-3 overflow-x-auto rounded bg-slate-950 p-3 font-mono text-xs leading-relaxed text-slate-300">
          <code>{'lensyxe analyze . --format json > lensyxe.json'}</code>
        </pre>
        <p className="mt-3 text-xs leading-relaxed text-slate-300">
          The file is parsed in this page and never uploaded. Reloading or closing
          the tab discards it, along with every figure on screen.
        </p>
      </section>
    </div>
  );
}