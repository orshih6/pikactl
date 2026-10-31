// Package api is an HTTP client for the Pika API. It knows about the key,
// retries and error shapes, and nothing about models or jobs.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// HeaderKey carries the API key. The OpenAPI document reserves Bearer on the
// /billing and /v1/webhooks reads for console session tokens, so this is the
// one header accepted everywhere.
const HeaderKey = "X-API-Key"

const (
	defaultTimeout = 60 * time.Second
	maxRetries     = 3
	maxRetryWait   = 60 * time.Second
)

// Client talks to one Pika API host.
type Client struct {
	BaseURL   string
	Key       string
	UserAgent string
	// HTTP makes every request that may carry the key.
	HTTP *http.Client
	// Plain never carries the key: signed storage URLs and documentation.
	Plain *http.Client
	// Sleep waits between retries. A field so tests need not wait.
	Sleep func(context.Context, time.Duration) error
}

// New returns a client for baseURL.
func New(baseURL, key, userAgent string) *Client {
	return &Client{
		BaseURL:   strings.TrimRight(baseURL, "/"),
		Key:       key,
		UserAgent: userAgent,
		HTTP:      &http.Client{CheckRedirect: stripKeyOnRedirect},
		Plain:     &http.Client{},
		Sleep:     sleep,
	}
}

// stripKeyOnRedirect drops the key when a redirect leaves the host it was
// meant for. net/http only does this by itself for Authorization and cookies,
// so a custom header such as X-API-Key would otherwise follow the redirect.
func stripKeyOnRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	if first := via[0].URL; req.URL.Host != first.Host || req.URL.Scheme != first.Scheme {
		req.Header.Del(HeaderKey)
	}
	return nil
}

func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Request is one call to the API.
type Request struct {
	Method string
	// Path is relative to the API host; see CheckPath.
	Path   string
	Query  url.Values
	Body   []byte
	Header http.Header
	// Retry marks a non-GET request as safe to repeat. GET always is.
	Retry bool
	// Timeout bounds one attempt. Zero means the default.
	Timeout time.Duration
}

// Response is a successful answer.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// CheckPath rejects anything but a path on the API host, so that the key
// cannot be sent elsewhere by a mistyped or hostile argument.
func CheckPath(path string) error {
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		return fmt.Errorf("%q is not a path on the API host: it must start with a single /", path)
	}
	for _, segment := range strings.Split(strings.SplitN(path, "?", 2)[0], "/") {
		if segment == ".." {
			return fmt.Errorf("%q must not contain .. segments", path)
		}
	}
	return nil
}

func (c *Client) newRequest(ctx context.Context, r Request) (*http.Request, error) {
	if err := CheckPath(r.Path); err != nil {
		return nil, err
	}
	target := c.BaseURL + r.Path
	if len(r.Query) > 0 {
		sep := "?"
		if strings.Contains(r.Path, "?") {
			sep = "&"
		}
		target += sep + r.Query.Encode()
	}
	var body io.Reader
	if r.Body != nil {
		body = bytes.NewReader(r.Body)
	}
	req, err := http.NewRequestWithContext(ctx, r.Method, target, body)
	if err != nil {
		return nil, err
	}
	for name, values := range r.Header {
		req.Header[http.CanonicalHeaderKey(name)] = values
	}
	if r.Body != nil && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Key != "" {
		req.Header.Set(HeaderKey, c.Key)
	}
	req.Header.Set("User-Agent", c.UserAgent)
	return req, nil
}

// Do makes the request and returns the response, or an *Error for a status of
// 400 and above. GET, and a request marked Retry, is repeated on a transient
// failure with Retry-After honoured. Anything else is tried once, because a
// write whose answer was lost may have happened.
func (c *Client) Do(ctx context.Context, r Request) (*Response, error) {
	attempts := 1
	if r.Retry || r.Method == http.MethodGet {
		attempts += maxRetries
	}
	var err error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			if serr := c.Sleep(ctx, retryWait(i, err)); serr != nil {
				return nil, serr
			}
		}
		var resp *Response
		if resp, err = c.once(ctx, r); err == nil {
			return resp, nil
		}
		if ctx.Err() != nil || !Transient(err) {
			return nil, err
		}
	}
	return nil, err
}

func (c *Client) once(ctx context.Context, r Request) (*Response, error) {
	timeout := r.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := c.newRequest(ctx, r)
	if err != nil {
		return nil, &badRequestError{err}
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, NewError(resp.StatusCode, resp.Header, body)
	}
	return &Response{Status: resp.StatusCode, Header: resp.Header, Body: body}, nil
}

// Open makes the request once and hands back the live response, so that a
// long or streamed answer reaches the caller as it arrives. The caller closes
// the body and judges the status.
func (c *Client) Open(ctx context.Context, r Request) (*http.Response, error) {
	req, err := c.newRequest(ctx, r)
	if err != nil {
		return nil, err
	}
	return c.HTTP.Do(req)
}

