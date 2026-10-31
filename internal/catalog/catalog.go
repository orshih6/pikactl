// Package catalog reads Pika's live description of its operations. Nothing in
// pikactl names a model: paths, parameters and prices all come from here, so
// an operation Pika adds or changes works without a new pikactl.
package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/orshih6/pikactl/internal/api"
	"github.com/orshih6/pikactl/internal/output"
)

// DefaultTTL is how long the catalog list and documentation are reused. The
// API rate-limits, and listing is what a person does repeatedly.
const DefaultTTL = time.Hour

// Call is how an operation is invoked. Language and decision models have none:
// their protocol is named in their documentation (see Endpoint).
type Call struct {
	Method string `json:"method"`
	Path   string `json:"path"`
}

// Op is one catalog operation.
type Op struct {
	ID          string          `json:"api_id"`
	Vendor      string          `json:"vendor"`
	Function    string          `json:"function"`
	Category    string          `json:"category"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Call        *Call           `json:"call"`
	Pricing     json.RawMessage `json:"display_pricing"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// Quote is a free price estimate for one request.
type Quote struct {
	SellUSD  string `json:"sell_usd"`
	MicroUSD int64  `json:"micro_usd"`
}

// ErrNoQuote means the operation exists but cannot be priced in advance, as
// with language models, which bill by the tokens they end up using.
var ErrNoQuote = errors.New("this operation has no quote")

// Store reads the catalog through a cache on disk.
type Store struct {
	API      *api.Client
	DocsURL  string
	CacheDir string
	TTL      time.Duration
	// Warn is told when stale data is used because the API could not answer.
	Warn func(string)
}

var validID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*(/[a-zA-Z0-9._-]+)*$`)

func checkID(id string) error {
	if !validID.MatchString(id) || strings.Contains(id, "..") {
		return fmt.Errorf("%q is not an operation ID (see `pikactl models`)", id)
	}
	return nil
}

func (s *Store) warn(format string, args ...any) {
	if s.Warn != nil {
		s.Warn(fmt.Sprintf(format, args...))
	}
}

func (s *Store) cachePath(name string) string {
	return filepath.Join(s.CacheDir, strings.ReplaceAll(name, "/", "__"))
}

func (s *Store) readCache(name string, maxAge time.Duration) ([]byte, bool) {
	path := s.cachePath(name)
	info, err := os.Stat(path)
	if err != nil || (maxAge > 0 && time.Since(info.ModTime()) > maxAge) {
		return nil, false
	}
	data, err := os.ReadFile(path)
	return data, err == nil
}

func (s *Store) writeCache(name string, data []byte) {
	if err := os.MkdirAll(s.CacheDir, 0o700); err != nil {
		return
	}
	path := s.cachePath(name)
	tmp := path + ".tmp"
	if os.WriteFile(tmp, data, 0o600) == nil {
		_ = os.Rename(tmp, path)
	}
}

func (s *Store) ttl() time.Duration {
	if s.TTL > 0 {
		return s.TTL
	}
	return DefaultTTL
}

// List returns every operation, from the cache while it is fresh.
func (s *Store) List(ctx context.Context, refresh bool) ([]Op, error) {
	const name = "catalog.json"
	if !refresh {
		if data, ok := s.readCache(name, s.ttl()); ok {
			if ops, err := parseList(data); err == nil {
				return ops, nil
			}
		}
	}
	resp, err := s.API.Do(ctx, api.Request{Method: http.MethodGet, Path: "/catalog/apis"})
	if err != nil {
		if data, ok := s.readCache(name, 0); ok {
			if ops, perr := parseList(data); perr == nil {
				s.warn("using the saved catalog, the API could not answer: %v", err)
				return ops, nil
			}
		}
		return nil, err
	}
	ops, err := parseList(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading the catalog: %w", err)
	}
	s.writeCache(name, resp.Body)
	return ops, nil
}

func parseList(data []byte) ([]Op, error) {
	var list struct {
		APIs []Op `json:"apis"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	return list.APIs, nil
}

