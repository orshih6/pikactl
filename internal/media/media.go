// Package media drives Pika's asynchronous job API: submit, poll, upload and
// download. It is the same for every model, because the catalog supplies each
// operation's path and body.
package media

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/orshih6/pikactl/internal/api"
)

// Job statuses Pika documents as final. Any other value, including one added
// later, means keep waiting.
const (
	StatusCompleted = "completed"
	StatusFailed    = "failed"
)

// submitAttempts bounds repeats of a submit whose answer never arrived.
const submitAttempts = 3

// Service runs jobs through one API client.
type Service struct {
	API *api.Client
}

// Job is the job object. Raw keeps every field as sent, including ones this
// version does not know.
type Job struct {
	ID      string          `json:"id"`
	Status  string          `json:"status"`
	ModelID string          `json:"model_id"`
	Output  json.RawMessage `json:"output"`
	Error   *JobError       `json:"error"`
	Billing *Billing        `json:"billing"`
	// FreeGeneration names the promotion that paid for the job, if one did.
	FreeGeneration *struct {
		PromoID string `json:"promo_id"`
	} `json:"free_generation"`
	Raw json.RawMessage `json:"-"`
}

// JobError is why a job failed. Code is the stable value to branch on.
type JobError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Billing is what a terminal job charged, once known.
type Billing struct {
	State          string `json:"state"`
	ChargeMicroUSD *int64 `json:"charge_micro_usd"`
}

// Terminal reports whether the job will not change status again.
func (j *Job) Terminal() bool {
	return j.Status == StatusCompleted || j.Status == StatusFailed
}

// Charge returns the settled charge, or nil while there is none to record.
func (j *Job) Charge() *int64 {
	if j.Billing != nil && j.Billing.State == "settled" {
		return j.Billing.ChargeMicroUSD
	}
	return nil
}

func parseJob(raw []byte) (*Job, error) {
	var job Job
	if err := json.Unmarshal(raw, &job); err != nil {
		return nil, err
	}
	if job.ID == "" {
		return nil, errors.New("no job id in the answer")
	}
	job.Raw = append(json.RawMessage(nil), raw...)
	return &job, nil
}

// NewIdempotencyKey mints a fresh key for one submit.
func NewIdempotencyKey() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Submit posts a job. Repeating the same key and body is safe by Pika's
// contract, so a submit that got no answer at all is retried under its key.
//
// A rejected submit (no balance, rate limit, unpriceable input) can come back
// as a failed job with a 4xx or 5xx status, so both results can be set. That
// key now replays the failure: a retry must use a fresh one.
func (s *Service) Submit(ctx context.Context, apiPath string, body []byte, key string) (*Job, error) {
	var lastErr error
	for attempt := 0; attempt < submitAttempts; attempt++ {
		if attempt > 0 {
			if err := s.API.Sleep(ctx, time.Duration(attempt)*2*time.Second); err != nil {
				return nil, err
			}
		}
		resp, err := s.API.Do(ctx, api.Request{
			Method: http.MethodPost,
			Path:   apiPath,
			Body:   body,
			Header: http.Header{"Idempotency-Key": {key}},
		})
		if err == nil {
			job, perr := parseJob(resp.Body)
			if perr != nil {
				return nil, fmt.Errorf("submit answered with something that is not a job: %w", perr)
			}
			return job, nil
		}
		var apiErr *api.Error
		if errors.As(err, &apiErr) {
			if job, perr := parseJob(apiErr.Body); perr == nil {
				return job, err
			}
			return nil, err
		}
		if ctx.Err() != nil {
			return nil, err
		}
		lastErr = err
	}
	return nil, &LostError{Key: key, Err: lastErr}
}

// LostError is a submit whose answer never arrived. The job may exist and may
// be charged, so it is reported as unknown and never as a failure.
type LostError struct {
	Key string
	Err error
}

func (e *LostError) Error() string {
	return fmt.Sprintf("no answer to the submit, so it may or may not have started (repeat it safely with --idempotency-key %s): %v", e.Key, e.Err)
}

