// Package history is pikactl's permanent record of what was requested, what it
// cost and where the results went. Pika has no endpoint that lists past jobs,
// so this is the only list there is.
//
// It is an append-only JSON Lines file. Nothing rewrites or removes a line, so
// an old entry stays readable after the API, the models or pikactl itself
// change: each line holds the request and the API's answers verbatim, unknown
// fields are ignored on the way in, and a record is rebuilt by folding its
// events in order.
package history

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// schemaVersion is stamped on each event so a future reader can tell old lines
// from new ones.
const schemaVersion = 1

// maxResponse caps a stored answer. An LLM reply can carry base64 images, and
// one such call should not bloat the file every later command reads.
const maxResponse = 1 << 20

// The kinds of event a record is folded from.
const (
	EventRequest  = "request"  // written before the request is sent
	EventResponse = "response" // the API answered, or the job changed status
	EventBilling  = "billing"  // the charge settled
	EventFiles    = "files"    // outputs were saved to disk
	EventDeleted  = "deleted"  // the job's media was erased on Pika's side
)

// The kinds of request a record can describe.
const (
	KindMedia    = "media"
	KindChat     = "chat"
	KindDecision = "decision"
	KindAPI      = "api"
)

// Status of a request whose answer never arrived. It may still have happened.
const StatusUnknown = "unknown"

// File is an output saved to disk.
type File struct {
	Name        string `json:"name"`
	URL         string `json:"url"`
	Path        string `json:"path"`
	Bytes       int64  `json:"bytes"`
	SHA256      string `json:"sha256"`
	ContentType string `json:"content_type,omitempty"`
}

// Event is one line of the file.
type Event struct {
	V    int       `json:"v"`
	At   time.Time `json:"at"`
	Type string    `json:"type"`
	Ref  string    `json:"ref"`

	Kind           string            `json:"kind,omitempty"`
	APIID          string            `json:"api_id,omitempty"`
	Method         string            `json:"method,omitempty"`
	Path           string            `json:"path,omitempty"`
	Body           json.RawMessage   `json:"body,omitempty"`
	IdempotencyKey string            `json:"idempotency_key,omitempty"`
	QuoteMicroUSD  *int64            `json:"quote_micro_usd,omitempty"`
	Note           string            `json:"note,omitempty"`
	RerunOf        string            `json:"rerun_of,omitempty"`
	Uploads        map[string]string `json:"uploads,omitempty"`

	JobID             string          `json:"job_id,omitempty"`
	Status            string          `json:"status,omitempty"`
	HTTPStatus        int             `json:"http_status,omitempty"`
	ErrorCode         string          `json:"error_code,omitempty"`
	ErrorMessage      string          `json:"error_message,omitempty"`
	Response          json.RawMessage `json:"response,omitempty"`
	ResponseTruncated bool            `json:"response_truncated,omitempty"`

	ChargeMicroUSD *int64 `json:"charge_micro_usd,omitempty"`

	Files []File `json:"files,omitempty"`
}

// Record is everything known about one request.
type Record struct {
	Ref            string            `json:"ref"`
	Kind           string            `json:"kind"`
	APIID          string            `json:"api_id,omitempty"`
	Method         string            `json:"method,omitempty"`
	Path           string            `json:"path,omitempty"`
	Body           json.RawMessage   `json:"body,omitempty"`
	IdempotencyKey string            `json:"idempotency_key,omitempty"`
	Note           string            `json:"note,omitempty"`
	RerunOf        string            `json:"rerun_of,omitempty"`
	Uploads        map[string]string `json:"uploads,omitempty"`
	QuoteMicroUSD  *int64            `json:"quote_micro_usd,omitempty"`
	ChargeMicroUSD *int64            `json:"charge_micro_usd,omitempty"`
	JobID          string            `json:"job_id,omitempty"`
	Status         string            `json:"status"`
	HTTPStatus     int               `json:"http_status,omitempty"`
	ErrorCode      string            `json:"error_code,omitempty"`
	ErrorMessage   string            `json:"error_message,omitempty"`
	Response       json.RawMessage   `json:"response,omitempty"`
	Files          []File            `json:"files,omitempty"`
	Deleted        bool              `json:"deleted,omitempty"`
	CreatedAt      time.Time         `json:"created_at"`
	UpdatedAt      time.Time         `json:"updated_at"`
}

// Terminal reports whether a job's outcome is known.
func (r *Record) Terminal() bool {
	switch r.Status {
	case "completed", "failed", "ok", "error":
		return true
	}
	return false
}

