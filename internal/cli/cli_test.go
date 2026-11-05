package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/orshih6/pikactl/internal/api"
	"github.com/orshih6/pikactl/internal/history"
)

const (
	testKey = "test-key-do-not-leak"
	testOp  = "pika/test/text-to-video"
	testSch = `{"additionalProperties":false,"required":["prompt"],"properties":{
		"prompt":{"type":"string","description":"The prompt"},
		"duration":{"type":"integer","default":5},
		"resolution":{"type":"string","enum":["480p","768p"],"default":"768p"},
		"rewrite":{"type":"boolean"},
		"image":{"anyOf":[{"type":"string","media_kinds":["image"],"pattern":"^https?://"},{"type":"null"}]},
		"tracks":{"type":"array","items":{"oneOf":[{"$ref":"#/$defs/Track"}]}}},
		"$defs":{"Track":{"type":"object","properties":{"url":{"type":"string","media_kinds":["video"]},"id":{"type":"string"}}}}}`
)

// fakePika is a local stand-in for the API, and for the CDN its files live on.
// No test contacts Pika or spends anything.
type fakePika struct {
	t   *testing.T
	api *httptest.Server
	cdn *httptest.Server

	mu          sync.Mutex
	schema      string
	opCall      string
	balance     int64
	quote       int64
	quoteStatus int
	submitReply func(w http.ResponseWriter)
	statuses    []string
	polls       int
	output      string
	failure     string
	billing     string
	jobExtra    string
	submits     []submit
	uploads     int
	puts        []put
	deletedUps  []string
	deletedJobs []string
	calls       []string
	keysToCDN   []string
	llmBodies   []string
	llmHeaders  []http.Header
}

type submit struct {
	body string
	key  string
}

type put struct {
	contentType string
	length      int64
	key         string
}

func newFake(t *testing.T) *fakePika {
	t.Helper()
	f := &fakePika{
		t:        t,
		schema:   testSch,
		opCall:   `{"method":"POST","path":"/v1/media/` + testOp + `"}`,
		balance:  5_000_000,
		quote:    100_000,
		statuses: []string{"running", "completed"},
		billing:  `{"state":"settled","charge_micro_usd":100000}`,
	}
	f.cdn = httptest.NewServer(http.HandlerFunc(f.serveCDN))
	f.api = httptest.NewServer(http.HandlerFunc(f.serveAPI))
	f.output = fmt.Sprintf(`{"media_type":"video","video":{"url":"%s/files/video.mp4","content_type":"video/mp4"}}`, f.cdn.URL)
	t.Cleanup(f.cdn.Close)
	t.Cleanup(f.api.Close)

	t.Setenv("PIKA_API_KEY", testKey)
	t.Setenv("PIKA_API_URL", f.api.URL)
	t.Setenv("PIKA_DOCS_URL", f.api.URL)
	dir := t.TempDir()
	t.Setenv("PIKACTL_DATA", filepath.Join(dir, "data"))
	t.Setenv("PIKACTL_CONFIG", filepath.Join(dir, "config.yaml"))
	t.Chdir(dir)

	before := configureClient
	configureClient = func(c *api.Client) {
		c.Sleep = func(ctx context.Context, _ time.Duration) error { return ctx.Err() }
	}
	t.Cleanup(func() { configureClient = before })
	return f
}

func (f *fakePika) serveCDN(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.Method {
	case http.MethodGet:
		f.keysToCDN = append(f.keysToCDN, r.Header.Get(api.HeaderKey))
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = io.WriteString(w, "fake video bytes")
	case http.MethodPut:
		_, _ = io.Copy(io.Discard, r.Body)
		f.puts = append(f.puts, put{r.Header.Get("Content-Type"), r.ContentLength, r.Header.Get(api.HeaderKey)})
	}
}