func (e *LostError) Unwrap() error { return e.Err }

// Get reads a job.
func (s *Service) Get(ctx context.Context, id string) (*Job, error) {
	resp, err := s.API.Do(ctx, api.Request{Method: http.MethodGet, Path: jobPath(id)})
	if err != nil {
		return nil, err
	}
	return parseJob(resp.Body)
}

func jobPath(id string) string { return "/v1/media/jobs/" + url.PathEscape(id) }

// WaitOptions tunes Wait. Zero values take sensible defaults.
type WaitOptions struct {
	Interval    time.Duration
	MaxInterval time.Duration
	Timeout     time.Duration
	// OnChange is told each new status.
	OnChange func(*Job)
}

// Wait polls until the job is terminal. A brief API outage does not end the
// wait; a key or job the API rejects does. Giving up says nothing about the
// job, which keeps running on Pika's side.
func (s *Service) Wait(ctx context.Context, id string, opts WaitOptions) (*Job, error) {
	if opts.Interval == 0 {
		opts.Interval = 3 * time.Second
	}
	if opts.MaxInterval == 0 {
		opts.MaxInterval = 15 * time.Second
	}
	if opts.Timeout == 0 {
		opts.Timeout = 30 * time.Minute
	}
	deadline, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	seen, interval := "", opts.Interval
	for {
		job, err := s.Get(deadline, id)
		switch {
		case err == nil:
			if job.Status != seen {
				seen = job.Status
				if opts.OnChange != nil {
					opts.OnChange(job)
				}
			}
			if job.Terminal() {
				return job, nil
			}
		case ctx.Err() != nil:
			return nil, ctx.Err()
		case deadline.Err() != nil:
			return nil, fmt.Errorf("gave up after %s: %w", opts.Timeout, deadline.Err())
		case !api.Transient(err):
			return nil, err
		}
		if err := s.API.Sleep(deadline, interval); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("gave up after %s: %w", opts.Timeout, err)
		}
		interval = min(interval*3/2, opts.MaxInterval)
	}
}

// Settle re-reads a terminal job until its charge is final, on the schedule
// the docs give for a pending charge. If it still is not, the job comes back
// as it is and `history sync` picks the charge up later.
func (s *Service) Settle(ctx context.Context, job *Job) *Job {
	for _, wait := range []time.Duration{time.Second, 2 * time.Second, 5 * time.Second} {
		if job.Billing != nil && job.Billing.State != "pending" {
			return job
		}
		if err := s.API.Sleep(ctx, wait); err != nil {
			return job
		}
		fresh, err := s.Get(ctx, job.ID)
		if err != nil {
			return job
		}
		job = fresh
	}
	return job
}

// Delete erases a job's stored media and its captured request on Pika's side.
func (s *Service) Delete(ctx context.Context, id string) error {
	_, err := s.API.Do(ctx, api.Request{Method: http.MethodDelete, Path: jobPath(id), Retry: true})
	return err
}

// Content returns a completed job's download URL.
func (s *Service) Content(ctx context.Context, id string) (string, error) {
	resp, err := s.API.Do(ctx, api.Request{Method: http.MethodGet, Path: jobPath(id) + "/content"})
	if err != nil {
		return "", err
	}
	var out struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		return "", err
	}
	return out.URL, nil
}

// Upload is a local file now hosted by Pika.
type Upload struct {
	LocalPath   string `json:"local_path"`
	URL         string `json:"url"`
	ContentType string `json:"content_type"`
	Bytes       int64  `json:"bytes"`
}

// extraTypes covers formats the system's MIME table often lacks; a wrong type
// is rejected with 415 and a missing one would not get that far.
var extraTypes = map[string]string{
	".mp4": "video/mp4", ".mov": "video/quicktime", ".webm": "video/webm", ".mkv": "video/x-matroska",
	".mp3": "audio/mpeg", ".wav": "audio/wav", ".m4a": "audio/mp4", ".flac": "audio/flac", ".ogg": "audio/ogg", ".aac": "audio/aac",
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".webp": "image/webp", ".gif": "image/gif",
}

