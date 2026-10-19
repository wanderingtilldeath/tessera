// The only module that talks to the server.

async function json(path, opts) {
  const res = await fetch(path, opts);
  const body = await res.json();
  if (!res.ok) throw new Error(body.error || res.statusText);
  return body;
}

export const listBatches = () => json('/batches').then(r => r.batches);
export const getBatch = id => json('/batches/' + id);

export const createBatch = body => json('/batches', {
  method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
});

export function search({ q, batch, urls = [], page = 1 }) {
  const p = new URLSearchParams({ q, page });
  if (batch) p.set('batch', batch);
  urls.forEach(u => p.append('url', u));
  return json('/search/semantic?' + p);
}

// XMLHttpRequest, because fetch cannot report upload progress.
// onProgress(fraction, sent): sent is true once the bytes are uploaded and the server is reading the text.
export function uploadFile(batchId, file, onProgress) {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open('POST', '/batches/' + batchId + '/files');
    xhr.responseType = 'json';
    xhr.upload.onprogress = e => e.lengthComputable && onProgress(e.loaded / e.total, false);
    xhr.upload.onload = () => onProgress(1, true);
    xhr.onerror = () => reject(new Error('network error'));
    xhr.onload = () => {
      const body = xhr.response || {};
      if (xhr.status !== 200) return reject(new Error(body.error || 'upload failed'));
      resolve(body.results[0]);
    };
    const form = new FormData();
    form.append('files', file, file.name);
    xhr.send(form);
  });
}
