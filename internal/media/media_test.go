package media

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/orshih6/pikactl/internal/api"
)

func service(url string) *Service {
	c := api.New(url, "secret-key", "test")
	c.Sleep = func(ctx context.Context, _ time.Duration) error { return ctx.Err() }
	return &Service{API: c}
}

func TestAssetsFindsFilesInEveryOutputShape(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   []string
	}{
		{"video", `{"media_type":"video","video":{"url":"https://a/v.mp4","content_type":"video/mp4"}}`, []string{"video"}},
		{"images", `{"media_type":"image","images":[{"url":"https://a/1.png"},{"url":"https://a/2.png"}]}`, []string{"images-0", "images-1"}},
		{"videos", `{"media_type":"video","videos":[{"url":"https://a/1.mp4"}]}`, []string{"videos-0"}},
		{"audio and subtitle", `{"media_type":"audio","audio":{"url":"https://a/a.mp3"},"subtitle":{"url":"https://a/s.srt"}}`, []string{"audio", "subtitle"}},
		{"stems", `{"media_type":"video","video":{"url":"https://a/v.mp4"},"stems":[{"name":"score","url":"https://a/s.wav"}],"stems_error":null}`, []string{"stems-0", "video"}},
		{"transcript", `{"media_type":"transcription","transcript":{"url":"https://a/t.json"}}`, []string{"transcript"}},
		{"archive", `{"media_type":"archive","archive":{"url":"https://a/x.zip"}}`, []string{"archive"}},
		{"a shape nobody has seen", `{"new_thing":{"deep":[{"url":"https://a/n.bin"}]},"poster_url":"https://a/p.jpg"}`, []string{"new_thing-deep-0", "poster"}},
		{"text only", `{"prompt":"https://looks.like/a-url","mode":"x"}`, nil},
		{"nothing", ``, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var names []string
			for _, a := range Assets(json.RawMessage(tt.output)) {
				names = append(names, a.Name)
			}
			if !reflect.DeepEqual(names, tt.want) {
				t.Errorf("assets = %v, want %v", names, tt.want)
			}
		})
	}
}

func TestSafeNameCannotClimbOut(t *testing.T) {
	for _, in := range []string{"../../etc/passwd", "..", "/abs/path", "a/b", "media_1"} {
		got := SafeName(in)
		if strings.ContainsAny(got, "/\\") || got == ".." || got == "." || strings.HasPrefix(got, ".") {
			t.Errorf("SafeName(%q) = %q", in, got)
		}
	}
	if got := SafeName("media_8f3a-2c91"); got != "media_8f3a-2c91" {
		t.Errorf("an ordinary ID was changed: %q", got)
	}
}

func TestExtension(t *testing.T) {
	tests := []struct{ url, contentType, want string }{
		{"https://a/v.MP4?sig=x", "", ".mp4"},
		{"https://a/noext", "image/png", ".png"},
		{"https://a/noext", "", ".bin"},
		{"https://a/evil.mp4%2F..%2Fx", "", ".bin"},
	}
	for _, tt := range tests {
		if got := extensionFor(tt.url, tt.contentType); got != tt.want {
			t.Errorf("extensionFor(%q, %q) = %q, want %q", tt.url, tt.contentType, got, tt.want)
		}
	}
}

func TestSubmitSendsTheKeyAndTheBody(t *testing.T) {
	var key, body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key = r.Header.Get("Idempotency-Key")
		data, _ := io.ReadAll(r.Body)
		body = string(data)
		_, _ = io.WriteString(w, `{"id":"media_1","status":"queued"}`)
	}))
	defer srv.Close()
	job, err := service(srv.URL).Submit(context.Background(), "/v1/media/a/b/c", []byte(`{"prompt":"x"}`), "key-1")
	if err != nil {
		t.Fatal(err)
	}
	if job.ID != "media_1" || job.Status != "queued" {
		t.Errorf("job = %+v", job)
	}
	if key != "key-1" || body != `{"prompt":"x"}` {
		t.Errorf("key %q, body %q", key, body)
	}
}

func TestSubmitRepeatsALostAnswerUnderTheSameKey(t *testing.T) {
	var keys []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		if len(keys) < 3 {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			conn.Close()
			return
		}
		_, _ = io.WriteString(w, `{"id":"media_1","status":"queued"}`)
	}))
	defer srv.Close()
	job, err := service(srv.URL).Submit(context.Background(), "/v1/media/a", []byte(`{}`), "stable")
	if err != nil {
		t.Fatal(err)
	}
	if job.ID != "media_1" {
		t.Errorf("job = %+v", job)
	}
	if !reflect.DeepEqual(keys, []string{"stable", "stable", "stable"}) {
		t.Errorf("keys = %v: only the same key makes a repeat safe", keys)
	}
}

func TestSubmitThatNeverGetsAnAnswerIsReportedAsLost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _, _ := w.(http.Hijacker).Hijack()
		conn.Close()
	}))
	defer srv.Close()
	_, err := service(srv.URL).Submit(context.Background(), "/v1/media/a", []byte(`{}`), "stable")
	var lost *LostError
	if !errors.As(err, &lost) {
		t.Fatalf("err = %v, want a LostError", err)
	}
	if lost.Key != "stable" || !strings.Contains(err.Error(), "stable") {
		t.Errorf("the error should name the key to repeat with: %v", err)
	}
}

