package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/wanderingtilldeath/tessera/internal/batch"
	"github.com/wanderingtilldeath/tessera/internal/frontier"
	"github.com/wanderingtilldeath/tessera/internal/store"
	"github.com/wanderingtilldeath/tessera/internal/testutil"
)

type upload struct{ name, content string }

// post sends files as multipart parts named "files".
func (f *fixture) upload(t *testing.T, path string, files ...upload) (int, map[string]any) {
	t.Helper()

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for _, u := range files {
		part, _ := mw.CreateFormFile("files", u.name)
		_, _ = io.WriteString(part, u.content)
	}
	_ = mw.WriteField("note", "ignored: not a file")
	_ = mw.Close()

	resp, err := http.Post(f.srv.URL+path, mw.FormDataContentType(), &body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)

	return resp.StatusCode, out
}

// embedPending plays the background embedder.
func (f *fixture) embedPending(t *testing.T) {
	t.Helper()

	for {
		n, err := f.store.EmbedPending(context.Background(), 10, testutil.FakeEmbedder{}.EmbedDocuments)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			return
		}
	}
}

func hitURLs(body map[string]any) []string {
	var out []string
	for _, h := range body["hits"].([]any) {
		out = append(out, h.(map[string]any)["url"].(string))
	}
	return out
}

func TestCreateBatchStartsOneCrawlPerURL(t *testing.T) {
	f := newFixture(t)

	status, body, hdr := f.do(t, "POST", "/batches",
		`{"name":"  Docs  ","urls":["http://a.test/one","HTTP://A.test:80/one#frag"," ","http://b.test/two"],"path_prefixes":["docs"]}`)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, body %v", status, body)
	}
	if hdr.Get("Location") != "/batches/1" || body["name"] != "Docs" {
		t.Fatalf("Location %q, body %v", hdr.Get("Location"), body)
	}
	// The duplicate collapses; documents are the listed pages unless links are followed.
	crawls := body["crawls"].([]any)
	if body["crawls_total"] != float64(2) || len(crawls) != 2 || body["done"] != false {
		t.Fatalf("body = %v", body)
	}
	first := crawls[0].(map[string]any)
	if first["max_depth"] != float64(0) || first["batch_id"] != float64(1) || first["seed"] != "http://a.test/one" {
		t.Fatalf("first crawl = %v", first)
	}
	if got := first["path_prefixes"].([]any); len(got) != 1 || got[0] != "/docs" {
		t.Fatalf("path prefixes = %v", got)
	}
	if first["allowed_hosts"].([]any)[0] != "a.test" {
		t.Fatalf("each URL is limited to its own host by default: %v", first)
	}

	// Finishing every seed makes the batch done (no embedding backlog).
	for range 2 {
		items, _ := f.frontier.Claim(context.Background(), 1, 60e9)
		_ = f.frontier.Finish(context.Background(), items[0], frontier.Done, "")
	}
	_, body, _ = f.do(t, "GET", "/batches/1", "")
	if body["done"] != true || body["crawls_done"] != float64(2) {
		t.Fatalf("finished batch = %v", body)
	}

	_, list, _ := f.do(t, "GET", "/batches", "")
	rows := list["batches"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["crawls"] != nil {
		t.Fatalf("the list view should leave out per-crawl detail: %v", list)
	}
}