func contentTypeOf(localPath string, f *os.File) (string, error) {
	ext := strings.ToLower(filepath.Ext(localPath))
	if t, ok := extraTypes[ext]; ok {
		return t, nil
	}
	if t := mime.TypeByExtension(ext); t != "" {
		return strings.SplitN(t, ";", 2)[0], nil
	}
	head := make([]byte, 512)
	n, err := f.ReadAt(head, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.SplitN(http.DetectContentType(head[:n]), ";", 2)[0], nil
}

// Upload sends a local file through a presigned URL and returns its hosted
// URL, which is what a model field takes. Size limits are left to the API,
// which answers 413 and 415 and can change them without a new pikactl.
func (s *Service) Upload(ctx context.Context, localPath string) (*Upload, error) {
	f, err := os.Open(localPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("%s is a directory", localPath)
	}
	contentType, err := contentTypeOf(localPath, f)
	if err != nil {
		return nil, err
	}
	reqBody, err := json.Marshal(map[string]any{"content_type": contentType, "size_bytes": info.Size()})
	if err != nil {
		return nil, err
	}
	resp, err := s.API.Do(ctx, api.Request{Method: http.MethodPost, Path: "/v1/media/uploads", Body: reqBody})
	if err != nil {
		return nil, err
	}
	var minted struct {
		UploadURL string                     `json:"upload_url"`
		URL       string                     `json:"url"`
		Headers   map[string]json.RawMessage `json:"headers"`
	}
	if err := json.Unmarshal(resp.Body, &minted); err != nil {
		return nil, fmt.Errorf("reading the upload answer: %w", err)
	}
	if minted.UploadURL == "" || minted.URL == "" {
		return nil, errors.New("the upload answer had no upload_url or url")
	}
	headers := make(map[string]string, len(minted.Headers))
	for name, raw := range minted.Headers {
		var text string
		if json.Unmarshal(raw, &text) != nil {
			text = string(raw) // a number, such as Content-Length
		}
		headers[name] = text
	}
	if err := s.API.PutSigned(ctx, minted.UploadURL, headers, f, info.Size()); err != nil {
		return nil, fmt.Errorf("uploading %s: %w", localPath, err)
	}
	return &Upload{LocalPath: localPath, URL: minted.URL, ContentType: contentType, Bytes: info.Size()}, nil
}

// DeleteUpload erases one uploaded file by its hosted URL.
func (s *Service) DeleteUpload(ctx context.Context, hosted string) error {
	_, err := s.API.Do(ctx, api.Request{
		Method: http.MethodDelete,
		Path:   "/v1/media/uploads",
		Query:  url.Values{"url": {hosted}},
		Retry:  true,
	})
	return err
}

// Asset is one downloadable file in a job's output.
type Asset struct {
	Name        string
	URL         string
	ContentType string
}

// Assets finds every file in a job's output, whatever its shape: output.video,
// output.images[], stems and archives today, and whatever is added later.
func Assets(output json.RawMessage) []Asset {
	var root any
	if len(output) == 0 || json.Unmarshal(output, &root) != nil {
		return nil
	}
	var out []Asset
	walkAssets(root, nil, &out)
	return out
}

func walkAssets(v any, trail []string, out *[]Asset) {
	switch node := v.(type) {
	case map[string]any:
		if u, ok := node["url"].(string); ok && isHTTP(u) {
			contentType, _ := node["content_type"].(string)
			*out = append(*out, Asset{Name: assetName(trail), URL: u, ContentType: contentType})
		}
		keys := make([]string, 0, len(node))
		for k := range node {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if u, ok := node[k].(string); ok && strings.HasSuffix(k, "_url") && isHTTP(u) {
				*out = append(*out, Asset{Name: assetName(append(trail, strings.TrimSuffix(k, "_url"))), URL: u})
				continue
			}
			walkAssets(node[k], append(trail[:len(trail):len(trail)], k), out)
		}
	case []any:
		for i, item := range node {
			if u, ok := item.(string); ok && isHTTP(u) {
				*out = append(*out, Asset{Name: assetName(append(trail, fmt.Sprint(i))), URL: u})
				continue
			}
			walkAssets(item, append(trail[:len(trail):len(trail)], fmt.Sprint(i)), out)
		}
	}
}

func isHTTP(s string) bool {
	return strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "http://")
}