func (f *fakePika) serveAPI(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	authed := r.Header.Get(api.HeaderKey) == testKey
	path := r.URL.Path
	switch {
	case path == "/catalog/apis":
		fmt.Fprintf(w, `{"apis":[{"api_id":"%s","vendor":"pika","function":"text-to-video","category":"video","name":"Test","call":%s,
			"display_pricing":{"components":[{"unit":{"type":"second","quantity":1},"starting_at":{"micro_usd":10000},"price_tiers":[{"spec":{"resolution":"480p"},"micro_usd":10000}]}]}},
			{"api_id":"anthropic/test-llm","vendor":"anthropic","category":"llm","name":"LLM","call":null}]}`, testOp, f.opCall)
	case path == "/catalog/apis/"+testOp:
		fmt.Fprintf(w, `{"api_id":"%s","vendor":"pika","function":"text-to-video","category":"video","name":"Test","call":%s,"input_schema":%s}`, testOp, f.opCall, f.schema)
	case path == "/catalog/apis/anthropic/test-llm", path == "/catalog/apis/openai/test-llm", path == "/catalog/apis/google/test-llm":
		fmt.Fprintf(w, `{"api_id":"%s","category":"llm","name":"LLM","call":null,"input_schema":{}}`, strings.TrimPrefix(path, "/catalog/apis/"))
	case path == "/catalog/apis/typesafe/jev":
		_, _ = io.WriteString(w, `{"api_id":"typesafe/jev","category":"decision","name":"JEV","call":null}`)
	case strings.HasPrefix(path, "/catalog/apis/") && strings.HasSuffix(path, "/quote"):
		if f.quoteStatus != 0 {
			w.WriteHeader(f.quoteStatus)
			_, _ = io.WriteString(w, `{"message":"duration: Input should be less than or equal to 15"}`)
			return
		}
		fmt.Fprintf(w, `{"sell_usd":"x","micro_usd":%d}`, f.quote)
	case path == "/catalog/competitors":
		fmt.Fprintf(w, `{"recorded_at":"2026-10-24","competitors":[{"id":"fal","name":"fal","kind":"aggregator"},{"id":"runway","name":"Runway","kind":"official"}],
			"rows":[{"api_id":"%s","spec":{"resolution":"480p"},"quotes":[{"competitor":"runway","usd":"0.3","unit":"second"},{"competitor":"fal","usd":"0.14","unit":"second"}]},
			{"api_id":"pika/other/text-to-video","spec":{},"quotes":[]}]}`, testOp)
	case strings.HasPrefix(path, "/catalog/"):
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"Catalog API not found"}`)
	case path == "/llms/anthropic/test-llm":
		_, _ = io.WriteString(w, "## Endpoint\n```\nPOST https://api.dev.pika.art/anthropic/v1/messages\n```\nRequired headers:\n- `anthropic-version: 2023-06-01`\n\n## Request\n")
	case path == "/llms/openai/test-llm":
		_, _ = io.WriteString(w, "POST https://api.dev.pika.art/v1/chat/completions\n")
	case path == "/llms/google/test-llm":
		_, _ = io.WriteString(w, "POST https://api.dev.pika.art/genai/v1beta/models/test-llm:generateContent\n")
	case path == "/llms/typesafe/jev":
		_, _ = io.WriteString(w, "POST https://api.dev.pika.art/v1/decisions\n")
	case !authed:
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"message":"Invalid API key"}`)
	case path == "/billing/balance":
		fmt.Fprintf(w, `{"unit":"micro_usd","balance_micro_usd":%d,"prepaid_micro_usd":%d,"credit_micro_usd":0,"postpaid":null}`, f.balance, f.balance)
	case path == "/v1/media/uploads" && r.Method == http.MethodPost:
		f.uploads++
		fmt.Fprintf(w, `{"upload_url":"%s/signed/%d","url":"https://hosted.example/%d","headers":{"Content-Type":"image/png","Content-Length":"5"}}`, f.cdn.URL, f.uploads, f.uploads)
	case path == "/v1/media/uploads" && r.Method == http.MethodDelete:
		f.deletedUps = append(f.deletedUps, r.URL.Query().Get("url"))
		w.WriteHeader(http.StatusNoContent)
	case path == "/v1/media/"+testOp && r.Method == http.MethodPost:
		f.submits = append(f.submits, submit{string(body), r.Header.Get("Idempotency-Key")})
		if f.submitReply != nil {
			f.submitReply(w)
			return
		}
		fmt.Fprintf(w, `{"id":"media_%d","status":"queued"}`, len(f.submits))
	case strings.HasPrefix(path, "/v1/media/jobs/") && r.Method == http.MethodDelete:
		f.deletedJobs = append(f.deletedJobs, strings.TrimPrefix(path, "/v1/media/jobs/"))
		w.WriteHeader(http.StatusNoContent)
	case strings.HasPrefix(path, "/v1/media/jobs/"):
		id := strings.TrimPrefix(path, "/v1/media/jobs/")
		status := f.statuses[min(f.polls, len(f.statuses)-1)]
		f.polls++
		switch status {
		case "completed":
			fmt.Fprintf(w, `{"id":"%s","status":"completed","model_id":"%s","output":%s,"billing":%s,"a_new_field":1%s}`, id, testOp, f.output, f.billing, f.jobExtra)
		case "failed":
			fmt.Fprintf(w, `{"id":"%s","status":"failed","error":%s,"billing":{"state":"settled","charge_micro_usd":0}}`, id, f.failure)
		default:
			fmt.Fprintf(w, `{"id":"%s","status":"%s"}`, id, status)
		}
	case path == "/anthropic/v1/messages":
		f.llmBodies, f.llmHeaders = append(f.llmBodies, string(body)), append(f.llmHeaders, r.Header.Clone())
		_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"hello from anthropic"}],"usage":{"input_tokens":1}}`)
	case path == "/v1/chat/completions":
		f.llmBodies = append(f.llmBodies, string(body))
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"hello from openai"}}]}`)
	case path == "/genai/v1beta/models/test-llm:generateContent":
		f.llmBodies = append(f.llmBodies, string(body))
		_, _ = io.WriteString(w, `{"candidates":[{"content":{"parts":[{"text":"hello from google"}]}}]}`)
	case path == "/v1/decisions":
		f.llmBodies = append(f.llmBodies, string(body))
		_, _ = io.WriteString(w, `{"answers":{"is_bug":0.9}}`)
	case path == "/v1/voices" && r.Method == http.MethodGet:
		_, _ = io.WriteString(w, `{"voices":[{"voice_id":"v1"}]}`)
	case path == "/v1/voices" && r.Method == http.MethodPost:
		f.llmBodies = append(f.llmBodies, string(body))
		_, _ = io.WriteString(w, `{"voice_id":"v2"}`)
	case path == "/v1/a-route-from-the-future":
		_, _ = io.WriteString(w, `{"future":true}`)
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"not found"}`)
	}
}

func (f *fakePika) submitted() []submit {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]submit(nil), f.submits...)
}

// run executes one pikactl command line and returns stdout and stderr.
func run(stdin string, args ...string) (string, string, error) {
	var out, errOut bytes.Buffer
	cmd := newRootCommand()
	cmd.SetArgs(args)
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetIn(strings.NewReader(stdin))
	err := cmd.ExecuteContext(context.Background())
	return out.String(), errOut.String(), err
}

func mustRun(t *testing.T, args ...string) (string, string) {
	t.Helper()
	out, errOut, err := run("", args...)
	if err != nil {
		t.Fatalf("pikactl %v: %v\nstderr: %s", args, err, errOut)
	}
	return out, errOut
}

func loadHistory(t *testing.T) []*history.Record {
	t.Helper()
	records, skipped, err := history.Open(os.Getenv("PIKACTL_DATA")).Load()
	if err != nil {
		t.Fatal(err)
	}
	if skipped != 0 {
		t.Errorf("%d history lines were unreadable", skipped)
	}
	return records
}