func TestCreateBatchValidation(t *testing.T) {
	f := newFixture(t)

	tooMany := `"` + strings.Repeat(`http://a.test/x","`, maxBatchURLs) + `http://a.test/y"`
	for name, tc := range map[string]struct{ body, want string }{
		"bad url":       {`{"urls":["http://ok.test/","not a url","ftp://x.test/"]}`, "not a url, ftp://x.test/"},
		"too many":      {`{"urls":[` + tooMany + `]}`, "at most"},
		"bad depth":     {`{"urls":["http://a.test/"],"max_depth":11}`, "max_depth"},
		"long name":     {`{"name":"` + strings.Repeat("n", maxBatchName+1) + `"}`, "name must be"},
		"unknown field": {`{"urls":[],"seeds":[]}`, "invalid JSON"},
	} {
		if status, body, _ := f.do(t, "POST", "/batches", tc.body); status != http.StatusBadRequest || !strings.Contains(body["error"].(string), tc.want) {
			t.Errorf("%s: %d %v, want 400 containing %q", name, status, body, tc.want)
		}
	}

	// A rejected request must not leave a half-made batch behind.
	if _, list, _ := f.do(t, "GET", "/batches", ""); len(list["batches"].([]any)) != 0 {
		t.Fatalf("batches after rejected requests: %v", list)
	}

	// A files-only batch is fine, and gets a default name.
	status, body, _ := f.do(t, "POST", "/batches", `{}`)
	if status != http.StatusCreated || body["name"] != defaultBatchName || body["crawls_total"] != float64(0) {
		t.Fatalf("empty batch: %d %v", status, body)
	}
}

func TestUploadAndSemanticSearch(t *testing.T) {
	f := newFixture(t)
	f.do(t, "POST", "/batches", `{"name":"hr"}`)
	f.do(t, "POST", "/batches", `{"name":"canteen"}`)

	status, out := f.upload(t, "/batches/1/files",
		upload{"policy.md", "# Policy\n\n## Leave\n\nAnnual leave is twenty five days.\n\n## Travel\n\nBook economy flights.\n"},
		upload{"tool.exe", "MZ"},
		upload{"empty.txt", "   "},
	)
	if status != http.StatusOK {
		t.Fatalf("upload status = %d %v", status, out)
	}
	results := out["results"].([]any)
	if len(results) != 3 {
		t.Fatalf("results = %v", results)
	}
	if r := results[0].(map[string]any); r["status"] != "done" || r["chunks"] != float64(2) {
		t.Errorf("policy.md = %v", r)
	}
	for _, i := range []int{1, 2} {
		if r := results[i].(map[string]any); r["status"] != "failed" || r["error"] == "" {
			t.Errorf("result %d should be a reported failure: %v", i, r)
		}
	}

	if status, _ := f.upload(t, "/batches/2/files", upload{"menu.txt", "The canteen serves soup on Tuesdays."}); status != 200 {
		t.Fatal("second upload failed")
	}

	// Until the background embedder runs, nothing is searchable, and the response says so.
	_, body, _ := f.do(t, "GET", "/search/semantic?q=leave&batch=1", "")
	if len(hitURLs(body)) != 0 || body["pending"] != float64(2) {
		t.Fatalf("before embedding: %v", body)
	}
	_, view, _ := f.do(t, "GET", "/batches/1", "")
	if view["done"] != false || view["chunks_total"] != float64(2) || view["chunks_embedded"] != float64(0) {
		t.Fatalf("batch should not be done while chunks wait for embeddings: %v", view)
	}

	f.embedPending(t)

	_, view, _ = f.do(t, "GET", "/batches/1", "")
	docs := view["documents"].([]any)
	if view["done"] != true || len(docs) != 3 || view["embedding_enabled"] != true {
		t.Fatalf("batch after embedding = %v", view)
	}

	// Scoped to batch 1: the policy, not the canteen menu.
	_, body, _ = f.do(t, "GET", "/search/semantic?q=annual+leave+days&batch=1", "")
	urls := hitURLs(body)
	if len(urls) != 2 || urls[0] != "upload://1/policy.md" || body["pending"] != float64(0) {
		t.Fatalf("batch 1 hits = %v", body)
	}
	top := body["hits"].([]any)[0].(map[string]any)
	if top["heading"] != "Policy > Leave" || top["title"] != "Policy" || !strings.Contains(top["text"].(string), "twenty five") {
		t.Fatalf("top hit = %v", top)
	}

	// Scoped to batch 2, then to everything.
	_, body, _ = f.do(t, "GET", "/search/semantic?q=annual+leave+days&batch=2", "")
	if urls := hitURLs(body); len(urls) != 1 || urls[0] != "upload://2/menu.txt" {
		t.Fatalf("batch 2 hits = %v", urls)
	}
	_, body, _ = f.do(t, "GET", "/search/semantic?q=annual+leave+days&limit=2", "")
	if urls := hitURLs(body); len(urls) != 2 || urls[0] != "upload://1/policy.md" {
		t.Fatalf("all-batches hits = %v", urls)
	}
}

