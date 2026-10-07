// Regenerates the responsive harness into dashboard/out/.
//
// It has to live in out/ because it iframes index.html, and `npm run build`
// empties that directory -- the harness was silently deleted twice mid-session
// before this script existed.
//
// An iframe is the only way to test responsive layout here: the real browser
// window cannot be resized from the available tooling, and media queries inside
// an iframe resolve against the iframe's own width, so one page exercises seven
// viewports at once.
//
//   node scripts/responsive-harness.mjs [path-to-out]
import { writeFileSync, existsSync, mkdirSync, copyFileSync } from 'node:fs';
import { homedir } from 'node:os';
import { join, resolve } from 'node:path';

const out = resolve(process.argv[2] ?? 'out');
if (!existsSync(join(out, 'index.html'))) {
  console.error(`no index.html in ${out} -- run npm run build first`);
  process.exit(1);
}

// The snapshot is copied in so the frames can be fed real data. The harness is
// gitignored build output and never committed.
const snapshot = join(homedir(), 'AppData', 'Local', 'Temp', 'snap.json');
if (existsSync(snapshot)) copyFileSync(snapshot, join(out, 'snap.json'));
else console.warn('no snapshot at %s; frames will render the loader only', snapshot);

const page = `<!doctype html>
<html><head><meta charset="utf-8"><title>responsive harness</title>
<style>
  body { margin: 0; background: #111; font: 12px monospace; color: #ccc; }
  .wrap { display: flex; flex-wrap: wrap; gap: 10px; padding: 10px; }
  .col { display: flex; flex-direction: column; gap: 4px; }
  label { color: #7dd3fc; }
  iframe { border: 1px solid #444; background: #020617; height: 780px; }
</style></head>
<body>
<div class="wrap" id="w"></div>
<script>
const WIDTHS = [360, 414, 640, 768, 985, 1280, 1600];

async function feed(frame) {
  const d = frame.contentDocument;
  const input = d && d.querySelector('input[type=file]');
  if (!input) return 'no input';
  const text = await (await fetch('/snap.json')).text();
  const dt = new DataTransfer();
  dt.items.add(new File([text], 'lensyxe.json', { type: 'application/json' }));
  input.files = dt.files;
  input.dispatchEvent(new Event('change', { bubbles: true }));
  return 'fed';
}

const frames = WIDTHS.map((px) => {
  const col = document.createElement('div');
  col.className = 'col';
  const label = document.createElement('label');
  label.textContent = px + 'px';
  const f = document.createElement('iframe');
  f.width = px; f.height = 780; f.src = 'index.html';
  col.appendChild(label); col.appendChild(f);
  document.getElementById('w').appendChild(col);
  return f;
});

window.__feedAll = async () => {
  await new Promise((r) => setTimeout(r, 1500));
  const res = [];
  for (const f of frames) res.push(await feed(f));
  await new Promise((r) => setTimeout(r, 1200));
  return res;
};

// Audit: overflow that is not already inside a scroll container, plus which
// table columns survive at each width.
window.__audit = () => {
  const inScroller = (el, doc) => {
    let p = el.parentElement;
    while (p && p !== doc.body) {
      const ov = getComputedStyle(p).overflowX;
      if (ov === 'auto' || ov === 'scroll' || ov === 'hidden') return true;
      p = p.parentElement;
    }
    return false;
  };
  return frames.map((f) => {
    const d = f.contentDocument;
    if (!d) return { frame: f.width, err: 'no doc' };
    const de = d.documentElement, vw = de.clientWidth;
    let worst = 0, cls = '';
    d.querySelectorAll('*').forEach((el) => {
      const b = el.getBoundingClientRect();
      if (b.width <= 0 || b.right <= vw + 1) return;
      if (inScroller(el, d)) return;
      const ex = Math.round(b.right - vw);
      if (ex > worst) { worst = ex; cls = el.tagName + '.' + (el.className || '').toString().slice(0, 40); }
    });
    const visCols = (t) =>
      [...t.querySelectorAll('thead th')].filter((th) => getComputedStyle(th).display !== 'none').length;
    const tables = [...d.querySelectorAll('table')];
    return {
      frame: f.width, inner: vw,
      docHOver: de.scrollWidth > vw + 1,
      realOverflowPx: worst, cls,
      tables: tables.length,
      visibleCols: tables.map(visCols),
      rows: d.querySelectorAll('tbody tr').length,
      meters: d.querySelectorAll('[role=meter]').length,
      score: /\\d\\d\\.\\d/.test(d.body.textContent),
    };
  });
};
</script>
</body></html>
`;

writeFileSync(join(out, 'harness.html'), page);
console.log(`harness written to ${join(out, 'harness.html')}`);