func TestRunEndToEnd(t *testing.T) {
	f := newFake(t)
	out, errOut := mustRun(t, "run", testOp, "--prompt", "a paper boat", "--duration", "7", "--note", "first try", "--yes")

	subs := f.submitted()
	if len(subs) != 1 {
		t.Fatalf("submitted %d times", len(subs))
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(subs[0].body), &body); err != nil {
		t.Fatal(err)
	}
	if body["prompt"] != "a paper boat" || body["duration"] != float64(7) {
		t.Errorf("body = %v", body)
	}
	if _, sent := body["resolution"]; sent {
		t.Errorf("an unset input was sent; the API's own default should apply: %v", body)
	}
	if subs[0].key == "" {
		t.Error("no idempotency key")
	}

	path := strings.TrimSpace(out)
	if filepath.Base(path) != "video.mp4" {
		t.Errorf("stdout should be the file's path, got %q", out)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "fake video bytes" {
		t.Errorf("file = %q", data)
	}
	for _, want := range []string{"$0.10", "$5.00", "media_1", "completed"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr should mention %q:\n%s", want, errOut)
		}
	}

	records := loadHistory(t)
	if len(records) != 1 {
		t.Fatalf("%d history records", len(records))
	}
	r := records[0]
	if r.Status != "completed" || r.JobID != "media_1" || r.APIID != testOp || r.Note != "first try" {
		t.Errorf("record = %+v", r)
	}
	if !strings.Contains(string(r.Body), "a paper boat") {
		t.Errorf("the prompt should be kept: %s", r.Body)
	}
	if r.QuoteMicroUSD == nil || *r.QuoteMicroUSD != 100_000 || r.ChargeMicroUSD == nil || *r.ChargeMicroUSD != 100_000 {
		t.Errorf("quote %v, charge %v", r.QuoteMicroUSD, r.ChargeMicroUSD)
	}
	if len(r.Files) != 1 || r.Files[0].Path != path || r.Files[0].Bytes != 16 || len(r.Files[0].SHA256) != 64 {
		t.Errorf("files = %+v", r.Files)
	}
	if !strings.Contains(string(r.Response), "a_new_field") {
		t.Errorf("the answer should be kept verbatim, with fields pikactl does not know: %s", r.Response)
	}
	for _, key := range f.keysToCDN {
		if key != "" {
			t.Error("the key was sent to the CDN")
		}
	}
}

func TestTheKeyIsNeverWritten(t *testing.T) {
	newFake(t)
	out, errOut := mustRun(t, "run", testOp, "--prompt", "x", "--yes")
	jsonOut, jsonErr := mustRun(t, "history", "--json")
	data, err := os.ReadFile(filepath.Join(os.Getenv("PIKACTL_DATA"), "history.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{"stdout": out, "stderr": errOut, "history": string(data), "json": jsonOut, "json stderr": jsonErr} {
		if strings.Contains(text, testKey) {
			t.Errorf("the key is in %s", name)
		}
	}
}

func TestFreeGenerationIsNamed(t *testing.T) {
	f := newFake(t)
	f.billing = `{"state":"settled","charge_micro_usd":0}`
	f.jobExtra = `,"free_generation":{"promo_id":"daily-free","allowance":"t2v","remaining_today":null}`
	_, errOut := mustRun(t, "run", testOp, "--prompt", "x", "--yes")
	if !strings.Contains(errOut, "$0.00 (free generation: daily-free)") {
		t.Errorf("a $0.00 charge against a real quote should explain itself: %s", errOut)
	}
	r := loadHistory(t)[0]
	if r.ChargeMicroUSD == nil || *r.ChargeMicroUSD != 0 || *r.QuoteMicroUSD != 100_000 {
		t.Errorf("record = %+v", r)
	}
	if !strings.Contains(string(r.Response), "daily-free") {
		t.Errorf("the promotion should be kept in the answer: %s", r.Response)
	}
}

func TestFlagsComeFromTheLiveSchema(t *testing.T) {
	f := newFake(t)
	f.schema = `{"required":["prompt"],"properties":{"prompt":{"type":"string"},"knob_added_tomorrow":{"type":"integer"}}}`
	mustRun(t, "run", testOp, "--prompt", "x", "--knob-added-tomorrow", "3", "--yes")
	if body := f.submitted()[0].body; !strings.Contains(body, `"knob_added_tomorrow":3`) {
		t.Errorf("body = %s", body)
	}
	help, _ := mustRun(t, "run", testOp, "--help")
	if !strings.Contains(help, "--knob-added-tomorrow") {
		t.Errorf("help should list the new input:\n%s", help)
	}
}

func TestInputFlagForms(t *testing.T) {
	f := newFake(t)
	mustRun(t, "run", testOp, "--prompt", "x", "--resolution=480p", "--rewrite", "--set", "duration=9", "--yes")
	var body map[string]any
	if err := json.Unmarshal([]byte(f.submitted()[0].body), &body); err != nil {
		t.Fatal(err)
	}
	if body["resolution"] != "480p" || body["rewrite"] != true || body["duration"] != float64(9) {
		t.Errorf("body = %v", body)
	}
}

func TestBadInputsSendNothing(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"missing required", []string{"--duration", "5"}, "missing required input: prompt"},
		{"unknown flag", []string{"--prompt", "x", "--nope", "1"}, "unknown flag"},
		{"bad enum", []string{"--prompt", "x", "--resolution", "9000p"}, "not one of"},
		{"bad number", []string{"--prompt", "x", "--duration", "soon"}, "not a valid integer"},
		{"bad set", []string{"--prompt", "x", "--set", "noequals"}, "expected key=value"},
		{"unknown set", []string{"--prompt", "x", "--set", "nope=1"}, "not an input"},
		{"missing file", []string{"--prompt", "x", "--image", "./absent.png"}, "neither a URL nor a file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFake(t)
			_, _, err := run("", append([]string{"run", testOp, "--yes"}, tt.args...)...)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want it to mention %q", err, tt.want)
			}
			if len(f.submitted()) != 0 {
				t.Error("a bad request was submitted")
			}
		})
	}
}

