// Shared state and a tiny event bus. Modules never touch each other's DOM; they talk through this.
//
// Events:
//   batch         the selected batch's latest data (from the poller)
//   batch:select  id: show this batch (e.g. a new one)
//   batch:refresh fetch the selected batch again
//   picked        the document selection changed
//   ingest        the upload/ingest state of the batch being added
//   error         message: something failed

export const state = { batchId: null, batch: null, picked: new Set() };

const handlers = {};
export const on = (event, fn) => (handlers[event] ||= []).push(fn);
export const emit = (event, data) => (handlers[event] || []).forEach(fn => fn(data));
