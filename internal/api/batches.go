package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/wanderingtilldeath/docs-rag-ingest/internal/batch"
	"github.com/wanderingtilldeath/docs-rag-ingest/internal/frontier"
	"github.com/wanderingtilldeath/docs-rag-ingest/internal/ingest"
	"github.com/wanderingtilldeath/docs-rag-ingest/internal/store"
	"github.com/wanderingtilldeath/docs-rag-ingest/internal/urlnorm"
)

const (
	maxBatchURLs   = 100
	maxBatchName   = 100
	maxUploadFiles = 50
	maxFileBytes   = 25 << 20
	maxUploadBytes = 200 << 20
	maxQueryLen    = 1000
	maxItems       = 500
	// maxResults bounds how deep a search can page. Vector search has no natural
	// end: every embedded chunk is "a match", so results are capped.
	maxResults       = 100
	maxScopeURLs     = 100
	semanticTimeout  = 30 * time.Second
	defaultBatchName = "Untitled batch"
)

type createBatchRequest struct {
	Name         string   `json:"name"`
	URLs         []string `json:"urls"`
	MaxDepth     *int     `json:"max_depth"`
	AllowedHosts []string `json:"allowed_hosts"`
	PathPrefixes []string `json:"path_prefixes"`
}

type batchResponse struct {
	batch.Batch
	// CrawlsTotal and CrawlsDone count the batch's URL crawls.
	CrawlsTotal int `json:"crawls_total"`
	CrawlsDone  int `json:"crawls_done"`
	// Stats and Total sum the frontier counts of all the batch's crawls.
	Stats map[string]int64 `json:"stats"`
	Total int64            `json:"total"`
	// Chunks counts the batch's text chunks and how many have an embedding.
	ChunksTotal      int64 `json:"chunks_total"`
	ChunksEmbedded   int64 `json:"chunks_embedded"`
	EmbeddingEnabled bool  `json:"embedding_enabled"`
	// Done is true when every crawl has finished, every upload is processed
	// and (if embedding is on) every chunk is embedded.
	Done      bool             `json:"done"`
	Crawls    []crawlResponse  `json:"crawls,omitempty"`
	Documents []batch.Document `json:"documents,omitempty"`
	// Items are the stored documents (crawled pages and uploaded files) with
	// their passage counts, up to maxItems, so the page can show them live.
	Items []store.DocInfo `json:"items,omitempty"`
}

func (s *server) createBatch(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)

	var req createBatchRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = defaultBatchName
	}
	if utf8.RuneCountInString(name) > maxBatchName {
		writeError(w, http.StatusBadRequest, "name must be at most "+strconv.Itoa(maxBatchName)+" characters")
		return
	}

	// Documents are the listed pages themselves unless the caller asks to follow links.
	maxDepth := 0
	if req.MaxDepth != nil {
		maxDepth = *req.MaxDepth
	}
	if maxDepth < 0 || maxDepth > maxDepthLimit {
		writeError(w, http.StatusBadRequest, "max_depth must be between 0 and "+strconv.Itoa(maxDepthLimit))
		return
	}

	if len(req.URLs) > maxBatchURLs {
		writeError(w, http.StatusBadRequest, "at most "+strconv.Itoa(maxBatchURLs)+" URLs per batch")
		return
	}
	seeds, bad := canonicalURLs(req.URLs)
	if len(bad) > 0 {
		writeError(w, http.StatusBadRequest, "not absolute http(s) URLs: "+strings.Join(bad, ", "))
		return
	}

	b, err := s.Batches.CreateBatch(r.Context(), name)
	if err != nil {
		s.Logger.Error("create batch failed", "error", err)
		writeError(w, http.StatusInternalServerError, "could not create batch")
		return
	}

	for _, seed := range seeds {
		if _, err := frontier.StartCrawlInBatch(r.Context(), s.Frontier, b.ID, seed, maxDepth, req.AllowedHosts, req.PathPrefixes); err != nil {
			s.Logger.Error("start batch crawl failed", "batch", b.ID, "seed", seed, "error", err)
			writeError(w, http.StatusInternalServerError, "could not start crawl for "+seed)
			return
		}
	}

	w.Header().Set("Location", "/batches/"+strconv.FormatInt(b.ID, 10))
	s.writeBatch(w, r, http.StatusCreated, b, true)
}

// canonicalURLs canonicalises and de-duplicates raw, keeping order. Blank
// entries are ignored; entries that are not absolute http(s) URLs are
// returned in bad.
func canonicalURLs(raw []string) (urls, bad []string) {
	seen := map[string]bool{}
	for _, u := range raw {
		if u = strings.TrimSpace(u); u == "" {
			continue
		}
		c, err := urlnorm.Canonicalize(u)
		if err != nil {
			bad = append(bad, u)
			continue
		}
		if !seen[c] {
			seen[c] = true
			urls = append(urls, c)
		}
	}

	return urls, bad
}

