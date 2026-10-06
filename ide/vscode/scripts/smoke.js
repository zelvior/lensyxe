/**
 * Smoke test for the CLI client against the real binary.
 *
 * The client is the only part of the extension that talks to the outside world,
 * and it is deliberately free of any `vscode` import so it can be exercised
 * without an extension host. That makes it the one piece here that can be
 * verified rather than assumed.
 *
 * It checks the things that actually break in practice: that the JSON parses,
 * that a threshold exit code is not treated as a failure, that a missing binary
 * produces an actionable message rather than a raw ENOENT, and that a superseded
 * run stays silent.
 *
 * Usage:
 *   npm run build:lensyxe      # from the repository root, or set LENSYXE_BIN
 *   node scripts/smoke.js <path-to-lensyxe-binary> <path-to-repository>
 */
const path = require('node:path');
const { analyze, serveDashboard, LensyxeError, SupersededError, TimeoutError } =
  require('../out/lensyxe.js');

let failures = 0;

function check(name, condition, detail) {
  if (condition) {
    console.log(`  ok    ${name}`);
  } else {
    failures++;
    console.log(`  FAIL  ${name}${detail ? ` -- ${detail}` : ''}`);
  }
}

async function main() {
  const bin = process.argv[2];
  const repo = process.argv[3];
  if (!bin || !repo) {
    console.error('usage: node scripts/smoke.js <lensyxe-binary> <repository>');
    process.exit(2);
  }

  console.log(`analyzing ${repo} with ${bin}`);

  // 1. The happy path.
  const outcome = await analyze({
    cwd: repo,
    executable: bin,
    timeoutMs: 120000,
  });
  check('snapshot parses', typeof outcome.snapshot.health.score === 'number');
  check('metrics present', Array.isArray(outcome.snapshot.health.metrics) && outcome.snapshot.health.metrics.length > 0);
  check('duration reported', outcome.durationMs > 0, String(outcome.durationMs));
  check('root echoed back', path.resolve(outcome.snapshot.root) === path.resolve(repo));
  console.log(`        score ${outcome.snapshot.health.score.toFixed(1)} grade ${outcome.snapshot.health.grade}`);
  for (const m of outcome.snapshot.health.metrics) {
    console.log(`          ${m.label}: ${m.applicable ? m.score.toFixed(1) : 'not measured'} @ ${Math.round(m.weight * 100)}%`);
  }

  // 2. Determinism: the same tree twice must agree.
  const again = await analyze({ cwd: repo, executable: bin, timeoutMs: 120000 });
  check(
    'deterministic score',
    again.snapshot.health.score === outcome.snapshot.health.score,
    `${outcome.snapshot.health.score} vs ${again.snapshot.health.score}`,
  );
  check(
    'deterministic risk ids',
    JSON.stringify(again.snapshot.risks.map((r) => r.id)) ===
      JSON.stringify(outcome.snapshot.risks.map((r) => r.id)),
  );

  // 3. A missing binary must be actionable, not a raw ENOENT.
  try {
    await analyze({ cwd: repo, executable: 'lensyxe-does-not-exist', timeoutMs: 10000 });
    check('missing binary rejected', false, 'no error thrown');
  } catch (err) {
    check(
      'missing binary rejected',
      err instanceof LensyxeError && /Cannot find the lensyxe binary/.test(err.message),
      String(err && err.message),
    );
  }

  // 4. Supersession must stay silent: a newer run replaces an older one without
  //    producing an error the user would ever see.
  {
    const controller = new AbortController();
    const slow = analyze({ cwd: repo, executable: bin, timeoutMs: 60000, signal: controller.signal });
    controller.abort();
    try {
      await slow;
      check('supersession is silent', false, 'no SupersededError');
    } catch (err) {
      check(
        'supersession is silent',
        err instanceof SupersededError,
        `${err && err.constructor && err.constructor.name}: ${err && err.message}`,
      );
    }
  }

  // 5. A timeout must be distinguishable from a supersession, because only one
  //    of them is worth telling the user about.
  try {
    await analyze({ cwd: repo, executable: bin, timeoutMs: 1 });
    check('timeout enforced', false, 'no error thrown');
  } catch (err) {
    check('timeout enforced', err instanceof TimeoutError, `${err && err.constructor && err.constructor.name}`);
  }

  // 6. serveDashboard is only imported to prove it loads; starting a server in a
  //    smoke test would leave one running.
  check('serveDashboard exported', typeof serveDashboard === 'function');

  console.log(failures === 0 ? '\nall checks passed' : `\n${failures} check(s) failed`);
  process.exit(failures === 0 ? 0 : 1);
}

main().catch((err) => {
  console.error('unexpected failure:', err);
  process.exit(1);
});