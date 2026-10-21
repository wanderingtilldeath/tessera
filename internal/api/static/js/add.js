// The "Add documents" form: collects URLs and files, creates the batch, uploads files one by one.
import { createBatch, uploadFile } from './api.js';
import { emit } from './state.js';

const $ = id => document.getElementById(id);
const SUPPORTED = /\.(pdf|md|markdown|html?|txt|text)$/i, MAX_BYTES = 25 << 20;

let queue = [];

const problem = f => !SUPPORTED.test(f.name) ? 'unsupported type' : f.size > MAX_BYTES ? 'over 25 MB' : '';
const lines = v => v.split('\n').map(s => s.trim()).filter(Boolean);
const list = v => v.split(',').map(s => s.trim()).filter(Boolean);

function addFiles(files) {
  for (const f of files) if (!queue.some(q => q.name === f.name && q.size === f.size)) queue.push(f);
  renderChips();
}

function renderChips() {
  const ul = $('chips');
  ul.replaceChildren(...queue.map((f, i) => {
    const bad = problem(f);
    const li = document.createElement('li');
    li.className = bad ? 'bad' : '';
    li.textContent = bad ? f.name + ' · ' + bad : f.name;
    const x = document.createElement('button');
    x.type = 'button'; x.textContent = '×'; x.setAttribute('aria-label', 'Remove ' + f.name);
    x.onclick = () => { queue.splice(i, 1); renderChips(); };
    li.append(x);
    return li;
  }));
}

// A name for the batch when the user gave none: the first file or site, plus a count.
function defaultName(urls, files) {
  let first = files[0] ? files[0].name : '';
  if (!first && urls[0]) { try { first = new URL(urls[0]).hostname; } catch { first = urls[0]; } }
  const more = urls.length + files.length - 1;
  return first + (more > 0 ? ' +' + more : '');
}

async function submit(e) {
  e.preventDefault();
  const msg = $('addMsg'), button = $('start');
  const urls = lines($('urls').value), files = queue;
  msg.textContent = ''; msg.className = 'muted';
  if (!urls.length && !files.length) { msg.textContent = 'Add a URL or a file.'; msg.className = 'err'; return; }

  button.disabled = true;
  let batch;
  try {
    batch = await createBatch({
      name: $('name').value.trim() || defaultName(urls, files),
      urls, max_depth: +$('depth').value, path_prefixes: list($('prefixes').value), allowed_hosts: list($('hosts').value),
    });
  } catch (err) { msg.textContent = err.message; msg.className = 'err'; button.disabled = false; return; }

  const items = files.map(f => ({ file: f, name: f.name, error: problem(f), state: problem(f) ? 'failed' : 'queued', pct: 0 }));
  const ingest = { batchId: batch.id, urls: batch.crawls_total, files: items, uploading: items.some(i => i.state === 'queued') };
  $('urls').value = ''; queue = []; renderChips();
  emit('batch:select', batch.id);
  emit('ingest', ingest);

  for (const item of items.filter(i => i.state === 'queued')) {
    item.state = 'uploading';
    emit('ingest', ingest);
    try {
      const r = await uploadFile(batch.id, item.file, (pct, sent) => { item.pct = pct; item.state = sent ? 'reading' : 'uploading'; emit('ingest', ingest); });
      item.state = r.status === 'done' ? 'done' : 'failed'; item.error = r.error || '';
    } catch (err) { item.state = 'failed'; item.error = err.message; }
    emit('ingest', ingest);
    emit('batch:refresh');
  }
  ingest.uploading = false;
  emit('ingest', ingest);
  emit('batch:refresh');
  button.disabled = false;
}

export function init() {
  $('addForm').addEventListener('submit', submit);
  $('pick').onclick = () => $('files').click();
  $('files').onchange = e => { addFiles(e.target.files); e.target.value = ''; };

  const drop = $('drop');
  ['dragenter', 'dragover'].forEach(t => drop.addEventListener(t, e => { e.preventDefault(); drop.classList.add('over'); }));
  ['dragleave', 'drop'].forEach(t => drop.addEventListener(t, e => { e.preventDefault(); drop.classList.remove('over'); }));
  drop.addEventListener('drop', e => addFiles(e.dataTransfer.files));
  // a file dropped outside the zone must not navigate the page away
  ['dragover', 'drop'].forEach(t => window.addEventListener(t, e => e.preventDefault()));
}