func TestRunAsksFirst(t *testing.T) {
	f := newFake(t)
	for _, answer := range []string{"", "n\n", "nope\n"} {
		_, errOut, err := run(answer, "run", testOp, "--prompt", "x")
		if err == nil || !strings.Contains(err.Error(), "aborted") {
			t.Errorf("answer %q: err = %v", answer, err)
		}
		if !strings.Contains(errOut, "$0.10") {
			t.Errorf("the question should name the price: %s", errOut)
		}
	}
	if n := len(f.submitted()); n != 0 {
		t.Fatalf("submitted %d times without a yes", n)
	}
	if n := len(loadHistory(t)); n != 0 {
		t.Errorf("%d history entries for requests never sent", n)
	}
	if _, _, err := run("y\n", "run", testOp, "--prompt", "x"); err != nil {
		t.Fatal(err)
	}
	if n := len(f.submitted()); n != 1 {
		t.Errorf("submitted %d times after a yes", n)
	}
}

func TestGuardsThatStopSpending(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*fakePika)
		args  []string
		want  string
	}{
		{"dry run", nil, []string{"--dry-run"}, ""},
		{"over the cap", nil, []string{"--max-usd", "0.05"}, "above --max-usd"},
		{"no quote with a cap", func(f *fakePika) { f.quoteStatus = 404 }, []string{"--max-usd", "1"}, "no quote"},
		{"not enough balance", func(f *fakePika) { f.balance = 50_000 }, nil, "not enough balance"},
		{"empty balance, no quote", func(f *fakePika) { f.balance, f.quoteStatus = 0, 404 }, nil, "not enough balance"},
		{"quote refuses the request", func(f *fakePika) { f.quoteStatus = 422 }, nil, "less than or equal to 15"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFake(t)
			if tt.setup != nil {
				tt.setup(f)
			}
			_, _, err := run("", append([]string{"run", testOp, "--prompt", "x", "--yes"}, tt.args...)...)
			if tt.want == "" && err != nil {
				t.Errorf("err = %v", err)
			}
			if tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)) {
				t.Errorf("err = %v, want it to mention %q", err, tt.want)
			}
			if n := len(f.submitted()); n != 0 {
				t.Errorf("submitted %d times", n)
			}
		})
	}
}

func TestCapAtTheQuoteIsAllowed(t *testing.T) {
	f := newFake(t)
	mustRun(t, "run", testOp, "--prompt", "x", "--max-usd", "0.10", "--yes")
	if n := len(f.submitted()); n != 1 {
		t.Errorf("submitted %d times", n)
	}
}

func TestUnknownPriceStillRunsWhenAccepted(t *testing.T) {
	f := newFake(t)
	f.quoteStatus = 404
	_, errOut := mustRun(t, "run", testOp, "--prompt", "x", "--yes")
	if !strings.Contains(errOut, "unknown") {
		t.Errorf("the unknown price should be stated: %s", errOut)
	}
	if n := len(f.submitted()); n != 1 {
		t.Errorf("submitted %d times", n)
	}
}