// Get returns one operation with its input schema. It always asks the API,
// because the catalog is the source of truth for what a request may contain,
// and only falls back to the saved copy when the API cannot answer.
func (s *Store) Get(ctx context.Context, id string) (*Op, error) {
	if err := checkID(id); err != nil {
		return nil, err
	}
	name := "op__" + id + ".json"
	resp, err := s.API.Do(ctx, api.Request{
		Method: http.MethodGet,
		Path:   "/catalog/apis/" + id + "?expand=inputs",
	})
	if err != nil {
		var apiErr *api.Error
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			return nil, s.unknownOp(ctx, id)
		}
		if data, ok := s.readCache(name, 0); ok {
			var op Op
			if json.Unmarshal(data, &op) == nil {
				s.warn("using the saved description of %s, the API could not answer: %v", id, err)
				return &op, nil
			}
		}
		return nil, err
	}
	var op Op
	if err := json.Unmarshal(resp.Body, &op); err != nil {
		return nil, fmt.Errorf("reading the description of %s: %w", id, err)
	}
	s.writeCache(name, resp.Body)
	return &op, nil
}

func (s *Store) unknownOp(ctx context.Context, id string) error {
	msg := fmt.Sprintf("%q is not in the catalog", id)
	if ops, err := s.List(ctx, false); err == nil {
		if near := Similar(ops, id, 5); len(near) > 0 {
			msg += "; did you mean:\n  " + strings.Join(near, "\n  ")
		}
	}
	return errors.New(msg)
}

