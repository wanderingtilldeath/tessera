// One bar and one sentence about what the batch being added is doing. Hidden when there is nothing to say.
import { on } from './state.js';

const $ = id => document.getElementById(id);
const n = x => x.toLocaleString();

let ingest = null, batch = null, hideTimer;

// Where things stand, from the upload state (if this tab started it) and the batch's own counts.
function describe() {
  const files = ingest ? ingest.files : [];
  const uploading = ingest && ingest.uploading;
  const b = batch && (!ingest || batch.id === ingest.batchId) ? batch : null;

  const parts = [];                                  // [fraction] per stage that applies
  if (files.length) {
    const finished = files.filter(f => f.state === 'done' || f.state === 'failed').length;
    const current = files.reduce((a, f) => a + (f.state === 'uploading' ? f.pct * 0.8 : f.state === 'reading' ? 0.8 : 0), 0);
    parts.push((finished + current) / files.length);
  }
  const urlsOpen = b ? b.stats.pending + b.stats.leased : 0;
  if (b && b.total) parts.push((b.total - urlsOpen) / b.total);
  if (b && b.embedding_enabled && b.chunks_total) parts.push(b.chunks_embedded / b.chunks_total);

  const done = !uploading && !!b && b.done;
  const fraction = done ? 1 : parts.length ? parts.reduce((a, x) => a + x, 0) / parts.length : 0;

  const uploadingFile = files.find(f => f.state === 'uploading'), reading = files.find(f => f.state === 'reading');
  let text;
  if (done) text = b.chunks_total ? 'Ready · ' + n(b.chunks_total) + ' passages' : 'Done · no text found';
  else if (uploadingFile) text = 'Uploading ' + uploadingFile.name;
  else if (reading) text = 'Reading ' + reading.name;
  else if (urlsOpen) text = 'Crawling · ' + n(b.total - urlsOpen) + ' of ' + n(b.total);
  else if (b && b.chunks_embedded < b.chunks_total) text = 'Embedding · ' + n(b.chunks_embedded) + ' of ' + n(b.chunks_total);
  else text = 'Working';

  return { text, fraction, done, problems: files.filter(f => f.state === 'failed') };
}

function render() {
  clearTimeout(hideTimer);
  const view = describe();
  const active = ingest || (batch && !batch.done);
  $('progress').hidden = !active;
  if (!active) return;

  const pct = Math.round(view.fraction * 100);
  $('progressText').textContent = view.text;
  $('progressPct').textContent = view.done ? '' : pct + '%';
  $('progressBar').setAttribute('aria-valuenow', pct);
  $('progressBar').firstElementChild.style.width = pct + '%';
  $('problems').replaceChildren(...view.problems.map(f => Object.assign(document.createElement('li'), { textContent: f.name + ': ' + f.error })));

  // once everything is ready, leave the result visible briefly, then get out of the way
  if (view.done && !view.problems.length) hideTimer = setTimeout(() => { ingest = null; render(); }, 4000);
}

export function init() {
  on('ingest', i => { ingest = i; render(); });
  on('batch', b => { batch = b; render(); });
}
