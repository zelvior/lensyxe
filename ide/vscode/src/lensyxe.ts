import { spawn } from 'node:child_process';
import type { Snapshot } from './types';

/**
 * Why exit code 2 is not an error here.
 *
 * `lensyxe analyze` exits 0 clean, 1 broken, 2 threshold breached. A configured
 * threshold still produces a full JSON report on stdout — that ordering is
 * deliberate, so a rejection never erases its own evidence. Treating exit 2 as a
 * failure would therefore discard a perfectly good snapshot for the sake of a
 * gate the editor never asked about.
 *
 * Only exit 1 (and anything unrecognised) is treated as a real failure.
 */
const EXIT_GATE_FAILED = 2;

/** Cap on captured stdout, as a string length.
 *
 * A snapshot for a very large monorepo can be tens of megabytes. Accumulating
 * without a bound is a slow memory leak triggered by whichever repository the
 * user happens to open, so the buffer is truncated and the parse fails loudly
 * with an actionable message rather than exhausting the extension host.
 */
const MAX_OUTPUT_CHARS = 96 * 1024 * 1024;

export interface RunOptions {
  /** Repository to analyze. */
  cwd: string;
  /** Resolved path to the binary, or a bare name to look up on PATH. */
  executable: string;
  /** Hard limit on the child's runtime. */
  timeoutMs: number;
  /** Token used to supersede an in-flight run. */
  signal?: AbortSignal;
}

export interface RunOutcome {
  snapshot: Snapshot;
  /** Wall-clock duration reported by the CLI, in milliseconds. */
  durationMs: number;
  /** True when the repository failed a configured threshold. */
  gateFailed: boolean;
}

/** A failure that already carries a message fit to show a user. */
export class LensyxeError extends Error {
  constructor(
    message: string,
    readonly detail?: string,
  ) {
    super(message);
    this.name = 'LensyxeError';
  }
}

/** Raised when a run is superseded by a newer one. Not shown to the user. */
export class SupersededError extends Error {
  constructor() {
    super('analysis superseded');
    this.name = 'SupersededError';
  }
}

/** Thrown when the abort came from the timeout rather than from supersession. */
export class TimeoutError extends Error {
  constructor(timeoutMs: number) {
    super(`lensyxe did not finish within ${timeoutMs}ms`);
    this.name = 'TimeoutError';
  }
}

function withTimeout(signal: AbortSignal | undefined, timeoutMs: number): {
  signal: AbortSignal;
  dispose: () => void;
} {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(new TimeoutError(timeoutMs)), timeoutMs);

  const onAbort = () => controller.abort(signal?.reason);
  if (signal) {
    if (signal.aborted) {
      controller.abort(signal.reason);
    } else {
      signal.addEventListener('abort', onAbort, { once: true });
    }
  }

  return {
    signal: controller.signal,
    dispose: () => {
      clearTimeout(timer);
      if (signal) {
        signal.removeEventListener('abort', onAbort);
      }
    },
  };
}

/**
 * Runs one analysis and returns the parsed snapshot.
 *
 * This is the extension's only subprocess. Everything else is fed from the
 * snapshot it produces, so the editor is not spawning a process per keystroke,
 * per file, or per rendered row.
 */