func TestUploadErrors(t *testing.T) {
	f := newFixture(t)
	f.do(t, "POST", "/batches", `{}`)

	if status, _ := f.upload(t, "/batches/9/files", upload{"a.txt", "x"}); status != http.StatusNotFound {
		t.Errorf("unknown batch: %d", status)
	}
	if status, _ := f.upload(t, "/batches/1/files"); status != http.StatusBadRequest {
		t.Errorf("no files: %d", status)
	}
	if status, body, _ := f.do(t, "POST", "/batches/1/files", `{"not":"multipart"}`); status != http.StatusBadRequest {
		t.Errorf("non-multipart: %d %v", status, body)
	}
	if status, _, _ := f.do(t, "GET", "/batches/x", ""); status != http.StatusBadRequest {
		t.Errorf("bad id: %d", status)
	}

	// An oversized file is rejected and reported; the others in the upload still go through.
	status, out := f.upload(t, "/batches/1/files",
		upload{"huge.txt", strings.Repeat("a", maxFileBytes+1)},
		upload{"fine.txt", "small and fine"},
	)
	results, _ := out["results"].([]any)
	if status != http.StatusOK || len(results) != 2 {
		t.Fatalf("status %d, out %v", status, out)
	}
	if r := results[0].(map[string]any); r["status"] != "failed" || !strings.Contains(r["error"].(string), "25 MB") {
		t.Errorf("huge.txt = %v", r)
	}
	if r := results[1].(map[string]any); r["status"] != "done" {
		t.Errorf("fine.txt = %v", r)
	}

	// Too many files in one request.
	files := make([]upload, maxUploadFiles+1)
	for i := range files {
		files[i] = upload{"f.txt", "x"}
	}
	if status, _ := f.upload(t, "/batches/1/files", files...); status != http.StatusBadRequest {
		t.Errorf("too many files: %d", status)
	}
}

func TestSemanticSearchErrors(t *testing.T) {
	f := newFixture(t)
	f.do(t, "POST", "/batches", `{}`)

	for path, want := range map[string]int{
		"/search/semantic":                                         http.StatusBadRequest,
		"/search/semantic?q=%20":                                   http.StatusBadRequest,
		"/search/semantic?q=x&limit=0":                             http.StatusBadRequest,
		"/search/semantic?q=x&limit=51":                            http.StatusBadRequest,
		"/search/semantic?q=x&batch=abc":                           http.StatusBadRequest,
		"/search/semantic?q=x&batch=0":                             http.StatusBadRequest,
		"/search/semantic?q=x&batch=99":                            http.StatusNotFound,
		"/search/semantic?q=" + strings.Repeat("x", maxQueryLen+1): http.StatusBadRequest,
		"/search/semantic?q=x&batch=1":                             http.StatusOK,
	} {
		if status, body, _ := f.do(t, "GET", path, ""); status != want {
			t.Errorf("%.60s: %d %v, want %d", path, status, body, want)
		}
	}
}

