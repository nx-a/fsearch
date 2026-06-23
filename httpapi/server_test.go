package httpapi_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/nx-a/fsearch"
	"github.com/nx-a/fsearch/httpapi"
)

type Prefix struct {
	Prefix string
}

func (p *Prefix) GetServiceContext() string {
	return p.Prefix
}
func (p *Prefix) IsReadOnly() bool { return true }

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	e, err := fsearch.Open(fsearch.Options{Path: filepath.Join(t.TempDir(), "api.db")})
	if err != nil {
		t.Fatalf("open engine: %v", err)
	}
	srv := httptest.NewServer(httpapi.NewServer(e, &Prefix{Prefix: "api"}))
	t.Cleanup(func() {
		srv.Close()
		e.Close()
	})
	return srv
}

func doJSON(t *testing.T, method, url string, body any) (*http.Response, []byte) {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := new(bytes.Buffer)
	buf.ReadFrom(resp.Body)
	return resp, buf.Bytes()
}

func TestHTTPSwaggerDocs(t *testing.T) {
	srv := newTestServer(t)

	resp, body := doJSON(t, "GET", srv.URL+"/openapi.yaml", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("openapi.yaml: status %d", resp.StatusCode)
	}
	if !bytes.Contains(body, []byte("openapi: 3.0")) {
		t.Fatalf("openapi.yaml does not look like a spec: %.40s", body)
	}
	// Спека формируется налёту и должна отражать префикс.
	if !bytes.Contains(body, []byte("/api/{sysname}/search")) {
		t.Fatalf("spec does not reflect channel prefix:\n%s", body)
	}
	if bytes.Contains(body, []byte("/lists")) {
		t.Fatalf("spec still contains hardcoded /lists path")
	}

	resp, body = doJSON(t, "GET", srv.URL+"/swagger-ui", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("swagger-ui: status %d", resp.StatusCode)
	}
	if !bytes.Contains(body, []byte("swagger-ui")) {
		t.Fatalf("docs page does not embed swagger ui")
	}
}

func itoa(u uint64) string {
	return strconv.FormatUint(u, 10)
}
