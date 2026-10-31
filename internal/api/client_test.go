package api

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testClient(url string) *Client {
	c := New(url, "secret-key", "test")
	c.Sleep = func(context.Context, time.Duration) error { return nil }
	return c
}

func TestSendsTheKeyInTheHeader(t *testing.T) {
	var got http.Header
	var query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, query = r.Header.Clone(), r.URL.RawQuery
	}))
	defer srv.Close()
	if _, err := testClient(srv.URL).Do(context.Background(), Request{Method: http.MethodGet, Path: "/x"}); err != nil {
		t.Fatal(err)
	}
	if got.Get(HeaderKey) != "secret-key" {
		t.Errorf("%s = %q", HeaderKey, got.Get(HeaderKey))
	}
	if strings.Contains(query, "secret") {
		t.Errorf("the key reached the URL: %s", query)
	}
}

func TestKeyDoesNotFollowARedirectToAnotherHost(t *testing.T) {
	var foreign http.Header
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		foreign = r.Header.Clone()
	}))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/landed", http.StatusFound)
	}))
	defer srv.Close()
	if _, err := testClient(srv.URL).Do(context.Background(), Request{Method: http.MethodGet, Path: "/x"}); err != nil {
		t.Fatal(err)
	}
	if foreign == nil {
		t.Fatal("the redirect was not followed")
	}
	if v := foreign.Get(HeaderKey); v != "" {
		t.Errorf("the key was sent to another host: %q", v)
	}
}

func TestKeyFollowsARedirectOnTheSameHost(t *testing.T) {
	var landed string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/x" {
			http.Redirect(w, r, "/y", http.StatusFound)
			return
		}
		landed = r.Header.Get(HeaderKey)
	}))
	defer srv.Close()
	if _, err := testClient(srv.URL).Do(context.Background(), Request{Method: http.MethodGet, Path: "/x"}); err != nil {
		t.Fatal(err)
	}
	if landed != "secret-key" {
		t.Errorf("key on the same host = %q", landed)
	}
}

func TestCheckPath(t *testing.T) {
	for _, p := range []string{"https://evil.example/x", "//evil.example/x", "x", "", "/a/../b", "/.."} {
		if CheckPath(p) == nil {
			t.Errorf("%q should be refused", p)
		}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Errorf("%q reached the network", p)
		}))
		_, err := testClient(srv.URL).Do(context.Background(), Request{Method: http.MethodGet, Path: p})
		srv.Close()
		if err == nil {
			t.Errorf("Do(%q) should fail", p)
		}
	}
	for _, p := range []string{"/billing/balance", "/genai/v1beta/models/gemini-3.1-pro:generateContent", "/x?a=b"} {
		if err := CheckPath(p); err != nil {
			t.Errorf("%q: %v", p, err)
		}
	}
}