// Store is the history file.
type Store struct {
	Path string
	// Now is a field so tests can fix the clock.
	Now func() time.Time
}

// Open returns the store kept under dir. It is created on first write.
func Open(dir string) *Store {
	return &Store{Path: filepath.Join(dir, "history.jsonl"), Now: time.Now}
}

// NewRef mints a record's local ID: sortable and short enough to type.
func (s *Store) NewRef() string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	return s.Now().UTC().Format("20060102-150405") + "-" + hex.EncodeToString(b[:])
}

// Append adds one event. A single O_APPEND write keeps lines whole when two
// pikactl processes write at once.
func (s *Store) Append(e Event) error {
	if len(e.Response) > maxResponse {
		e.Response, e.ResponseTruncated = nil, true
	}
	e.V = schemaVersion
	if e.At.IsZero() {
		e.At = s.Now().UTC()
	}
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(s.Path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(line, '\n'))
	return err
}

// Load folds the file into records, oldest first. A damaged line, such as one
// cut short by a crash, is skipped and counted rather than losing the rest.
func (s *Store) Load() (records []*Record, skipped int, err error) {
	f, err := os.Open(s.Path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, 0, nil
		}
		return nil, 0, err
	}
	defer f.Close()

	byRef := map[string]*Record{}
	reader := bufio.NewReader(f)
	for {
		line, readErr := reader.ReadBytes('\n')
		if trimmed := strings.TrimSpace(string(line)); trimmed != "" {
			var e Event
			if json.Unmarshal([]byte(trimmed), &e) != nil || e.Ref == "" {
				skipped++
			} else {
				rec := byRef[e.Ref]
				if rec == nil {
					rec = &Record{Ref: e.Ref, CreatedAt: e.At}
					byRef[e.Ref] = rec
					records = append(records, rec)
				}
				rec.apply(e)
			}
		}
		if errors.Is(readErr, io.EOF) {
			return records, skipped, nil
		}
		if readErr != nil {
			return records, skipped, readErr
		}
	}
}

func (r *Record) apply(e Event) {
	r.UpdatedAt = e.At
	switch e.Type {
	case EventRequest:
		r.Kind, r.APIID, r.Method, r.Path = e.Kind, e.APIID, e.Method, e.Path
		r.Body, r.IdempotencyKey, r.Note = e.Body, e.IdempotencyKey, e.Note
		r.RerunOf, r.Uploads, r.QuoteMicroUSD = e.RerunOf, e.Uploads, e.QuoteMicroUSD
		r.JobID = firstNonEmpty(e.JobID, r.JobID)
		r.Status = firstNonEmpty(e.Status, StatusUnknown)
	case EventResponse:
		r.JobID = firstNonEmpty(e.JobID, r.JobID)
		r.Status = firstNonEmpty(e.Status, r.Status)
		if e.HTTPStatus != 0 {
			r.HTTPStatus = e.HTTPStatus
		}
		r.ErrorCode, r.ErrorMessage = e.ErrorCode, e.ErrorMessage
		if e.Response != nil {
			r.Response = e.Response
		}
	case EventBilling:
		r.ChargeMicroUSD = e.ChargeMicroUSD
	case EventFiles:
		r.Files = mergeFiles(r.Files, e.Files)
	case EventDeleted:
		r.Deleted = true
	}
}

// mergeFiles keeps one entry per output name, the latest winning, so saving an
// output again updates it rather than listing it twice.
func mergeFiles(have, add []File) []File {
	for _, f := range add {
		replaced := false
		for i := range have {
			if have[i].Name == f.Name {
				have[i], replaced = f, true
				break
			}
		}
		if !replaced {
			have = append(have, f)
		}
	}
	return have
}

// Find returns the record with this ref or job ID, or the only one a prefix
// of either matches.
func (s *Store) Find(id string) (*Record, error) {
	records, _, err := s.Load()
	if err != nil {
		return nil, err
	}
	var matches []*Record
	for _, r := range records {
		if r.Ref == id || r.JobID == id {
			return r, nil
		}
		if strings.HasPrefix(r.Ref, id) || (r.JobID != "" && strings.HasPrefix(r.JobID, id)) {
			matches = append(matches, r)
		}
	}
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("%w: %q", ErrNotFound, id)
	case 1:
		return matches[0], nil
	}
	return nil, fmt.Errorf("%q matches %d history entries; give more of it", id, len(matches))
}

// ErrNotFound means no history entry has that ID.
var ErrNotFound = errors.New("nothing in the history with that ID")

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