func TestSemanticSearchNeedsAnEmbedder(t *testing.T) {
	srv := httptest.NewServer(NewHandler(Deps{
		Frontier: frontier.NewMemory(nil), Store: store.NewMemory(), Batches: batch.NewMemory(),
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/search/semantic?q=hello")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&body)

	if resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(body["error"], "EMBED_URL") {
		t.Fatalf("%d %v: want 503 telling the operator to set EMBED_URL", resp.StatusCode, body)
	}
}

func TestBatchRoutesAreOffWithoutARegistry(t *testing.T) {
	srv := httptest.NewServer(NewHandler(Deps{Frontier: frontier.NewMemory(nil), Store: store.NewMemory()}))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/batches")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatal("/batches must not be served without a registry")
	}
}

func TestSemanticSearchWithinChosenDocuments(t *testing.T) {
	f := newFixture(t)
	f.do(t, "POST", "/batches", `{}`)
	f.upload(t, "/batches/1/files",
		upload{"a.txt", "Comets are icy bodies."},
		upload{"b.txt", "Comets have long tails."},
		upload{"c.txt", "Comets orbit the sun."},
	)
	f.embedPending(t)

	// The batch view lists every stored document with its progress.
	_, view, _ := f.do(t, "GET", "/batches/1", "")
	items := view["items"].([]any)
	if len(items) != 3 || items[0].(map[string]any)["url"] != "upload://1/a.txt" || items[0].(map[string]any)["embedded"] != float64(1) {
		t.Fatalf("items = %v", items)
	}

	// Two chosen documents: only they are searched.
	_, body, _ := f.do(t, "GET", "/search/semantic?q=comets&batch=1&url=upload%3A%2F%2F1%2Fa.txt&url=upload%3A%2F%2F1%2Fc.txt", "")
	got := hitURLs(body)
	if len(got) != 2 || strings.Contains(strings.Join(got, ","), "b.txt") {
		t.Fatalf("hits = %v", got)
	}

	// Bad filters are rejected.
	if status, _, _ := f.do(t, "GET", "/search/semantic?q=x&url=", ""); status != http.StatusBadRequest {
		t.Errorf("empty url filter: %d", status)
	}
	many := strings.Repeat("&url=u", maxScopeURLs+1)
	if status, _, _ := f.do(t, "GET", "/search/semantic?q=x"+many, ""); status != http.StatusBadRequest {
		t.Errorf("too many url filters: %d", status)
	}
}

func TestSemanticSearchPagination(t *testing.T) {
	f := newFixture(t)
	f.do(t, "POST", "/batches", `{}`)

	files := make([]upload, 25)
	for i := range files {
		files[i] = upload{name: "doc" + string(rune('a'+i)) + ".txt", content: "comets and more comets " + strings.Repeat("x", i)}
	}
	if status, _ := f.upload(t, "/batches/1/files", files...); status != http.StatusOK {
		t.Fatalf("upload = %d", status)
	}
	f.embedPending(t)

	get := func(query string) (int, map[string]any) {
		status, body, _ := f.do(t, "GET", "/search/semantic?q=comets&batch=1&"+query, "")
		return status, body
	}

	seen := map[string]bool{}
	for page, want := range map[int]int{1: 10, 2: 10, 3: 5} {
		status, body := get("limit=10&page=" + strconv.Itoa(page))
		if status != http.StatusOK {
			t.Fatalf("page %d = %d %v", page, status, body)
		}
		urls := hitURLs(body)
		if len(urls) != want {
			t.Errorf("page %d has %d hits, want %d", page, len(urls), want)
		}
		if body["page"] != float64(page) || body["page_size"] != float64(10) || body["has_more"] != (page < 3) {
			t.Errorf("page %d metadata = page %v size %v has_more %v", page, body["page"], body["page_size"], body["has_more"])
		}
		for _, u := range urls {
			if seen[u] {
				t.Errorf("%s appears on more than one page", u)
			}
			seen[u] = true
		}
	}
	if len(seen) != 25 {
		t.Errorf("pages covered %d distinct documents, want 25", len(seen))
	}

	// Past the end is an empty page, not an error. Page 1 is the default.
	if status, body := get("limit=10&page=4"); status != http.StatusOK || len(hitURLs(body)) != 0 || body["has_more"] != false {
		t.Errorf("page 4: %d %v", status, body)
	}
	if _, body := get("limit=10"); body["page"] != float64(1) {
		t.Errorf("default page = %v", body["page"])
	}

	// A search only goes so deep: the last page the cap allows has no next page, and beyond it is refused.
	if status, body := get("limit=50&page=2"); status != http.StatusOK || body["has_more"] != false {
		t.Errorf("limit 50 page 2: %d has_more %v", status, body["has_more"])
	}
	for _, q := range []string{"limit=10&page=11", "limit=50&page=3", "page=0", "page=abc", "page=-1"} {
		if status, _ := get(q); status != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", q, status)
		}
	}
}