func TestSubmitDoesNotRepeatARejection(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"id":"media_1","status":"failed","error":{"code":"insufficient_balance","message":"no funds"}}`)
	}))
	defer srv.Close()
	job, err := service(srv.URL).Submit(context.Background(), "/v1/media/a", []byte(`{}`), "k")
	if calls != 1 {
		t.Errorf("submitted %d times", calls)
	}
	var apiErr *api.Error
	if !errors.As(err, &apiErr) || apiErr.Code != "insufficient_balance" {
		t.Errorf("err = %v", err)
	}
	if job == nil || job.Status != StatusFailed || job.Error.Code != "insufficient_balance" {
		t.Errorf("the failed job envelope should come back too: %+v", job)
	}
}

func TestWaitSurvivesAnOutageAndReportsEachStatusOnce(t *testing.T) {
	answers := []string{
		`{"id":"media_1","status":"queued"}`,
		`{"id":"media_1","status":"queued"}`,
		"503",
		"503", "503", "503", // enough to exhaust one GET's own retries
		`{"id":"media_1","status":"running"}`,
		`{"id":"media_1","status":"a_status_added_later"}`,
		`{"id":"media_1","status":"completed","output":{"video":{"url":"https://a/v.mp4"}}}`,
	}
	i := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		answer := answers[min(i, len(answers)-1)]
		i++
		if answer == "503" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, answer)
	}))
	defer srv.Close()
	var seen []string
	job, err := service(srv.URL).Wait(context.Background(), "media_1", WaitOptions{
		OnChange: func(j *Job) { seen = append(seen, j.Status) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != StatusCompleted {
		t.Errorf("job = %+v", job)
	}
	want := []string{"queued", "running", "a_status_added_later", "completed"}
	if !reflect.DeepEqual(seen, want) {
		t.Errorf("statuses = %v, want %v", seen, want)
	}
}

func TestWaitStopsOnARejectedKey(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"message":"Invalid API key"}`)
	}))
	defer srv.Close()
	_, err := service(srv.URL).Wait(context.Background(), "media_1", WaitOptions{})
	var apiErr *api.Error
	if !errors.As(err, &apiErr) || !apiErr.Auth() {
		t.Fatalf("err = %v", err)
	}
	if calls != 1 {
		t.Errorf("polled %d times with a bad key", calls)
	}
}

func TestWaitGivesUpAtItsTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"id":"media_1","status":"running"}`)
	}))
	defer srv.Close()
	_, err := service(srv.URL).Wait(context.Background(), "media_1", WaitOptions{Timeout: 50 * time.Millisecond})
	if err == nil || !strings.Contains(err.Error(), "gave up") {
		t.Errorf("err = %v", err)
	}
}

func TestSettleWaitsForAPendingCharge(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			_, _ = io.WriteString(w, `{"id":"media_1","status":"completed","billing":{"state":"pending"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"media_1","status":"completed","billing":{"state":"settled","charge_micro_usd":100000}}`)
	}))
	defer srv.Close()
	start := &Job{ID: "media_1", Status: StatusCompleted, Billing: &Billing{State: "pending"}}
	job := service(srv.URL).Settle(context.Background(), start)
	if charge := job.Charge(); charge == nil || *charge != 100000 {
		t.Errorf("charge = %v", charge)
	}
}

func TestAPendingChargeIsNotACharge(t *testing.T) {
	for _, billing := range []*Billing{nil, {State: "pending"}, {State: "unavailable"}} {
		if (&Job{Billing: billing}).Charge() != nil {
			t.Errorf("%+v should not read as a charge", billing)
		}
	}
	zero := int64(0)
	if c := (&Job{Billing: &Billing{State: "settled", ChargeMicroUSD: &zero}}).Charge(); c == nil || *c != 0 {
		t.Errorf("a settled charge of zero is still a charge: %v", c)
	}
}

func TestUploadPresignsThenPutsVerbatim(t *testing.T) {
	var minted map[string]any
	var putHeaders http.Header
	var putLength int64
	var putBody string
	storage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		putHeaders, putLength = r.Header.Clone(), r.ContentLength
		data, _ := io.ReadAll(r.Body)
		putBody = string(data)
	}))
	defer storage.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&minted)
		// Content-Length as a number, the way a JSON API may well send it.
		_, _ = io.WriteString(w, `{"upload_url":"`+storage.URL+`/signed","url":"https://hosted/a.png","headers":{"Content-Type":"image/png","Content-Length":5,"X-Amz-Meta":"m"}}`)
	}))
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "a.png")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	upload, err := service(srv.URL).Upload(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if upload.URL != "https://hosted/a.png" {
		t.Errorf("url = %q", upload.URL)
	}
	if minted["content_type"] != "image/png" || minted["size_bytes"] != float64(5) {
		t.Errorf("mint request = %v", minted)
	}
	if putLength != 5 || putBody != "hello" {
		t.Errorf("put %d bytes: %q", putLength, putBody)
	}
	if putHeaders.Get("X-Amz-Meta") != "m" || putHeaders.Get("Content-Type") != "image/png" {
		t.Errorf("signed headers not sent verbatim: %v", putHeaders)
	}
	if putHeaders.Get(api.HeaderKey) != "" {
		t.Error("the key was sent to storage")
	}
}

func TestDownloadWritesWholeFilesOnly(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Content-Length", "100")
			_, _ = io.WriteString(w, "partial")
			return // the body ends short of its declared length
		}
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = io.WriteString(w, "whole file")
	}))
	defer srv.Close()
	dir := t.TempDir()
	file, err := service(srv.URL).Download(context.Background(), Asset{Name: "video", URL: srv.URL + "/v.mp4"}, dir)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(file.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "whole file" || file.Bytes != 10 || filepath.Base(file.Path) != "video.mp4" {
		t.Errorf("file = %+v, data %q", file, data)
	}
	if len(file.SHA256) != 64 {
		t.Errorf("sha256 = %q", file.SHA256)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("left behind: %v", entries)
	}
}
