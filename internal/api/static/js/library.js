// The batch picker and the list of its documents, with selection.
import { listBatches } from './api.js';
import { state, on, emit } from './state.js';
import { watch } from './poller.js';

const $ = id => document.getElementById(id);
const FILTER_FROM = 8;   // below this many documents a filter box is noise

let signature = '';

const label = d => d.url.startsWith('upload://') ? decodeURIComponent(d.url.slice(d.url.lastIndexOf('/') + 1)) : (d.title || d.url);

export async function loadBatches(select = state.batchId) {
  try {
    const batches = await listBatches();
    $('batch').replaceChildren(...batches.map(b => Object.assign(document.createElement('option'), { value: b.id, textContent: b.name })));
    $('batch').hidden = batches.length < 2;
    const id = batches.some(b => b.id === select) ? select : batches[0] && batches[0].id;
    if (id === undefined) { state.batchId = null; render(null); return; }
    $('batch').value = id;
    choose(id);
  } catch (e) { emit('error', e.message); }
}

function choose(id) {
  if (id !== state.batchId) { state.picked.clear(); signature = ''; emit('picked'); }
  watch(id);
}

function docs(batch) {
  const q = $('filter').value.trim().toLowerCase();
  const items = batch ? batch.items || [] : [];
  return q ? items.filter(d => (label(d) + ' ' + d.url).toLowerCase().includes(q)) : items;
}

function render(batch) {
  const all = batch ? batch.items || [] : [];
  const shown = docs(batch);
  const failed = batch ? (batch.documents || []).filter(d => d.status === 'failed') : [];
  $('filter').hidden = all.length < FILTER_FROM;

  const sig = JSON.stringify([shown.map(d => [d.url, d.embedded < d.chunks]), failed.map(d => d.id), [...state.picked]]);
  if (sig !== signature) {
    signature = sig;
    const rows = shown.map(d => {
      const li = document.createElement('li');
      const box = Object.assign(document.createElement('input'), { type: 'checkbox', checked: state.picked.has(d.url) });
      box.dataset.url = d.url;
      const name = Object.assign(document.createElement('span'), { className: 'nm', textContent: label(d), title: d.url });
      li.append(box, name);
      if (d.embedded < d.chunks) li.append(Object.assign(document.createElement('span'), { className: 'spin', title: 'embedding' }));
      return li;
    });
    for (const d of failed) {
      rows.push(Object.assign(document.createElement('li'), { className: 'bad', textContent: d.name + ': ' + d.error }));
    }
    if (!rows.length) rows.push(Object.assign(document.createElement('li'), { className: 'empty', textContent: all.length ? 'No match' : 'No documents yet' }));
    $('docs').replaceChildren(...rows);
  }

  const all$ = $('all');
  const chosen = shown.filter(d => state.picked.has(d.url)).length;
  all$.checked = shown.length > 0 && chosen === shown.length;
  all$.indeterminate = chosen > 0 && chosen < shown.length;
}

function changed(url, on_) {
  if (on_) state.picked.add(url); else state.picked.delete(url);
  emit('picked');
}

export function init() {
  on('batch', render);
  on('batch:select', id => loadBatches(id));
  on('batch:refresh', () => watch());

  $('batch').onchange = e => choose(+e.target.value);
  $('filter').oninput = () => render(state.batch);
  $('docs').addEventListener('change', e => e.target.dataset.url && changed(e.target.dataset.url, e.target.checked));
  $('docs').addEventListener('click', e => {           // clicking anywhere on a row toggles it
    const li = e.target.closest('li'), box = li && li.querySelector('input');
    if (box && e.target !== box) { box.checked = !box.checked; changed(box.dataset.url, box.checked); }
  });
  $('all').onchange = e => {
    docs(state.batch).forEach(d => e.target.checked ? state.picked.add(d.url) : state.picked.delete(d.url));
    signature = ''; render(state.batch); emit('picked');
  };
  on('picked', () => { signature = ''; render(state.batch); });
}
