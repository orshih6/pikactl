package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/orshih6/pikactl/internal/api"
	"github.com/orshih6/pikactl/internal/history"
	"github.com/orshih6/pikactl/internal/output"
)

// endpoint is a command that maps straight onto one API route. Supporting a new
// route takes one more entry below, and `pikactl api` reaches it before that.
type endpoint struct {
	use    string
	short  string
	method string
	path   string   // each {name} becomes a positional argument
	query  []string // each becomes a --flag
	need   []string // the query flags that are required
	ask    string   // asked first, for something that cannot be undone
	raw    bool     // print the answer as it came, not as JSON
}

var pathParam = regexp.MustCompile(`\{([a-z_]+)\}`)

func (e endpoint) command() *cobra.Command {
	names := pathParam.FindAllStringSubmatch(e.path, -1)
	use := e.use
	for _, n := range names {
		use += " <" + n[1] + ">"
	}
	values := map[string]*string{}
	cmd := &cobra.Command{
		Use:   use,
		Short: e.short,
		Args:  cobra.ExactArgs(len(names)),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			path := e.path
			for i, n := range names {
				path = strings.Replace(path, n[0], url.PathEscape(args[i]), 1)
			}
			query := url.Values{}
			for name, value := range values {
				if *value != "" {
					query.Set(name, *value)
				}
			}
			return a.callEndpoint(cmd, e, path, query)
		},
	}
	for _, name := range e.query {
		value := new(string)
		values[name] = value
		flag := strings.ReplaceAll(name, "_", "-")
		cmd.Flags().StringVar(value, flag, "", name+" query parameter")
	}
	for _, name := range e.need {
		_ = cmd.MarkFlagRequired(strings.ReplaceAll(name, "_", "-"))
	}
	return cmd
}

func (a *app) callEndpoint(cmd *cobra.Command, e endpoint, path string, query url.Values) error {
	if e.ask != "" {
		if err := confirm(cmd, e.ask); err != nil {
			return err
		}
	}
	if !strings.HasPrefix(path, "/catalog/") {
		if err := a.cfg.RequireKey(); err != nil {
			return err
		}
	}
	ref := ""
	if e.method != http.MethodGet {
		ref = a.hist.NewRef()
		a.record(history.Event{Type: history.EventRequest, Ref: ref, Kind: history.KindAPI, Method: e.method, Path: path})
	}
	resp, err := a.api.Do(cmd.Context(), api.Request{Method: e.method, Path: path, Query: query})
	if ref != "" {
		a.recordAPIResult(ref, resp, err)
	}
	if err != nil {
		return err
	}
	if e.raw {
		_, err := a.out.Write(resp.Body)
		return err
	}
	if len(resp.Body) == 0 {
		fmt.Fprintln(a.out, "Done.")
		return nil
	}
	return printRawJSON(a.out, resp.Body)
}

func (a *app) recordAPIResult(ref string, resp *api.Response, err error) {
	event := history.Event{Type: history.EventResponse, Ref: ref, Status: "ok"}
	if err != nil {
		event.Status, event.ErrorMessage = history.StatusUnknown, err.Error()
		if apiErr, ok := err.(*api.Error); ok {
			event.Status, event.HTTPStatus, event.ErrorCode, event.ErrorMessage = "error", apiErr.Status, apiErr.Code, apiErr.Message
		}
	} else {
		event.HTTPStatus = resp.Status
		if json.Valid(resp.Body) {
			event.Response = resp.Body
		}
	}
	a.record(event)
}

// printRawJSON indents the API's JSON without decoding it, so nothing in it is
// lost or reordered. Anything else is printed as it came.
func printRawJSON(w io.Writer, raw []byte) error {
	var buf bytes.Buffer
	if json.Indent(&buf, raw, "", "  ") != nil {
		_, err := w.Write(raw)
		return err
	}
	buf.WriteByte('\n')
	_, err := w.Write(buf.Bytes())
	return err
}

func newBalanceCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "balance",
		Short: "Show what the organisation can spend (free)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			if err := a.cfg.RequireKey(); err != nil {
				return err
			}
			if flagJSON {
				return a.callEndpoint(cmd, endpoint{method: http.MethodGet}, "/billing/balance", nil)
			}
			balance, err := a.readBalance(cmd.Context())
			if err != nil {
				return err
			}
			return output.Fields(a.out, []output.Field{
				{Name: "available", Value: output.USD(balance.Available())},
				{Name: "prepaid", Value: output.USD(balance.PrepaidMicroUSD)},
				{Name: "credits", Value: output.USD(balance.CreditMicroUSD)},
			})
		},
	}
}

func newBillingCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "billing", Short: "Read the balance, spend and monthly reports (free)"}
	cmd.AddCommand(
		newBalanceCommand(),
		endpoint{
			use: "spend", short: "Daily spend by key and model; both dates are inclusive",
			method: http.MethodGet, path: "/billing/spend/daily",
			query: []string{"start_date", "end_date", "api_key_id", "model"}, need: []string{"start_date", "end_date"},
		}.command(),
		endpoint{use: "monthly", short: "One month's report, as YYYY-MM", method: http.MethodGet, path: "/billing/monthly/{month}"}.command(),
		endpoint{use: "monthly-csv", short: "One month's report as CSV", method: http.MethodGet, path: "/billing/monthly/{month}/csv", raw: true}.command(),
	)
	return cmd
}

func newWebhooksCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "webhooks",
		Short: "Inspect and replay webhook deliveries",
		Long: "Inspect and replay webhook deliveries. Endpoints and signing secrets are managed\n" +
			"at https://dev.pika.art/webhooks. To be told about one job, use\n" +
			"`pikactl run --webhook-url`.",
	}
	cmd.AddCommand(
		endpoint{
			use: "deliveries", short: "List recent deliveries",
			method: http.MethodGet, path: "/v1/webhooks/deliveries",
			query: []string{"endpoint_id", "status", "source_id", "limit", "cursor"},
		}.command(),
		endpoint{use: "delivery", short: "Show one delivery", method: http.MethodGet, path: "/v1/webhooks/deliveries/{delivery_id}"}.command(),
		endpoint{use: "attempts", short: "List a delivery's attempts", method: http.MethodGet, path: "/v1/webhooks/deliveries/{delivery_id}/attempts"}.command(),
		endpoint{use: "replay", short: "Send a delivery again", method: http.MethodPost, path: "/v1/webhooks/deliveries/{delivery_id}/replay"}.command(),
	)
	return cmd
}

func newEndpointsCommand() *cobra.Command {
	var refresh bool
	cmd := &cobra.Command{
		Use:   "endpoints [filter]",
		Short: "List every route in Pika's live OpenAPI document",
		Long: "List every route in Pika's live OpenAPI document, for use with `pikactl api`.\n" +
			"This is how a new route can be found and called before pikactl has a command\n" +
			"for it. Language model routes are documented per model: see\n" +
			"`pikactl models docs <id>`.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			all, err := a.catalog.OpenAPI(cmd.Context(), refresh)
			if err != nil {
				return err
			}
			rows := [][]string{}
			for _, e := range all {
				if len(args) == 0 || strings.Contains(strings.ToLower(e.Path+" "+e.Summary), strings.ToLower(args[0])) {
					rows = append(rows, []string{e.Method, e.Path, e.Summary})
				}
			}
			return emit(a.out, rows, func() error {
				return output.Table(a.out, []string{"METHOD", "PATH", "SUMMARY"}, rows)
			})
		},
	}
	cmd.Flags().BoolVar(&refresh, "refresh", false, "fetch the document instead of using the saved copy")
	return cmd
}