// Fetch copies a URL into w and returns its content type. The key is sent
// only to the API host, never to a CDN the file may live on.
func (c *Client) Fetch(ctx context.Context, rawURL string, w io.Writer) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", c.UserAgent)
	client := c.Plain
	if base, err := url.Parse(c.BaseURL); err == nil && c.Key != "" && req.URL.Host == base.Host && req.URL.Scheme == base.Scheme {
		req.Header.Set(HeaderKey, c.Key)
		client = c.HTTP
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return "", NewError(resp.StatusCode, resp.Header, body)
	}
	if _, err := io.Copy(w, resp.Body); err != nil {
		return "", err
	}
	return resp.Header.Get("Content-Type"), nil
}

// GetPublic reads a public URL such as a documentation page, without the key.
func (c *Client) GetPublic(ctx context.Context, rawURL string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, defaultTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.UserAgent)
	resp, err := c.Plain.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, NewError(resp.StatusCode, resp.Header, body)
	}
	return body, nil
}

// PutSigned uploads to a presigned URL. Storage signs Content-Type and
// Content-Length, so the headers go out verbatim and the length is declared
// rather than chunked. The key is never sent.
func (c *Client) PutSigned(ctx context.Context, rawURL string, headers map[string]string, body io.Reader, size int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, rawURL, body)
	if err != nil {
		return err
	}
	req.ContentLength = size
	for name, value := range headers {
		if !strings.EqualFold(name, "Content-Length") {
			req.Header.Set(name, value)
		}
	}
	resp, err := c.Plain.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return NewError(resp.StatusCode, resp.Header, msg)
	}
	return nil
}

// Error is a status of 400 and above. Pika answers in two shapes: a
// request-level {"message": ...}, or the failed job object for a submit it
// rejected after creating the job.
type Error struct {
	Status     int
	Code       string
	Message    string
	RetryAfter time.Duration
	// Body is the raw answer, which for a rejected submit is the job.
	Body []byte
}

// NewError builds an Error from an HTTP answer.
func NewError(status int, header http.Header, body []byte) *Error {
	e := &Error{Status: status, Body: body, RetryAfter: parseRetryAfter(header.Get("Retry-After"))}
	var shape struct {
		Message string          `json:"message"`
		Detail  json.RawMessage `json:"detail"`
		Error   json.RawMessage `json:"error"`
	}
	if json.Unmarshal(body, &shape) == nil {
		e.Message = shape.Message
		var nested struct {
			Code    any    `json:"code"`
			Type    string `json:"type"`
			Message string `json:"message"`
		}
		var plain string
		switch {
		case json.Unmarshal(shape.Error, &plain) == nil && plain != "":
			e.Message = plain
		case json.Unmarshal(shape.Error, &nested) == nil:
			if nested.Message != "" {
				e.Message = nested.Message
			}
			if code, ok := nested.Code.(string); ok {
				e.Code = code
			} else if nested.Type != "" {
				e.Code = nested.Type
			}
		}
		if e.Message == "" && json.Unmarshal(shape.Detail, &plain) == nil {
			e.Message = plain
		}
	}
	if e.Message == "" {
		e.Message = strings.TrimSpace(string(body))
		if len(e.Message) > 300 {
			e.Message = e.Message[:300] + "…"
		}
	}
	if e.Message == "" {
		e.Message = http.StatusText(status)
	}
	return e
}

func (e *Error) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("pika API: %d %s: %s", e.Status, e.Code, e.Message)
	}
	return fmt.Sprintf("pika API: %d: %s", e.Status, e.Message)
}

// Auth reports a missing, invalid or inactive key. A 403 for an empty balance
// carries a code and is not one.
func (e *Error) Auth() bool {
	return e.Status == http.StatusUnauthorized || (e.Status == http.StatusForbidden && e.Code == "")
}

// RateLimited reports that the API asked to slow down.
func (e *Error) RateLimited() bool { return e.Status == http.StatusTooManyRequests }

// Transient reports whether repeating the request might succeed: a failure
// that never produced an answer, or a status that says to try later.
func Transient(err error) bool {
	var bad *badRequestError
	if errors.As(err, &bad) || errors.Is(err, context.Canceled) {
		return false
	}
	var apiErr *Error
	if errors.As(err, &apiErr) {
		switch apiErr.Status {
		case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return true
		}
		return false
	}
	return true
}

// badRequestError is a request that could not even be built.
type badRequestError struct{ err error }

func (e *badRequestError) Error() string { return e.err.Error() }
func (e *badRequestError) Unwrap() error { return e.err }

func retryWait(attempt int, err error) time.Duration {
	var apiErr *Error
	if errors.As(err, &apiErr) && apiErr.RetryAfter > 0 {
		return min(apiErr.RetryAfter, maxRetryWait)
	}
	return time.Duration(1<<(attempt-1)) * time.Second
}

func parseRetryAfter(value string) time.Duration {
	if seconds, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(value); err == nil {
		return time.Until(when)
	}
	return 0
}