func (s *server) batchID(w http.ResponseWriter, r *http.Request) (batch.Batch, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "batch id must be a positive integer")
		return batch.Batch{}, false
	}

	b, err := s.Batches.GetBatch(r.Context(), id)
	if errors.Is(err, batch.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no such batch")
		return batch.Batch{}, false
	}
	if err != nil {
		s.Logger.Error("get batch failed", "error", err)
		writeError(w, http.StatusInternalServerError, "could not load batch")
		return batch.Batch{}, false
	}

	return b, true
}

func (s *server) getBatch(w http.ResponseWriter, r *http.Request) {
	if b, ok := s.batchID(w, r); ok {
		s.writeBatch(w, r, http.StatusOK, b, true)
	}
}

func (s *server) listBatches(w http.ResponseWriter, r *http.Request) {
	batches, err := s.Batches.ListBatches(r.Context(), 20)
	if err != nil {
		s.Logger.Error("list batches failed", "error", err)
		writeError(w, http.StatusInternalServerError, "could not list batches")
		return
	}

	out := make([]batchResponse, 0, len(batches))
	for _, b := range batches {
		view, err := s.batchView(r.Context(), b, false)
		if err != nil {
			s.Logger.Error("batch view failed", "error", err)
			writeError(w, http.StatusInternalServerError, "could not load batch")
			return
		}
		out = append(out, view)
	}

	writeJSON(w, http.StatusOK, map[string]any{"batches": out})
}

func (s *server) writeBatch(w http.ResponseWriter, r *http.Request, status int, b batch.Batch, detail bool) {
	view, err := s.batchView(r.Context(), b, detail)
	if err != nil {
		s.Logger.Error("batch view failed", "error", err)
		writeError(w, http.StatusInternalServerError, "could not load batch")
		return
	}

	writeJSON(w, status, view)
}

// batchView gathers a batch's progress. detail adds the per-crawl and
// per-file lists, which the batch list leaves out.
func (s *server) batchView(ctx context.Context, b batch.Batch, detail bool) (batchResponse, error) {
	crawls, err := s.Frontier.BatchCrawls(ctx, b.ID)
	if err != nil {
		return batchResponse{}, err
	}
	docs, err := s.Batches.Documents(ctx, b.ID)
	if err != nil {
		return batchResponse{}, err
	}
	chunks, err := s.Store.EmbeddingStats(ctx, b.ID)
	if err != nil {
		return batchResponse{}, err
	}

	resp := batchResponse{
		Batch: b, CrawlsTotal: len(crawls), Stats: make(map[string]int64, len(frontier.AllStatuses)),
		ChunksTotal: chunks.Total, ChunksEmbedded: chunks.Embedded, EmbeddingEnabled: s.Embedder != nil,
	}
	for _, st := range frontier.AllStatuses {
		resp.Stats[string(st)] = 0
	}

	for _, c := range crawls {
		view, err := s.crawlView(ctx, c)
		if err != nil {
			return batchResponse{}, err
		}
		for k, v := range view.Stats {
			resp.Stats[k] += v
		}
		resp.Total += view.Total
		if view.Done {
			resp.CrawlsDone++
		}
		if detail {
			resp.Crawls = append(resp.Crawls, view)
		}
	}

	processing := false
	for _, d := range docs {
		processing = processing || d.Status == batch.Processing
	}
	embedded := s.Embedder == nil || chunks.Embedded == chunks.Total
	resp.Done = len(crawls)+len(docs) > 0 && resp.CrawlsDone == len(crawls) && !processing && embedded

	if detail {
		resp.Documents = docs
		if resp.Items, err = s.Store.BatchDocuments(ctx, b.ID, maxItems); err != nil {
			return batchResponse{}, err
		}
	}

	return resp, nil
}

type uploadResponse struct {
	Results []ingest.Result `json:"results"`
}

// uploadFiles ingests the files of a multipart/form-data request into a
// batch. Each file is read and stored in turn; a file that cannot be read
// (wrong type, too large, no text) is reported in its result and does not
// stop the others.
func (s *server) uploadFiles(w http.ResponseWriter, r *http.Request) {
	b, ok := s.batchID(w, r)
	if !ok {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	mr, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "expected a multipart/form-data upload")
		return
	}

	results := []ingest.Result{}
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				writeError(w, http.StatusRequestEntityTooLarge, "upload is larger than "+strconv.Itoa(maxUploadBytes>>20)+" MB")
				return
			}
			writeError(w, http.StatusBadRequest, "invalid multipart body: "+err.Error())
			return
		}
		if part.FileName() == "" {
			continue
		}
		if len(results) >= maxUploadFiles {
			writeError(w, http.StatusBadRequest, "at most "+strconv.Itoa(maxUploadFiles)+" files per upload")
			return
		}

		data, err := io.ReadAll(io.LimitReader(part, maxFileBytes+1))
		if err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				writeError(w, http.StatusRequestEntityTooLarge, "upload is larger than "+strconv.Itoa(maxUploadBytes>>20)+" MB")
				return
			}
			writeError(w, http.StatusBadRequest, "could not read upload: "+err.Error())
			return
		}

		if len(data) > maxFileBytes {
			results = append(results, s.rejectUpload(r.Context(), b.ID, part.FileName(),
				"file is larger than "+strconv.Itoa(maxFileBytes>>20)+" MB"))
			continue
		}

		res, err := ingest.Process(r.Context(), s.Store, s.Batches, b.ID, part.FileName(), data)
		if err != nil {
			s.Logger.Error("ingest failed", "batch", b.ID, "file", part.FileName(), "error", err)
			writeError(w, http.StatusInternalServerError, "could not store "+part.FileName())
			return
		}
		results = append(results, res)
	}

	if len(results) == 0 {
		writeError(w, http.StatusBadRequest, "no files in upload (send them as multipart parts named \"files\")")
		return
	}

	writeJSON(w, http.StatusOK, uploadResponse{Results: results})
}

