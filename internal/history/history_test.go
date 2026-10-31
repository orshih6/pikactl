package history

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func charge(n int64) *int64 { return &n }

func TestFoldsEventsIntoARecord(t *testing.T) {
	s := Open(t.TempDir())
	ref := s.NewRef()
	events := []Event{
		{Type: EventRequest, Ref: ref, Kind: KindMedia, APIID: "a/b/c", Body: json.RawMessage(`{"prompt":"x"}`), QuoteMicroUSD: charge(100), Note: "first"},
		{Type: EventResponse, Ref: ref, JobID: "media_1", Status: "queued"},
		{Type: EventResponse, Ref: ref, JobID: "media_1", Status: "completed", Response: json.RawMessage(`{"id":"media_1"}`)},
		{Type: EventBilling, Ref: ref, ChargeMicroUSD: charge(90)},
		{Type: EventFiles, Ref: ref, Files: []File{{Name: "video", Path: "/a"}}},
		{Type: EventFiles, Ref: ref, Files: []File{{Name: "video", Path: "/b"}}},
	}
	for _, e := range events {
		if err := s.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	records, skipped, err := s.Load()
	if err != nil || skipped != 0 || len(records) != 1 {
		t.Fatalf("Load = %d records, %d skipped, %v", len(records), skipped, err)
	}
	r := records[0]
	if r.Status != "completed" || r.JobID != "media_1" || r.APIID != "a/b/c" || r.Note != "first" {
		t.Errorf("record = %+v", r)
	}
	if *r.ChargeMicroUSD != 90 || *r.QuoteMicroUSD != 100 {
		t.Errorf("money = %d, %d", *r.ChargeMicroUSD, *r.QuoteMicroUSD)
	}
	if len(r.Files) != 1 || r.Files[0].Path != "/b" {
		t.Errorf("saving an output twice should replace it: %+v", r.Files)
	}
	if !r.Terminal() {
		t.Error("a completed record should be terminal")
	}
}

func TestStatusBeforeAnyAnswerIsUnknown(t *testing.T) {
	s := Open(t.TempDir())
	if err := s.Append(Event{Type: EventRequest, Ref: "r1", Kind: KindMedia}); err != nil {
		t.Fatal(err)
	}
	r, err := s.Find("r1")
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != StatusUnknown || r.Terminal() {
		t.Errorf("a request with no answer may have run, so must read as unknown: %+v", r)
	}
}

func TestOldFilesStayReadable(t *testing.T) {
	s := Open(t.TempDir())
	lines := []string{
		// A later version's fields and event types must not disturb this one.
		`{"v":99,"at":"2026-01-01T00:00:00Z","type":"request","ref":"r1","kind":"media","api_id":"a/b/c","field_from_the_future":{"x":1}}`,
		`{"v":99,"at":"2026-01-01T00:00:01Z","type":"event_from_the_future","ref":"r1"}`,
		// Neither must a line cut short by a crash, or one that is not ours.
		`{"v":1,"at":"2026-01-01T00:00:02Z","type":"resp`,
		`not json at all`,
		``,
		`{"v":1,"at":"2026-01-01T00:00:03Z","type":"response","ref":"r1","status":"completed"}`,
	}
	if err := os.WriteFile(s.Path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	records, skipped, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if skipped != 2 {
		t.Errorf("skipped = %d, want the 2 damaged lines", skipped)
	}
	if len(records) != 1 || records[0].Status != "completed" || records[0].APIID != "a/b/c" {
		t.Errorf("records = %+v", records)
	}
}

func TestAppendNeverChangesExistingLines(t *testing.T) {
	s := Open(t.TempDir())
	if err := s.Append(Event{Type: EventRequest, Ref: "r1"}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Append(Event{Type: EventDeleted, Ref: "r1"}); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(after), string(before)) {
		t.Error("an append changed what was already there")
	}
	if r, _ := s.Find("r1"); !r.Deleted {
		t.Error("the deleted mark should be folded in")
	}
}

func TestFileIsPrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested")
	s := Open(dir)
	if err := s.Append(Event{Type: EventRequest, Ref: "r1"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("history mode = %o, want 600: it holds prompts", perm)
	}
}

func TestHugeAnswersAreNotKept(t *testing.T) {
	s := Open(t.TempDir())
	huge := json.RawMessage(`"` + strings.Repeat("x", maxResponse+1) + `"`)
	if err := s.Append(Event{Type: EventResponse, Ref: "r1", Status: "ok", Response: huge}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > 1024 {
		t.Errorf("history grew to %d bytes", info.Size())
	}
}

func TestFind(t *testing.T) {
	s := Open(t.TempDir())
	for _, e := range []Event{
		{Type: EventRequest, Ref: "20261031-aaa", JobID: "media_111"},
		{Type: EventRequest, Ref: "20261031-bbb", JobID: "media_222"},
	} {
		if err := s.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	for id, want := range map[string]string{
		"20261031-aaa": "20261031-aaa",
		"media_222":    "20261031-bbb",
		"20261031-b":   "20261031-bbb",
		"media_1":      "20261031-aaa",
	} {
		r, err := s.Find(id)
		if err != nil {
			t.Errorf("Find(%q): %v", id, err)
			continue
		}
		if r.Ref != want {
			t.Errorf("Find(%q) = %s, want %s", id, r.Ref, want)
		}
	}
	if _, err := s.Find("20261031"); err == nil || !strings.Contains(err.Error(), "matches 2") {
		t.Errorf("an ambiguous prefix should say so: %v", err)
	}
	if _, err := s.Find("zzz"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Find(zzz) = %v", err)
	}
}

func TestNewRefsSortByTime(t *testing.T) {
	s := Open(t.TempDir())
	now := time.Date(2026, 10, 31, 15, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }
	first := s.NewRef()
	now = now.Add(time.Second)
	second := s.NewRef()
	if first >= second {
		t.Errorf("%s should sort before %s", first, second)
	}
	if !strings.HasPrefix(first, "20261031-150000-") {
		t.Errorf("ref = %s", first)
	}
}
