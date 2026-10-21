// Wires the modules together. No logic of its own.
import { on } from './state.js';
import { init as initAdd } from './add.js';
import { init as initProgress } from './progress.js';
import { init as initLibrary, loadBatches } from './library.js';
import { init as initSearch } from './search.js';

const error = document.getElementById('error');

on('error', msg => { error.textContent = msg; error.hidden = false; });
on('batch', () => { error.hidden = true; });

initAdd(); initProgress(); initLibrary(); initSearch();
loadBatches();

// Nothing polls in the background; coming back to the tab fetches the latest once.
document.addEventListener('visibilitychange', () => { if (!document.hidden) loadBatches(); });