func TestRejectedSubmitIsRecordedAndNotRepeated(t *testing.T) {
	f := newFake(t)
	f.submitReply = func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"id":"media_9","status":"failed","error":{"code":"rate_limited","message":"rate limit exceeded: rpm"}}`)
	}
	_, _, err := run("", "run", testOp, "--prompt", "x", "--yes")
	if err == nil || exitStatus(err) != exitThrottled {
		t.Errorf("err = %v, exit %d", err, exitStatus(err))
	}
	if n := len(f.submitted()); n != 1 {
		t.Errorf("submitted %d times: a failed job replays on its key, so repeating is pointless", n)
	}
	r := loadHistory(t)[0]
	if r.Status != "failed" || r.ErrorCode != "rate_limited" || r.JobID != "media_9" {
		t.Errorf("record = %+v", r)
	}
}

func TestSubmitRejectedWithoutAJob(t *testing.T) {
	f := newFake(t)
	f.submitReply = func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = io.WriteString(w, `{"message":"prompt: too long"}`)
	}
	_, _, err := run("", "run", testOp, "--prompt", "x", "--yes")
	if err == nil || exitStatus(err) != exitAPI {
		t.Errorf("err = %v", err)
	}
	r := loadHistory(t)[0]
	if r.Status != "failed" || r.HTTPStatus != 422 || r.ErrorMessage != "prompt: too long" {
		t.Errorf("record = %+v", r)
	}
}

func TestLostSubmitIsRecordedAsUnknown(t *testing.T) {
	f := newFake(t)
	f.submitReply = func(w http.ResponseWriter) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			conn.Close()
		}
	}
	_, _, err := run("", "run", testOp, "--prompt", "x", "--yes")
	if err == nil || !strings.Contains(err.Error(), "may or may not") {
		t.Fatalf("err = %v", err)
	}
	subs := f.submitted()
	if len(subs) < 3 {
		t.Errorf("submitted %d times", len(subs))
	}
	for _, s := range subs {
		if s.key != subs[0].key {
			t.Errorf("submits = %+v: a repeat is only safe under one key", subs)
		}
	}
	r := loadHistory(t)[0]
	if r.Status != history.StatusUnknown || r.IdempotencyKey != subs[0].key {
		t.Errorf("record = %+v", r)
	}
	if !strings.Contains(err.Error(), subs[0].key) {
		t.Errorf("the error should give the key to repeat with: %v", err)
	}
}

func TestFailedJob(t *testing.T) {
	f := newFake(t)
	f.statuses = []string{"failed"}
	f.failure = `{"code":"content_moderation","message":"refused by the provider"}`
	_, _, err := run("", "run", testOp, "--prompt", "x", "--yes")
	var failed *jobFailedError
	if !errors.As(err, &failed) || failed.Code != "content_moderation" {
		t.Fatalf("err = %v", err)
	}
	if exitStatus(err) != exitAPI {
		t.Errorf("exit %d", exitStatus(err))
	}
	r := loadHistory(t)[0]
	if r.Status != "failed" || r.ErrorCode != "content_moderation" || r.ChargeMicroUSD == nil || *r.ChargeMicroUSD != 0 {
		t.Errorf("record = %+v", r)
	}
}

func TestLocalFilesAreUploadedAtAnyDepth(t *testing.T) {
	f := newFake(t)
	if err := os.WriteFile("photo.png", []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	timeline := `[{"id":"a","url":"photo.png"},{"id":"b","url":"https://elsewhere/clip.mp4"},{"id":"c","url":"photo.png"}]`
	mustRun(t, "run", testOp, "--prompt", "x", "--image", "photo.png", "--tracks", timeline, "--yes")

	if f.uploads != 1 {
		t.Errorf("uploaded %d times: one file used three times is one upload", f.uploads)
	}
	if len(f.puts) != 1 || f.puts[0].contentType != "image/png" || f.puts[0].length != 5 {
		t.Errorf("puts = %+v", f.puts)
	}
	if f.puts[0].key != "" {
		t.Error("the key was sent to storage")
	}
	body := f.submitted()[0].body
	if strings.Contains(body, "photo.png") {
		t.Errorf("a local path reached the API: %s", body)
	}
	if !strings.Contains(body, `"image":"https://hosted.example/1"`) || !strings.Contains(body, "https://elsewhere/clip.mp4") {
		t.Errorf("body = %s", body)
	}
	if r := loadHistory(t)[0]; r.Uploads["photo.png"] != "https://hosted.example/1" {
		t.Errorf("the history should say which file became which URL: %v", r.Uploads)
	}
	if len(f.deletedUps) != 0 {
		t.Errorf("an upload in use was deleted: %v", f.deletedUps)
	}
}

func TestDecliningRemovesTheUpload(t *testing.T) {
	f := newFake(t)
	if err := os.WriteFile("photo.png", []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run("n\n", "run", testOp, "--prompt", "x", "--image", "photo.png"); err == nil {
		t.Fatal("expected an abort")
	}
	if len(f.deletedUps) != 1 || f.deletedUps[0] != "https://hosted.example/1" {
		t.Errorf("deleted = %v", f.deletedUps)
	}
	if len(f.submitted()) != 0 {
		t.Error("submitted after a no")
	}
}

func TestNoWaitThenPickUpLater(t *testing.T) {
	f := newFake(t)
	out, _ := mustRun(t, "run", testOp, "--prompt", "x", "--no-wait", "--yes")
	id := strings.TrimSpace(out)
	if id != "media_1" {
		t.Fatalf("stdout = %q", out)
	}
	r := loadHistory(t)[0]
	if r.Status != "queued" || r.Terminal() {
		t.Errorf("record = %+v", r)
	}
	if f.polls != 0 {
		t.Errorf("--no-wait polled %d times", f.polls)
	}

	out, _ = mustRun(t, "jobs", "wait", r.Ref)
	if filepath.Base(strings.TrimSpace(out)) != "video.mp4" {
		t.Errorf("stdout = %q", out)
	}
	records := loadHistory(t)
	if len(records) != 1 {
		t.Fatalf("picking a job up should not start a new entry: %d", len(records))
	}
	if r := records[0]; r.Status != "completed" || len(r.Files) != 1 || r.ChargeMicroUSD == nil {
		t.Errorf("record = %+v", r)
	}
}

func TestHistorySyncCatchesUp(t *testing.T) {
	f := newFake(t)
	mustRun(t, "run", testOp, "--prompt", "x", "--no-wait", "--yes")
	f.statuses = []string{"running"}
	f.polls = 0
	_, errOut := mustRun(t, "history", "sync")
	if !strings.Contains(errOut, "still running") {
		t.Errorf("stderr = %s", errOut)
	}
	if r := loadHistory(t)[0]; r.Status != "running" {
		t.Errorf("record = %+v", r)
	}

	f.statuses = []string{"completed"}
	mustRun(t, "history", "sync")
	r := loadHistory(t)[0]
	if r.Status != "completed" || len(r.Files) != 1 || r.ChargeMicroUSD == nil {
		t.Errorf("record = %+v", r)
	}

	calls := len(f.calls)
	mustRun(t, "history", "sync")
	if len(f.calls) != calls {
		t.Errorf("a finished job should not be read again: %v", f.calls[calls:])
	}
}

func TestPendingChargeIsPickedUpLater(t *testing.T) {
	f := newFake(t)
	f.billing = `{"state":"pending"}`
	_, errOut := mustRun(t, "run", testOp, "--prompt", "x", "--yes")
	if !strings.Contains(errOut, "not settled") {
		t.Errorf("stderr = %s", errOut)
	}
	if r := loadHistory(t)[0]; r.ChargeMicroUSD != nil || r.Status != "completed" {
		t.Errorf("a pending charge must not be recorded: %+v", r)
	}
	f.billing = `{"state":"settled","charge_micro_usd":90000}`
	mustRun(t, "history", "sync")
	if r := loadHistory(t)[0]; r.ChargeMicroUSD == nil || *r.ChargeMicroUSD != 90000 {
		t.Errorf("record = %+v", r)
	}
}

func TestRerun(t *testing.T) {
	f := newFake(t)
	mustRun(t, "run", testOp, "--prompt", "same prompt", "--duration", "6", "--note", "take one", "--yes")
	first := loadHistory(t)[0]
	f.polls = 0

	mustRun(t, "history", "rerun", first.Ref, "--set", "duration=8", "--yes")
	records := loadHistory(t)
	if len(records) != 2 {
		t.Fatalf("%d records: the original must stay as it was", len(records))
	}
	second := records[1]
	if second.RerunOf != first.Ref || second.Note != "take one" || second.Status != "completed" {
		t.Errorf("rerun = %+v", second)
	}
	subs := f.submitted()
	if !strings.Contains(subs[1].body, "same prompt") || !strings.Contains(subs[1].body, `"duration":8`) {
		t.Errorf("body = %s", subs[1].body)
	}
	if subs[0].key == subs[1].key {
		t.Error("a rerun must use a new key, or it just replays the old job")
	}
	if first.Status != "completed" || !strings.Contains(string(records[0].Body), `"duration":6`) {
		t.Error("the rerun changed the original entry")
	}
}

func TestRerunAsksFirst(t *testing.T) {
	f := newFake(t)
	mustRun(t, "run", testOp, "--prompt", "x", "--yes")
	ref := loadHistory(t)[0].Ref
	if _, _, err := run("", "history", "rerun", ref); err == nil {
		t.Error("a rerun spends money and should ask")
	}
	if n := len(f.submitted()); n != 1 {
		t.Errorf("submitted %d times", n)
	}
}

func TestHistoryListAndShow(t *testing.T) {
	f := newFake(t)
	mustRun(t, "run", testOp, "--prompt", "a paper boat", "--yes")
	f.polls = 0
	f.statuses = []string{"failed"}
	f.failure = `{"code":"provider_error","message":"boom"}`
	if _, _, err := run("", "run", testOp, "--prompt", "a second one", "--note", "labelled", "--yes"); err == nil {
		t.Fatal("expected the job to fail")
	}

	out, errOut := mustRun(t, "history")
	for _, want := range []string{"a paper boat", "labelled", "completed", "failed", "$0.10", testOp} {
		if !strings.Contains(out, want) {
			t.Errorf("list should show %q:\n%s", want, out)
		}
	}
	if strings.Index(out, "labelled") > strings.Index(out, "a paper boat") {
		t.Errorf("newest should come first:\n%s", out)
	}
	if !strings.Contains(errOut, "$0.10 charged") {
		t.Errorf("stderr = %s", errOut)
	}

	if out, _ := mustRun(t, "history", "--status", "failed"); strings.Contains(out, "a paper boat") {
		t.Errorf("filter ignored:\n%s", out)
	}
	if out, _ := mustRun(t, "history", "--limit", "1"); strings.Contains(out, "a paper boat") {
		t.Errorf("limit ignored:\n%s", out)
	}
	out, errOut = mustRun(t, "history", "--note", "LABEL")
	if !strings.Contains(out, "labelled") || strings.Contains(out, "a paper boat") {
		t.Errorf("--note should keep only the labelled request:\n%s", out)
	}
	if !strings.Contains(errOut, "across all 1") {
		t.Errorf("--note total should count only the labelled request: %s", errOut)
	}

	ref := loadHistory(t)[0].Ref
	out, _ = mustRun(t, "history", "show", ref)
	for _, want := range []string{"media_1", "a paper boat", "video.mp4", "$0.10"} {
		if !strings.Contains(out, want) {
			t.Errorf("show should include %q:\n%s", want, out)
		}
	}

	out, _ = mustRun(t, "history", "show", ref, "--json")
	var record history.Record
	if err := json.Unmarshal([]byte(out), &record); err != nil {
		t.Fatalf("--json: %v\n%s", err, out)
	}
	if record.JobID != "media_1" {
		t.Errorf("record = %+v", record)
	}
}

func TestJobsDelete(t *testing.T) {
	f := newFake(t)
	mustRun(t, "run", testOp, "--prompt", "x", "--yes")
	ref := loadHistory(t)[0].Ref
	if _, _, err := run("n\n", "jobs", "delete", ref); err == nil {
		t.Error("deleting should ask")
	}
	if len(f.deletedJobs) != 0 {
		t.Fatal("deleted after a no")
	}
	mustRun(t, "jobs", "delete", ref, "--yes")
	if len(f.deletedJobs) != 1 || f.deletedJobs[0] != "media_1" {
		t.Errorf("deleted = %v", f.deletedJobs)
	}
	r := loadHistory(t)[0]
	if !r.Deleted || !strings.Contains(string(r.Body), `"prompt"`) || len(r.Files) != 1 {
		t.Errorf("the local entry should survive, marked: %+v", r)
	}
	if _, err := os.Stat(r.Files[0].Path); err != nil {
		t.Errorf("the saved file should survive: %v", err)
	}
}

func TestOutputCopy(t *testing.T) {
	newFake(t)
	mustRun(t, "run", testOp, "--prompt", "x", "--yes", "-o", "out/boat.mp4")
	if data, err := os.ReadFile("out/boat.mp4"); err != nil || string(data) != "fake video bytes" {
		t.Errorf("copy = %q, %v", data, err)
	}
	if r := loadHistory(t)[0]; r.Files[0].Path == "out/boat.mp4" {
		t.Error("the history should point at the kept file, not the copy")
	}
}

func TestJSONOutput(t *testing.T) {
	newFake(t)
	out, _ := mustRun(t, "run", testOp, "--prompt", "x", "--yes", "--json")
	var record history.Record
	if err := json.Unmarshal([]byte(out), &record); err != nil {
		t.Fatalf("--json: %v\n%s", err, out)
	}
	if record.Status != "completed" || len(record.Files) != 1 {
		t.Errorf("record = %+v", record)
	}
}

func TestChatFollowsEachModelsProtocol(t *testing.T) {
	tests := []struct {
		id, want, inBody string
	}{
		{"anthropic/test-llm", "hello from anthropic", `"max_tokens":1024`},
		{"openai/test-llm", "hello from openai", `"model":"openai/test-llm"`},
		{"google/test-llm", "hello from google", `"parts":[{"text":"hi there"}]`},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			f := newFake(t)
			out, _ := mustRun(t, "chat", tt.id, "hi", "there")
			if strings.TrimSpace(out) != tt.want {
				t.Errorf("stdout = %q", out)
			}
			if !strings.Contains(f.llmBodies[0], tt.inBody) {
				t.Errorf("body = %s", f.llmBodies[0])
			}
			r := loadHistory(t)[0]
			if r.Kind != history.KindChat || r.Status != "ok" || !strings.Contains(string(r.Body), "hi there") || !strings.Contains(string(r.Response), tt.want) {
				t.Errorf("record = %+v", r)
			}
		})
	}
}

func TestChatSendsRequiredHeadersAndExtras(t *testing.T) {
	f := newFake(t)
	mustRun(t, "chat", "anthropic/test-llm", "hi", "--system", "be brief", "--max-tokens", "50", "--set", "temperature=0.2")
	if got := f.llmHeaders[0].Get("anthropic-version"); got != "2023-06-01" {
		t.Errorf("anthropic-version = %q", got)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(f.llmBodies[0]), &body); err != nil {
		t.Fatal(err)
	}
	if body["system"] != "be brief" || body["max_tokens"] != float64(50) || body["temperature"] != 0.2 {
		t.Errorf("body = %v", body)
	}
}

func TestChatPromptFromStdin(t *testing.T) {
	f := newFake(t)
	if _, _, err := run("from a pipe\n", "chat", "openai/test-llm"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.llmBodies[0], "from a pipe") {
		t.Errorf("body = %s", f.llmBodies[0])
	}
}

func TestEachKindOfOperationPointsToItsCommand(t *testing.T) {
	newFake(t)
	if _, _, err := run("", "run", "anthropic/test-llm", "--prompt", "x"); err == nil || !strings.Contains(err.Error(), "pikactl chat") {
		t.Errorf("run on an LLM: %v", err)
	}
	if _, _, err := run("", "run", "typesafe/jev"); err == nil || !strings.Contains(err.Error(), "pikactl decide") {
		t.Errorf("run on a decision model: %v", err)
	}
	if _, _, err := run("", "chat", testOp, "hi"); err == nil || !strings.Contains(err.Error(), "pikactl run") {
		t.Errorf("chat on a job: %v", err)
	}
}

func TestDecide(t *testing.T) {
	f := newFake(t)
	out, _ := mustRun(t, "decide", "typesafe/jev", "--state", `{"ticket":"blank page"}`, "--questions", `{"is_bug":{"type":"noul"}}`)
	if !strings.Contains(out, `"is_bug": 0.9`) {
		t.Errorf("stdout = %s", out)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(f.llmBodies[0]), &body); err != nil {
		t.Fatal(err)
	}
	if body["model"] != "typesafe/jev" || body["state"].(map[string]any)["ticket"] != "blank page" {
		t.Errorf("body = %v", body)
	}
	if r := loadHistory(t)[0]; r.Kind != history.KindDecision || r.Status != "ok" {
		t.Errorf("record = %+v", r)
	}
}

func TestVoiceCloningNeedsConsentFromThePerson(t *testing.T) {
	f := newFake(t)
	if _, _, err := run("", "voices", "create", "--name", "n", "--sample", "https://a/s.mp3", "--yes"); err == nil || !strings.Contains(err.Error(), "--consent") {
		t.Errorf("err = %v", err)
	}
	if len(f.llmBodies) != 0 {
		t.Fatal("a voice was sent without consent")
	}
	mustRun(t, "voices", "create", "--name", "n", "--sample", "https://a/s.mp3", "--consent", "--yes")
	if !strings.Contains(f.llmBodies[0], `"consent_attested":true`) {
		t.Errorf("body = %s", f.llmBodies[0])
	}
}

func TestAPICommandReachesAnyRoute(t *testing.T) {
	newFake(t)
	out, _ := mustRun(t, "api", "GET", "/v1/a-route-from-the-future")
	if !strings.Contains(out, `"future":true`) {
		t.Errorf("stdout = %s", out)
	}
	if n := len(loadHistory(t)); n != 0 {
		t.Errorf("a read should not enter the history: %d", n)
	}
}

func TestAPICommandAsksBeforeWritesAndRecordsThem(t *testing.T) {
	f := newFake(t)
	path := "/v1/media/" + testOp
	if _, _, err := run("", "api", "POST", path, "-d", `{"prompt":"x"}`); err == nil {
		t.Error("a write should ask")
	}
	if len(f.submitted()) != 0 {
		t.Fatal("sent without a yes")
	}
	mustRun(t, "api", "POST", path, "-d", `{"prompt":"x"}`, "--yes")
	r := loadHistory(t)[0]
	if r.Kind != history.KindAPI || r.JobID != "media_1" || r.Status != "queued" || !strings.Contains(string(r.Body), `"prompt"`) {
		t.Errorf("record = %+v", r)
	}
	if _, _, err := run("", "jobs", "wait", r.Ref); err != nil {
		t.Errorf("a job started with `api` should be followable: %v", err)
	}
}

func TestAPICommandRefusesOtherHosts(t *testing.T) {
	newFake(t)
	for _, path := range []string{"https://evil.example/x", "//evil.example/x", "x"} {
		if _, _, err := run("", "api", "GET", path); err == nil || !strings.Contains(err.Error(), "path on the API host") {
			t.Errorf("%s: %v", path, err)
		}
	}
}

func TestAPIErrorStatusIsReported(t *testing.T) {
	newFake(t)
	out, _, err := run("", "api", "GET", "/v1/nothing-here")
	var apiErr *api.Error
	if !errors.As(err, &apiErr) || apiErr.Status != 404 {
		t.Errorf("err = %v", err)
	}
	if !strings.Contains(out, "not found") {
		t.Errorf("the body should still reach stdout: %q", out)
	}
}

func TestEndpointTable(t *testing.T) {
	newFake(t)
	out, _ := mustRun(t, "voices", "list")
	if !strings.Contains(out, `"voice_id": "v1"`) {
		t.Errorf("stdout = %s", out)
	}
	if _, _, err := run("", "billing", "spend"); err == nil {
		t.Error("spend should require its dates")
	}
}

func TestPublicCommandsNeedNoKey(t *testing.T) {
	f := newFake(t)
	t.Setenv("PIKA_API_KEY", "")
	out, _ := mustRun(t, "models")
	if !strings.Contains(out, testOp) || !strings.Contains(out, "$0.01/second") {
		t.Errorf("models = %s", out)
	}
	out, _ = mustRun(t, "quote", testOp, "--prompt", "x")
	if strings.TrimSpace(out) != "$0.10" {
		t.Errorf("quote = %q", out)
	}
	mustRun(t, "run", testOp, "--prompt", "x", "--dry-run")
	if len(f.submitted()) != 0 {
		t.Error("a dry run was submitted")
	}
}

func TestNoKeyIsAnAuthError(t *testing.T) {
	f := newFake(t)
	t.Setenv("PIKA_API_KEY", "")
	_, _, err := run("", "run", testOp, "--prompt", "x", "--yes")
	if err == nil || exitStatus(err) != exitAuth {
		t.Errorf("err = %v, exit %d", err, exitStatus(err))
	}
	if len(f.submitted()) != 0 {
		t.Error("submitted without a key")
	}
}

func TestBadKeyIsAnAuthError(t *testing.T) {
	newFake(t)
	t.Setenv("PIKA_API_KEY", "wrong")
	_, _, err := run("", "balance")
	if err == nil || exitStatus(err) != exitAuth {
		t.Errorf("err = %v, exit %d", err, exitStatus(err))
	}
}

func TestAuthLoginChecksThenSaves(t *testing.T) {
	newFake(t)
	t.Setenv("PIKA_API_KEY", "")
	path := os.Getenv("PIKACTL_CONFIG")
	if _, _, err := run("wrong-key\n", "auth", "login", "--config", path); err == nil {
		t.Error("a bad key should be refused")
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("a bad key was saved")
	}
	out, _, err := run(testKey+"\n", "auth", "login", "--config", path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, testKey) {
		t.Error("the key was echoed")
	}
	if !strings.Contains(out, "$5.00") {
		t.Errorf("stdout = %s", out)
	}
	if out, _ := mustRun(t, "auth", "status", "--config", path); !strings.Contains(out, "works") || strings.Contains(out, testKey) {
		t.Errorf("status = %s", out)
	}
	mustRun(t, "auth", "logout", "--config", path)
	if _, _, err := run("", "auth", "status", "--config", path); err == nil {
		t.Error("the key should be gone")
	}
}

func TestSavedCatalogIsUsedWhenTheAPIIsDown(t *testing.T) {
	f := newFake(t)
	mustRun(t, "run", testOp, "--help")
	mustRun(t, "models")
	f.api.Close()
	out, errOut := mustRun(t, "run", testOp, "--help")
	if !strings.Contains(out, "--prompt") {
		t.Errorf("help = %s", out)
	}
	if !strings.Contains(errOut, "saved") {
		t.Errorf("using a stale copy should be said out loud: %s", errOut)
	}
}

func TestCompetitorsSetPikaBesideTheCheapestOther(t *testing.T) {
	newFake(t)
	t.Setenv("PIKA_API_KEY", "")
	out, errOut := mustRun(t, "models", "competitors", "test")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("the filter should keep one row: %s", out)
	}
	for _, want := range []string{testOp, "resolution=480p", "$0.01/second", "fal $0.14/second", "2"} {
		if !strings.Contains(lines[1], want) {
			t.Errorf("row lacks %q: %s", want, lines[1])
		}
	}
	if !strings.Contains(errOut, "1 of 2 rows") {
		t.Errorf("stderr = %s", errOut)
	}
	out, _ = mustRun(t, "--json", "models", "competitors", "test")
	if !strings.Contains(out, `"competitor": "runway"`) || strings.Contains(out, "pika/other") {
		t.Errorf("json = %s", out)
	}
}

func TestUnknownCategoryNamesTheRealOnes(t *testing.T) {
	newFake(t)
	_, errOut := mustRun(t, "models", "--category", "nonsense")
	if !strings.Contains(errOut, "categories: llm, video") {
		t.Errorf("stderr = %s", errOut)
	}
}

func TestUnknownOperationSuggestsCloseOnes(t *testing.T) {
	newFake(t)
	_, _, err := run("", "run", "pika/test/text-to-vidoe", "--prompt", "x")
	if err == nil || !strings.Contains(err.Error(), testOp) {
		t.Errorf("err = %v", err)
	}
}

func TestHistoryIsWrittenBeforeTheRequestIsSent(t *testing.T) {
	f := newFake(t)
	var sawRecord bool
	f.submitReply = func(w http.ResponseWriter) {
		data, err := os.ReadFile(filepath.Join(os.Getenv("PIKACTL_DATA"), "history.jsonl"))
		sawRecord = err == nil && strings.Contains(string(data), `"type":"request"`)
		_, _ = io.WriteString(w, `{"id":"media_1","status":"queued"}`)
	}
	mustRun(t, "run", testOp, "--prompt", "x", "--no-wait", "--yes")
	if !sawRecord {
		t.Error("the request should be on disk before money can move")
	}
}

func TestNoSpendingWithoutAHistory(t *testing.T) {
	f := newFake(t)
	// A file where the data directory should be makes the history unwritable.
	dataDir := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(dataDir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PIKACTL_DATA", dataDir)
	_, _, err := run("", "run", testOp, "--prompt", "x", "--yes")
	if err == nil || !strings.Contains(err.Error(), "nothing was sent") {
		t.Errorf("err = %v", err)
	}
	if len(f.submitted()) != 0 {
		t.Error("spent with no record of it")
	}
}