func assetName(trail []string) string {
	if len(trail) == 0 {
		return "output"
	}
	return strings.Join(trail, "-")
}

// File is an output saved to disk.
type File struct {
	Name        string `json:"name"`
	URL         string `json:"url"`
	Path        string `json:"path"`
	Bytes       int64  `json:"bytes"`
	SHA256      string `json:"sha256"`
	ContentType string `json:"content_type,omitempty"`
}

var unsafeName = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)
var safeExtension = regexp.MustCompile(`^\.[a-z0-9]{1,8}$`)

// SafeName makes a value from the API or a command line usable as one path
// segment, so it cannot climb out of the outputs directory.
func SafeName(s string) string {
	s = strings.Trim(unsafeName.ReplaceAllString(s, "_"), ".")
	if s == "" {
		return "output"
	}
	return s
}

func extensionFor(rawURL, contentType string) string {
	if u, err := url.Parse(rawURL); err == nil {
		if ext := strings.ToLower(path.Ext(u.Path)); safeExtension.MatchString(ext) {
			return ext
		}
	}
	if contentType != "" {
		if exts, _ := mime.ExtensionsByType(contentType); len(exts) > 0 {
			sort.Strings(exts)
			if safeExtension.MatchString(exts[0]) {
				return exts[0]
			}
		}
	}
	return ".bin"
}

// Download saves an asset under dir. It writes to a temporary name first, so a
// file with the final name is always whole.
func (s *Service) Download(ctx context.Context, asset Asset, dir string) (File, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return File{}, err
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			if err := s.API.Sleep(ctx, time.Duration(attempt)*2*time.Second); err != nil {
				return File{}, err
			}
		}
		file, err := s.downloadOnce(ctx, asset, dir)
		if err == nil {
			return file, nil
		}
		lastErr = err
		if ctx.Err() != nil || !api.Transient(err) {
			break
		}
	}
	return File{}, lastErr
}

func (s *Service) downloadOnce(ctx context.Context, asset Asset, dir string) (File, error) {
	tmp, err := os.CreateTemp(dir, ".download-*")
	if err != nil {
		return File{}, err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()

	hash := sha256.New()
	counter := &countingWriter{}
	contentType, err := s.API.Fetch(ctx, asset.URL, io.MultiWriter(tmp, hash, counter))
	if err != nil {
		return File{}, err
	}
	if contentType == "" {
		contentType = asset.ContentType
	}
	if err := tmp.Close(); err != nil {
		return File{}, err
	}
	final := filepath.Join(dir, SafeName(asset.Name)+extensionFor(asset.URL, contentType))
	if err := os.Rename(tmp.Name(), final); err != nil {
		return File{}, err
	}
	return File{
		Name:        asset.Name,
		URL:         asset.URL,
		Path:        final,
		Bytes:       counter.n,
		SHA256:      hex.EncodeToString(hash.Sum(nil)),
		ContentType: contentType,
	}, nil
}

type countingWriter struct{ n int64 }

func (c *countingWriter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	return len(p), nil
}

// CopyFile copies a saved output somewhere the user asked for. The original
// stays put, because the history points at it.
func CopyFile(from, to string) error {
	data, err := os.Open(from)
	if err != nil {
		return err
	}
	defer data.Close()
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	out, err := os.Create(to)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, data); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// CompactJSON renders raw JSON on one line, for messages.
func CompactJSON(raw []byte) string {
	var buf bytes.Buffer
	if json.Compact(&buf, raw) != nil {
		return string(raw)
	}
	return buf.String()
}
