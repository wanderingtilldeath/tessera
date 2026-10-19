// Keeps the selected batch fresh. It fetches once, then every second only while the batch is
// still working and the tab is visible. A finished batch is never polled.
import { getBatch } from './api.js';
import { state, emit } from './state.js';

let generation = 0, timer;

export function watch(id = state.batchId) {
  clearTimeout(timer);
  state.batchId = id;
  if (id != null) tick();
}

async function tick() {
  const mine = ++generation, id = state.batchId;
  try {
    const batch = await getBatch(id);
    if (mine !== generation) return;
    state.batch = batch;
    emit('batch', batch);
    if (!batch.done && !document.hidden) timer = setTimeout(tick, 1000);
  } catch (e) {
    if (mine === generation) emit('error', e.message);
  }
}
