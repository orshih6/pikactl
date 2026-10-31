package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/orshih6/pikactl/internal/api"
	"github.com/orshih6/pikactl/internal/history"
)

func newAPICommand() *cobra.Command {
	var data string
	var queries, headers []string
	cmd := &cobra.Command{
		Use:   "api <method> <path>",
		Short: "Call any API route directly",
		Long: "Call any API route directly, including ones added after this version. The answer\n" +
			"goes to stdout as it arrives, so a streamed response streams. Anything but GET\n" +
			"asks first and is written to the history. Paths only: the key is never sent to\n" +
			"another host. `pikactl endpoints` lists the routes.",
		Example: `  pikactl api GET /billing/balance
  pikactl api GET /billing/spend/daily -q start_date=2026-10-01 -q end_date=2026-10-31
  pikactl api POST /anthropic/v1/messages -H 'anthropic-version: 2023-06-01' -d @request.json`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			method, path := strings.ToUpper(args[0]), args[1]
			if err := api.CheckPath(path); err != nil {
				return err
			}
			if !strings.HasPrefix(path, "/catalog/") {
				if err := a.cfg.RequireKey(); err != nil {
					return err
				}
			}
			req := api.Request{Method: method, Path: path, Query: url.Values{}, Header: http.Header{}}
			for _, q := range queries {
				name, value, ok := strings.Cut(q, "=")
				if !ok {
					return fmt.Errorf("--query %q: expected key=value", q)
				}
				req.Query.Add(name, value)
			}
			for _, h := range headers {
				name, value, ok := strings.Cut(h, ":")
				if !ok {
					return fmt.Errorf("--header %q: expected name: value", h)
				}
				req.Header.Add(strings.TrimSpace(name), strings.TrimSpace(value))
			}
			if data != "" {
				if req.Body, err = readData(cmd, data); err != nil {
					return err
				}
			}
			ref := ""
			if method != http.MethodGet && method != http.MethodHead {
				if err := confirm(cmd, method+" "+path+" may spend money or change something. Send it?"); err != nil {
					return err
				}
				ref = a.hist.NewRef()
				event := history.Event{Type: history.EventRequest, Ref: ref, Kind: history.KindAPI, Method: method, Path: path}
				if json.Valid(req.Body) {
					event.Body = req.Body
				}
				if err := a.hist.Append(event); err != nil {
					return fmt.Errorf("could not write to the history, so nothing was sent: %w", err)
				}
			}
			resp, err := a.api.Open(cmd.Context(), req)
			if err != nil {
				if ref != "" {
					a.recordAPIResult(ref, nil, err)
				}
				return err
			}
			defer resp.Body.Close()
			var kept bytes.Buffer
			if _, err := io.Copy(io.MultiWriter(a.out, &limitedWriter{w: &kept, left: 1 << 20}), resp.Body); err != nil {
				return err
			}
			var failure error
			if resp.StatusCode >= 400 {
				failure = api.NewError(resp.StatusCode, resp.Header, kept.Bytes())
			}
			if ref != "" {
				a.recordRawResult(ref, resp, kept.Bytes(), failure)
			}
			return failure
		},
	}
	cmd.Flags().StringVarP(&data, "data", "d", "", "request body: literal text, @file, or - for stdin")
	cmd.Flags().StringArrayVarP(&queries, "query", "q", nil, "query parameter, key=value (repeatable)")
	cmd.Flags().StringArrayVarP(&headers, "header", "H", nil, "request header, 'name: value' (repeatable)")
	return cmd
}

// recordRawResult also links a job started this way to the history, so that
// `jobs wait` and `history sync` can follow it.
func (a *app) recordRawResult(ref string, resp *http.Response, body []byte, failure error) {
	event := history.Event{Type: history.EventResponse, Ref: ref, HTTPStatus: resp.StatusCode, Status: "ok"}
	if json.Valid(body) {
		event.Response = body
		var job struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		}
		if json.Unmarshal(body, &job) == nil && strings.HasPrefix(job.ID, "media_") {
			event.JobID, event.Status = job.ID, job.Status
		}
	}
	if apiErr, ok := failure.(*api.Error); ok {
		event.Status, event.ErrorCode, event.ErrorMessage = "error", apiErr.Code, apiErr.Message
	}
	a.record(event)
}

func readData(cmd *cobra.Command, data string) ([]byte, error) {
	switch {
	case data == "-":
		return io.ReadAll(cmd.InOrStdin())
	case strings.HasPrefix(data, "@"):
		return os.ReadFile(data[1:])
	}
	return []byte(data), nil
}

// limitedWriter keeps the start of a stream and silently drops the rest, so a
// long answer still reaches stdout without all of it being held for the history.
type limitedWriter struct {
	w    io.Writer
	left int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.left > 0 {
		n := min(len(p), l.left)
		l.left -= n
		if _, err := l.w.Write(p[:n]); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}