export function analyze(opts: RunOptions): Promise<RunOutcome> {
  const args = [
    'analyze',
    opts.cwd,
    '--format',
    'json',
    // The editor is a viewer, not a gate. Recording a snapshot on every save
    // would write to the user's repository database behind their back, so
    // persistence is off and the local dashboard reads history only when the
    // user asks for it.
    '--no-persist',
    '--no-gate',
  ];

  const { signal, dispose } = withTimeout(opts.signal, opts.timeoutMs);

  return new Promise<RunOutcome>((resolve, reject) => {
    let stdout = '';
    let stderr = '';
    let truncated = false;
    let settled = false;

    /**
     * Settles the promise once, classifying an abort correctly.
     *
     * This exists because aborting a spawned child produces an `error` event on
     * the child *before* `close` fires. Handling the error first therefore
     * reports every superseded run as "Could not start lensyxe: The operation
     * was aborted", which would pop an error on every single save and defeat the
     * point of superseding. The abort state is the authoritative signal, so it is
     * checked before anything else.
     */
    const fail = (err: Error): void => {
      if (settled) return;
      settled = true;
      dispose();

      if (signal.aborted) {
        // A newer analysis replacing this one is the normal case and must stay
        // silent. Only the timeout is worth telling the user about.
        if (signal.reason instanceof TimeoutError) {
          reject(signal.reason);
        } else {
          reject(new SupersededError());
        }
        return;
      }
      reject(err);
    };

    const child = spawn(opts.executable, args, {
      cwd: opts.cwd,
      signal,
      windowsHide: true,
      // No shell. Resolving the executable ourselves avoids a shell entirely,
      // which also means a path containing a space or a quote cannot turn into
      // command injection.
      shell: false,
      env: {
        ...process.env,
        // The CLI is local-first and needs none of these; clearing them stops a
        // stray key in the developer's environment from making an analysis fail
        // or unexpectedly reach a network provider.
        LENSYXE_AI_KEY: '',
        LENSYXE_AI_PROVIDER: '',
        LENSYXE_AI_MODEL: '',
      },
    });

    child.stdout.setEncoding('utf8');
    child.stderr.setEncoding('utf8');

    child.stdout.on('data', (chunk: string) => {
      if (truncated) return;
      if (stdout.length + chunk.length > MAX_OUTPUT_CHARS) {
        truncated = true;
        return;
      }
      stdout += chunk;
    });

    child.stderr.on('data', (chunk: string) => {
      if (stderr.length < 64 * 1024) {
        stderr += chunk;
      }
    });

    child.on('error', (err: NodeJS.ErrnoException) => {
      if (err.code === 'ENOENT') {
        fail(
          new LensyxeError(
            `Cannot find the lensyxe binary ("${opts.executable}").`,
            'Install it, or set lensyxe.executablePath to an absolute path.',
          ),
        );
        return;
      }
      if (err.code === 'EACCES') {
        fail(new LensyxeError(`Not permitted to run "${opts.executable}".`));
        return;
      }
      fail(new LensyxeError(`Could not start lensyxe: ${err.message}`));
    });

    child.on('close', (code, termSignal) => {
      if (settled) return;

      // An abort closes the child too; the error handler above normally wins the
      // race, and this catches the ordering where close arrives first.
      if (signal.aborted) {
        settled = true;
        dispose();
        if (signal.reason instanceof TimeoutError) {
          reject(signal.reason);
        } else {
          reject(new SupersededError());
        }
        return;
      }

      settled = true;
      dispose();

      if (termSignal) {
        reject(new LensyxeError(`lensyxe was terminated by ${termSignal}.`));
        return;
      }

      if (code !== 0 && code !== EXIT_GATE_FAILED) {
        const detail = stderr.trim() || stdout.trim() || `exit code ${code}`;
        reject(
          new LensyxeError(
            'lensyxe analyze failed.',
            detail.length > 2000 ? `${detail.slice(0, 2000)}\u2026` : detail,
          ),
        );
        return;
      }

      if (truncated) {
        reject(
          new LensyxeError(
            'The analysis output was too large to parse.',
            `The report exceeded ${Math.round(MAX_OUTPUT_CHARS / 1024 / 1024)} MB. ` +
              'Consider excluding directories with ignore_dirs.',
          ),
        );
        return;
      }

      let parsed: Snapshot;
      try {
        parsed = JSON.parse(stdout) as Snapshot;
      } catch (err) {
        reject(
          new LensyxeError(
            'Could not parse the analysis output.',
            err instanceof Error ? err.message : String(err),
          ),
        );
        return;
      }

      if (typeof parsed.health?.score !== 'number' || !Array.isArray(parsed.health?.metrics)) {
        reject(
          new LensyxeError(
            'The analysis output was not a Lensyxe snapshot.',
            `Expected health.score and health.metrics; saw keys: ${Object.keys(parsed).join(', ')}. ` +
              'This usually means the configured executablePath points at a different tool.',
          ),
        );
        return;
      }

      resolve({
        snapshot: parsed,
        durationMs: parsed.duration_ms ?? 0,
        gateFailed: code === EXIT_GATE_FAILED,
      });
    });
  });
}

/**
 * Starts the local dashboard.
 *
 * Resolves once the server is listening rather than when the process exits: the
 * process does not exit until the user stops it, so waiting for `close` would
 * hang forever. `--open` lets the CLI launch the browser, which avoids
 * implementing VS Code's external-URI handling.
 */
export function serveDashboard(opts: {
  cwd: string;
  executable: string;
  port?: number;
}): Promise<void> {
  const args = ['serve', opts.cwd, '--open'];
  if (opts.port) {
    args.push('--port', String(opts.port));
  }

  return new Promise<void>((resolve, reject) => {
    const child = spawn(opts.executable, args, {
      cwd: opts.cwd,
      windowsHide: true,
      shell: false,
      detached: false,
    });

    let stderr = '';
    let settled = false;

    child.stderr.setEncoding('utf8');
    child.stderr.on('data', (chunk: string) => {
      if (stderr.length < 64 * 1024) {
        stderr += chunk;
      }
      // The CLI prints a line once it is listening. Treating that as the signal
      // is what makes this a "the dashboard is up" promise rather than "a
      // process was spawned".
      if (!settled && /listening|localhost|127\.0\.0\.1/i.test(chunk)) {
        settled = true;
        resolve();
      }
    });

    child.on('error', (err: NodeJS.ErrnoException) => {
      if (err.code === 'ENOENT') {
        reject(
          new LensyxeError(
            `Cannot find the lensyxe binary ("${opts.executable}").`,
            'Install it, or set lensyxe.executablePath to an absolute path.',
          ),
        );
        return;
      }
      reject(new LensyxeError(`Could not start the dashboard: ${err.message}`));
    });

    child.on('close', (code) => {
      if (settled) return;
      settled = true;
      const detail = stderr.trim();
      reject(
        new LensyxeError(
          'The dashboard server exited before it was ready.',
          detail || `exit code ${code}`,
        ),
      );
    });

    // If the startup banner never arrives, fail rather than hang. A server that
    // prints nothing is more likely broken than slow.
    setTimeout(() => {
      if (!settled) {
        settled = true;
        reject(
          new LensyxeError(
            'The dashboard did not report itself ready in time.',
            stderr.trim() || undefined,
          ),
        );
      }
    }, 15_000);
  });
}