func TestGetRetriesAndHonoursRetryAfter(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"message":"Too many requests"}`)
			return
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()
	c := testClient(srv.URL)
	var waits []time.Duration
	c.Sleep = func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		return nil
	}
	resp, err := c.Do(context.Background(), Request{Method: http.MethodGet, Path: "/x"})
	if err != nil {
		t.Fatal(err)
	}
	if string(resp.Body) != `{"ok":true}` || calls != 3 {
		t.Errorf("body %s after %d calls", resp.Body, calls)
	}
	if len(waits) != 2 || waits[0] != 7*time.Second {
		t.Errorf("waits = %v", waits)
	}
}

func TestWriteIsNotRetried(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	_, err := testClient(srv.URL).Do(context.Background(), Request{Method: http.MethodPost, Path: "/x", Body: []byte(`{}`)})
	if err == nil {
		t.Fatal("expected an error")
	}
	if calls != 1 {
		t.Errorf("a write was sent %d times: a repeat could spend money twice", calls)
	}
}

func TestClientErrorIsNotRetried(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	if _, err := testClient(srv.URL).Do(context.Background(), Request{Method: http.MethodGet, Path: "/x"}); err == nil {
		t.Fatal("expected an error")
	}
	if calls != 1 {
		t.Errorf("a 404 was tried %d times", calls)
	}
}

func TestErrorShapes(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		message string
		code    string
		auth    bool
		limited bool
	}{
		{"request level", 422, `{"message":"duration: too long"}`, "duration: too long", "", false, false},
		{"invalid key", 401, `{"message":"Invalid API key"}`, "Invalid API key", "", true, false},
		{"inactive key", 403, `{"message":"API key inactive"}`, "API key inactive", "", true, false},
		{"failed job envelope", 403, `{"id":"media_1","status":"failed","error":{"code":"insufficient_balance","message":"Insufficient org balance"}}`, "Insufficient org balance", "insufficient_balance", false, false},
		{"rate limit", 429, `{"id":"media_1","status":"failed","error":{"code":"rate_limited","message":"rate limit exceeded: rpm"}}`, "rate limit exceeded: rpm", "rate_limited", false, true},
		{"upstream shape", 400, `{"type":"error","error":{"type":"invalid_request_error","message":"bad"}}`, "bad", "invalid_request_error", false, false},
		{"plain error", 500, `{"error":"boom"}`, "boom", "", false, false},
		{"not json", 502, `<html>bad gateway</html>`, "<html>bad gateway</html>", "", false, false},
		{"empty", 503, ``, "Service Unavailable", "", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := NewError(tt.status, http.Header{}, []byte(tt.body))
			if e.Message != tt.message || e.Code != tt.code {
				t.Errorf("got message %q code %q", e.Message, e.Code)
			}
			if e.Auth() != tt.auth || e.RateLimited() != tt.limited {
				t.Errorf("auth %v limited %v", e.Auth(), e.RateLimited())
			}
		})
	}
}

func TestFetchSendsTheKeyOnlyToTheAPIHost(t *testing.T) {
	var apiKey, cdnKey string
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cdnKey = r.Header.Get(HeaderKey)
		_, _ = io.WriteString(w, "cdn file")
	}))
	defer cdn.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiKey = r.Header.Get(HeaderKey)
		_, _ = io.WriteString(w, "api file")
	}))
	defer srv.Close()
	c := testClient(srv.URL)

	var buf bytes.Buffer
	if _, err := c.Fetch(context.Background(), srv.URL+"/v1/files/a.mp4", &buf); err != nil {
		t.Fatal(err)
	}
	if apiKey != "secret-key" || buf.String() != "api file" {
		t.Errorf("API host: key %q body %q", apiKey, buf.String())
	}
	buf.Reset()
	if _, err := c.Fetch(context.Background(), cdn.URL+"/a.mp4", &buf); err != nil {
		t.Fatal(err)
	}
	if cdnKey != "" {
		t.Errorf("the key was sent to the CDN: %q", cdnKey)
	}
	if buf.String() != "cdn file" {
		t.Errorf("body %q", buf.String())
	}
}

func TestPutSignedDeclaresLengthAndSendsNoKey(t *testing.T) {
	var length int64
	var chunked []string
	var contentType, key, body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		length, chunked = r.ContentLength, r.TransferEncoding
		contentType, key = r.Header.Get("Content-Type"), r.Header.Get(HeaderKey)
		data, _ := io.ReadAll(r.Body)
		body = string(data)
	}))
	defer srv.Close()
	c := testClient("https://api.invalid")
	err := c.PutSigned(context.Background(), srv.URL+"/signed", map[string]string{"Content-Type": "image/png", "Content-Length": "5"}, strings.NewReader("hello"), 5)
	if err != nil {
		t.Fatal(err)
	}
	if length != 5 || len(chunked) != 0 {
		t.Errorf("length %d, transfer encoding %v: storage signs the length, so it must be declared", length, chunked)
	}
	if contentType != "image/png" || body != "hello" {
		t.Errorf("content type %q, body %q", contentType, body)
	}
	if key != "" {
		t.Errorf("the key was sent to storage: %q", key)
	}
}

func TestTransient(t *testing.T) {
	if Transient(context.Canceled) {
		t.Error("a cancelled request should not be repeated")
	}
	if !Transient(errors.New("connection reset")) {
		t.Error("a network failure can be repeated")
	}
	if Transient(&Error{Status: 422}) {
		t.Error("a validation failure will fail again")
	}
	if !Transient(&Error{Status: 503}) {
		t.Error("a 503 can be repeated")
	}
}
