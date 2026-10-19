// Draws one page of search results and its pager. Knows nothing about the server or the page's other parts.
//
// A result has two clearly different parts: the matched text (a quoted block) and its source
// (a link, or a plain "file" label when the document was uploaded and there is nothing to open).

const el = (tag, cls, text) => {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text !== undefined) e.textContent = text;
  return e;
};

// "https://example.com/docs/intro/" -> "example.com/docs/intro"
const shortUrl = u => u.replace(/^https?:\/\//, '').replace(/\/$/, '');
const fileName = u => decodeURIComponent(u.slice(u.lastIndexOf('/') + 1));

function source(hit) {
  const row = el('div', 'source');
  if (hit.url.startsWith('upload://')) {
    row.append(el('span', 'tag', 'File'), el('span', 'file', fileName(hit.url)));
  } else if (/^https?:\/\//.test(hit.url)) {
    const a = el('a', 'ext', shortUrl(hit.url));
    Object.assign(a, { href: hit.url, target: '_blank', rel: 'noopener', title: hit.url });
    row.append(el('span', 'tag', 'Link'), a);
  }
  return row;
}

function quote(text) {
  const box = el('blockquote', 'quote');
  const body = el('p', '', text);
  box.append(body);
  return { box, body };
}

function hit(h) {
  const art = el('article', 'hit');
  const head = el('div', 'head');
  head.append(el('h3', 'title', h.title || h.url));
  if (h.heading) head.append(el('span', 'where', h.heading));

  const { box, body } = quote(h.text);
  art.append(head, box, source(h));

  // Long passages are clipped; offer the rest only when something is actually hidden.
  requestAnimationFrame(() => {
    if (body.scrollHeight <= body.clientHeight + 1) return;
    const more = el('button', 'link', 'Show more');
    more.type = 'button';
    more.onclick = () => { const open = body.classList.toggle('open'); more.textContent = open ? 'Show less' : 'Show more'; };
    box.append(more);
  });

  return art;
}

function pager(body, onPage) {
  const nav = el('nav', 'pager');
  nav.setAttribute('aria-label', 'Result pages');

  const prev = el('button', 'ghost', '‹ Previous'), next = el('button', 'ghost', 'Next ›');
  prev.type = next.type = 'button';
  prev.disabled = body.page <= 1;
  next.disabled = !body.has_more;
  prev.onclick = () => onPage(body.page - 1);
  next.onclick = () => onPage(body.page + 1);

  const from = (body.page - 1) * body.page_size + 1;
  nav.append(prev, el('span', 'muted', from + '–' + (from + body.hits.length - 1)), next);
  return nav;
}

// onPage(n) is called with the page the user asked for.
export function renderResults(container, body, onPage) {
  const out = [];
  if (body.pending > 0) out.push(el('p', 'muted', body.pending.toLocaleString() + ' passages are still being embedded; results may be incomplete.'));

  if (!body.hits.length) {
    out.push(el('p', 'muted', body.page > 1 ? 'No more results.' : 'No results.'));
    if (body.page > 1) out.push(pager(body, onPage));
  } else {
    out.push(...body.hits.map(hit));
    if (body.page > 1 || body.has_more) out.push(pager(body, onPage));
  }

  container.replaceChildren(...out);
  if (body.page > 1) container.scrollIntoView({ block: 'start' });
}