// rejectUpload records a file that was refused before extraction as failed.
func (s *server) rejectUpload(ctx context.Context, batchID int64, name, reason string) ingest.Result {
	res := ingest.Result{Name: name, Status: batch.Failed, Error: reason}

	kind := ingest.KindOf(name)
	if kind == "" {
		kind = "unknown"
	}
	id, err := s.Batches.AddDocument(ctx, batchID, name, kind)
	if err == nil {
		err = s.Batches.FinishDocument(ctx, id, batch.Failed, 0, reason)
	}
	if err != nil {
		s.Logger.Error("record rejected upload failed", "batch", batchID, "file", name, "error", err)
	}

	return res
}

// searchSemantic embeds the query and returns the closest chunks, in one
// batch or across all of them.
func (s *server) searchSemantic(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeError(w, http.StatusBadRequest, "q is required")
		return
	}
	if utf8.RuneCountInString(q) > maxQueryLen {
		writeError(w, http.StatusBadRequest, "q must be at most "+strconv.Itoa(maxQueryLen)+" characters")
		return
	}

	limit := 10
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 50 {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and 50")
			return
		}
		limit = n
	}

	page := 1
	if v := r.URL.Query().Get("page"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "page must be a positive integer")
			return
		}
		page = n
	}
	offset := (page - 1) * limit
	if offset >= maxResults {
		writeError(w, http.StatusBadRequest, "search returns at most "+strconv.Itoa(maxResults)+" results; page is out of range")
		return
	}

	var batchID int64
	if v := r.URL.Query().Get("batch"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			writeError(w, http.StatusBadRequest, "batch must be a positive integer")
			return
		}
		if _, err := s.Batches.GetBatch(r.Context(), id); errors.Is(err, batch.ErrNotFound) {
			writeError(w, http.StatusNotFound, "no such batch")
			return
		} else if err != nil {
			s.Logger.Error("semantic search: get batch failed", "error", err)
			writeError(w, http.StatusInternalServerError, "search failed")
			return
		}
		batchID = id
	}

	// url=... (repeatable) narrows the search to chosen documents.
	urls := r.URL.Query()["url"]
	if len(urls) > maxScopeURLs {
		writeError(w, http.StatusBadRequest, "at most "+strconv.Itoa(maxScopeURLs)+" url filters")
		return
	}
	for _, u := range urls {
		if u == "" || len(u) > 2048 {
			writeError(w, http.StatusBadRequest, "url filters must be non-empty and at most 2048 characters")
			return
		}
	}
	scope := store.Scope{BatchID: batchID, URLs: urls}

	if s.Embedder == nil {
		writeError(w, http.StatusServiceUnavailable, "semantic search is not configured: set EMBED_URL to an Ollama server")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), semanticTimeout)
	defer cancel()

	vec, err := s.Embedder.EmbedQuery(ctx, q)
	if err != nil {
		s.Logger.Error("embed query failed", "error", err)
		writeError(w, http.StatusBadGateway, "the embedding service is unavailable; try again shortly")
		return
	}

	// Ask for one more than the page holds: its presence is how we know there is a next page.
	// The last page the cap allows never has one.
	want := min(limit, maxResults-offset)
	fetch := want
	if offset+want < maxResults {
		fetch++
	}
	hits, err := s.Store.SearchChunks(ctx, vec, scope, fetch, offset)
	if err != nil {
		s.Logger.Error("semantic search failed", "error", err)
		writeError(w, http.StatusInternalServerError, "search failed")
		return
	}
	stats, err := s.Store.EmbeddingStats(ctx, batchID)
	if err != nil {
		s.Logger.Error("embedding stats failed", "error", err)
		writeError(w, http.StatusInternalServerError, "search failed")
		return
	}

	hasMore := len(hits) > want
	if hasMore {
		hits = hits[:want]
	}
	if hits == nil {
		hits = []store.PassageHit{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"query": q, "batch": batchID, "hits": hits, "pending": stats.Total - stats.Embedded,
		"page": page, "page_size": limit, "has_more": hasMore,
	})
}
