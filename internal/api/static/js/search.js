// The question box. Runs a search and moves between its pages; drawing the answers is results.js.
import { search } from './api.js';
import { state, on } from './state.js';
import { renderResults } from './results.js';

const $ = id => document.getElementById(id);
const MAX_PICKED = 100;

let current = null;   // the search being paged: the question and scope are fixed when it starts

function scopeText() {
  if (state.picked.size) return 'Searching ' + state.picked.size + (state.picked.size === 1 ? ' selected document' : ' selected documents');
  if (state.batch) return 'Searching all of “' + state.batch.name + '”';
  return 'Add documents to search them.';
}

function message(text, cls) {
  $('results').replaceChildren(Object.assign(document.createElement('p'), { className: cls, textContent: text }));
}

async function run(page) {
  const out = $('results'), button = $('searchBtn');
  button.disabled = true;
  out.replaceChildren(Object.assign(document.createElement('span'), { className: 'spin' }));
  try {
    const body = await search({ ...current, page });
    renderResults(out, body, run);
  } catch (err) { message(err.message, 'err'); }
  finally { button.disabled = false; }
}

function submit(e) {
  e.preventDefault();
  if (!state.batchId) return message('Add documents first.', 'muted');
  if (state.picked.size > MAX_PICKED) return message('Select at most ' + MAX_PICKED + ' documents.', 'err');
  current = { q: $('q').value, batch: state.batchId, urls: [...state.picked] };
  run(1);
}

export function init() {
  $('searchForm').addEventListener('submit', submit);
  const update = () => { $('scope').textContent = scopeText(); };
  on('batch', update); on('picked', update);
  update();
}
