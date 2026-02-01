package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestStripPrefixReverseProxy_ForwardsPathAndQuery(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(r.URL.Path + "?" + r.URL.RawQuery))
	}))
	t.Cleanup(target.Close)

	targetURL, err := url.Parse(target.URL)
	if err != nil {
		t.Fatalf("parse target url: %v", err)
	}

	stripPrefix := "/branches/alice/demo/main/ports/3000"
	proxy := newStripPrefixReverseProxy(targetURL, stripPrefix)

	req := httptest.NewRequest("GET", stripPrefix+"/foo/bar?x=1&y=two", nil)
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	res := rec.Result()
	t.Cleanup(func() { _ = res.Body.Close() })
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want=%d", res.StatusCode, http.StatusOK)
	}
	body, _ := io.ReadAll(res.Body)
	if got, want := string(body), "/foo/bar?x=1&y=two"; got != want {
		t.Fatalf("body=%q, want=%q", got, want)
	}
}

func TestStripPrefixReverseProxy_ForwardsRootWhenNoSuffix(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(r.URL.Path))
	}))
	t.Cleanup(target.Close)

	targetURL, err := url.Parse(target.URL)
	if err != nil {
		t.Fatalf("parse target url: %v", err)
	}

	stripPrefix := "/branches/alice/demo/main/ports/3000"
	proxy := newStripPrefixReverseProxy(targetURL, stripPrefix)

	req := httptest.NewRequest("GET", stripPrefix, nil)
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	res := rec.Result()
	t.Cleanup(func() { _ = res.Body.Close() })
	body, _ := io.ReadAll(res.Body)
	if got, want := string(body), "/"; got != want {
		t.Fatalf("body=%q, want=%q", got, want)
	}
}