// Similar returns IDs sharing the most path segments or words with id.
func Similar(ops []Op, id string, limit int) []string {
	words := strings.FieldsFunc(strings.ToLower(id), func(r rune) bool { return r == '/' || r == '-' || r == '_' })
	type hit struct {
		id    string
		score int
	}
	var hits []hit
	for _, op := range ops {
		score := 0
		lower := strings.ToLower(op.ID)
		for _, w := range words {
			if strings.Contains(lower, w) {
				score++
			}
		}
		if score > 0 {
			hits = append(hits, hit{op.ID, score})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	var out []string
	for i := 0; i < len(hits) && i < limit; i++ {
		out = append(out, hits[i].id)
	}
	return out
}

// GetQuote prices a request. It is free, needs no key, and validates the body
// against the model's schema, so it doubles as a dry run of the request.
func (s *Store) GetQuote(ctx context.Context, id string, body []byte) (*Quote, error) {
	if err := checkID(id); err != nil {
		return nil, err
	}
	resp, err := s.API.Do(ctx, api.Request{
		Method: http.MethodPost,
		Path:   "/catalog/apis/" + id + "/quote",
		Body:   body,
		Retry:  true,
	})
	if err != nil {
		var apiErr *api.Error
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			return nil, ErrNoQuote
		}
		return nil, err
	}
	var q Quote
	if err := json.Unmarshal(resp.Body, &q); err != nil {
		return nil, fmt.Errorf("reading the quote: %w", err)
	}
	return &q, nil
}

// Docs returns an operation's specification as Markdown.
func (s *Store) Docs(ctx context.Context, id string) (string, error) {
	if err := checkID(id); err != nil {
		return "", err
	}
	data, err := s.public(ctx, "docs__"+id+".md", "/llms/"+id)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (s *Store) public(ctx context.Context, cacheName, path string) ([]byte, error) {
	if data, ok := s.readCache(cacheName, s.ttl()); ok {
		return data, nil
	}
	data, err := s.API.GetPublic(ctx, s.DocsURL+path)
	if err != nil {
		if stale, ok := s.readCache(cacheName, 0); ok {
			s.warn("using the saved copy of %s, it could not be fetched: %v", path, err)
			return stale, nil
		}
		return nil, err
	}
	s.writeCache(cacheName, data)
	return data, nil
}

// Endpoint is where a synchronous model is called, as its documentation says.
type Endpoint struct {
	Path    string
	Headers map[string]string
}

var (
	endpointLine = regexp.MustCompile("POST https?://[^/\\s]+(/[^\\s`]+)")
	headerLine   = regexp.MustCompile("^- `([A-Za-z0-9-]+): ([^`]+)`")
)

// Endpoint finds the path and required headers of an operation the catalog
// gives no call for. The documentation is the only place that names them.
func (s *Store) Endpoint(ctx context.Context, id string) (*Endpoint, error) {
	spec, err := s.Docs(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("reading the documentation of %s: %w", id, err)
	}
	return ParseEndpoint(id, spec)
}

// ParseEndpoint reads an Endpoint out of a specification.
func ParseEndpoint(id, spec string) (*Endpoint, error) {
	match := endpointLine.FindStringSubmatch(spec)
	if match == nil {
		return nil, fmt.Errorf("the documentation of %s names no endpoint; see `pikactl models docs %s`", id, id)
	}
	ep := &Endpoint{Path: match[1], Headers: map[string]string{}}
	inHeaders := false
	for _, line := range strings.Split(spec, "\n") {
		switch {
		case strings.HasPrefix(line, "Required headers:"):
			inHeaders = true
		case strings.HasPrefix(line, "## "):
			inHeaders = false
		case inHeaders:
			if h := headerLine.FindStringSubmatch(line); h != nil {
				ep.Headers[h[1]] = h[2]
			}
		}
	}
	return ep, nil
}

// APIEndpoint is one method and path of the OpenAPI document.
type APIEndpoint struct {
	Method  string `json:"method"`
	Path    string `json:"path"`
	Summary string `json:"summary,omitempty"`
}

// OpenAPI lists every endpoint Pika currently declares.
func (s *Store) OpenAPI(ctx context.Context, refresh bool) ([]APIEndpoint, error) {
	const name = "openapi.json"
	if refresh {
		_ = os.Remove(s.cachePath(name))
	}
	data, err := s.public(ctx, name, "/openapi.json")
	if err != nil {
		return nil, err
	}
	var doc struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("reading the OpenAPI document: %w", err)
	}
	var out []APIEndpoint
	for path, methods := range doc.Paths {
		for method, raw := range methods {
			switch method {
			case "get", "post", "put", "patch", "delete":
				var op struct {
					Summary string `json:"summary"`
				}
				_ = json.Unmarshal(raw, &op)
				out = append(out, APIEndpoint{Method: strings.ToUpper(method), Path: path, Summary: op.Summary})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Method < out[j].Method
	})
	return out, nil
}

type pricing struct {
	Components []struct {
		Name string `json:"name"`
		Unit struct {
			Type     string `json:"type"`
			Quantity int64  `json:"quantity"`
		} `json:"unit"`
		StartingAt *tier  `json:"starting_at"`
		PriceTiers []tier `json:"price_tiers"`
	} `json:"components"`
}

type tier struct {
	Spec     map[string]any `json:"spec"`
	MicroUSD int64          `json:"micro_usd"`
}

func unitLabel(kind string, quantity int64) string {
	label := strings.ReplaceAll(kind, "_", " ")
	switch {
	case quantity >= 1_000_000:
		return fmt.Sprintf("%dM %ss", quantity/1_000_000, label)
	case quantity >= 1_000:
		return fmt.Sprintf("%dk %ss", quantity/1_000, label)
	}
	return label
}

// PriceFrom is a one-line starting price, such as "$0.01/second".
func (o *Op) PriceFrom() string {
	var p pricing
	if json.Unmarshal(o.Pricing, &p) != nil || len(p.Components) == 0 {
		return ""
	}
	c := p.Components[0]
	if c.StartingAt == nil {
		return ""
	}
	return output.USD(c.StartingAt.MicroUSD) + "/" + unitLabel(c.Unit.Type, c.Unit.Quantity)
}

// PriceLines lists every price tier, one per line.
func (o *Op) PriceLines() []string {
	var p pricing
	if json.Unmarshal(o.Pricing, &p) != nil {
		return nil
	}
	var lines []string
	for _, c := range p.Components {
		for _, t := range c.PriceTiers {
			line := output.USD(t.MicroUSD) + "/" + unitLabel(c.Unit.Type, c.Unit.Quantity)
			if spec := specLabel(t.Spec); spec != "" {
				line += "  " + spec
			}
			lines = append(lines, line)
		}
	}
	return lines
}

func specLabel(spec map[string]any) string {
	keys := make([]string, 0, len(spec))
	for k := range spec {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", k, spec[k]))
	}
	return strings.Join(parts, " ")
}
