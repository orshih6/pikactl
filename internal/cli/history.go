package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/orshih6/pikactl/internal/catalog"
	"github.com/orshih6/pikactl/internal/history"
	"github.com/orshih6/pikactl/internal/output"
)

func newHistoryCommand() *cobra.Command {
	var limit int
	var op, status, since, note string
	cmd := &cobra.Command{
		Use:   "history",
		Short: "List everything requested so far, with prompts, costs and files",
		Long: "List everything requested so far, newest first. Pika has no endpoint listing\n" +
			"past jobs, so this local record is the only list. It is an append-only JSON\n" +
			"Lines file that nothing rewrites; deleting a job on Pika's side keeps its entry.",
		Example: `  pikactl history
  pikactl history --op h3-flex --status completed --since 7d
  pikactl history --note "paper boat ad" --limit 0     # one project's requests and its total spend
  pikactl history show 20261031-151023-a1b2c3
  pikactl history rerun 20261031-151023-a1b2c3 --set seed=7`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			records, skipped, err := a.hist.Load()
			if err != nil {
				return err
			}
			if skipped > 0 {
				a.logf("warning: skipped %d damaged line(s) in %s", skipped, a.hist.Path)
			}
			cutoff, err := parseSince(since)
			if err != nil {
				return err
			}
			var matched []*history.Record
			for i := len(records) - 1; i >= 0; i-- {
				r := records[i]
				if op != "" && !strings.Contains(r.APIID, op) && !strings.Contains(r.Path, op) {
					continue
				}
				if status != "" && r.Status != status {
					continue
				}
				if note != "" && !strings.Contains(strings.ToLower(r.Note), strings.ToLower(note)) {
					continue
				}
				if !cutoff.IsZero() && r.CreatedAt.Before(cutoff) {
					continue
				}
				matched = append(matched, r)
			}
			var spent int64
			for _, r := range matched {
				if r.ChargeMicroUSD != nil {
					spent += *r.ChargeMicroUSD
				}
			}
			shown := matched
			if limit > 0 && len(shown) > limit {
				shown = shown[:limit]
			}
			return emit(a.out, shown, func() error {
				if len(matched) == 0 {
					fmt.Fprintln(a.out, "Nothing in the history yet.")
					return nil
				}
				rows := make([][]string, 0, len(shown))
				for _, r := range shown {
					rows = append(rows, []string{
						r.Ref,
						r.CreatedAt.Local().Format("2006-01-02 15:04"),
						operationLabel(r),
						r.Status,
						costLabel(r),
						output.Truncate(describe(r), 44),
					})
				}
				if err := output.Table(a.out, []string{"REF", "WHEN", "OPERATION", "STATUS", "COST", "WHAT"}, rows); err != nil {
					return err
				}
				a.logf("%d of %d shown; %s charged across all %d (`pikactl billing spend` has Pika's own figures)", len(shown), len(matched), output.USD(spent), len(matched))
				return nil
			})
		},
	}
	cmd.Flags().IntVarP(&limit, "limit", "n", 20, "show at most this many; 0 for all")
	cmd.Flags().StringVar(&op, "op", "", "only operations containing this text")
	cmd.Flags().StringVar(&status, "status", "", "only this status, such as completed or failed")
	cmd.Flags().StringVar(&since, "since", "", "only since this long ago (7d, 24h) or this date (2026-10-31)")
	cmd.Flags().StringVar(&note, "note", "", "only requests whose --note contains this text (any case), e.g. a project's name")
	cmd.AddCommand(newHistoryShowCommand(), newHistoryRerunCommand(), newHistorySyncCommand())
	return cmd
}

func operationLabel(r *history.Record) string {
	if r.APIID != "" {
		return r.APIID
	}
	return strings.TrimSpace(r.Method + " " + r.Path)
}

func costLabel(r *history.Record) string {
	switch {
	case r.ChargeMicroUSD != nil:
		return output.USD(*r.ChargeMicroUSD)
	case r.QuoteMicroUSD != nil && !r.Terminal():
		return "~" + output.USD(*r.QuoteMicroUSD)
	}
	return "-"
}

// describe picks the most recognisable text in a request: its note, or the
// input a person wrote.
func describe(r *history.Record) string {
	if r.Note != "" {
		return r.Note
	}
	var body map[string]any
	if json.Unmarshal(r.Body, &body) != nil {
		return ""
	}
	for _, key := range []string{"prompt", "text", "name"} {
		if text, ok := body[key].(string); ok {
			return text
		}
	}
	if messages, ok := body["messages"].([]any); ok && len(messages) > 0 {
		if last, ok := messages[len(messages)-1].(map[string]any); ok {
			if text, ok := last["content"].(string); ok {
				return text
			}
		}
	}
	return ""
}

func parseSince(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	if days, ok := strings.CutSuffix(value, "d"); ok {
		if n, err := strconv.Atoi(days); err == nil {
			return time.Now().AddDate(0, 0, -n), nil
		}
	}
	if d, err := time.ParseDuration(value); err == nil {
		return time.Now().Add(-d), nil
	}
	if t, err := time.ParseInLocation("2006-01-02", value, time.Local); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("--since %q: expected something like 7d, 24h or 2026-10-31", value)
}

func newHistoryShowCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "show <id>",
		Short: "Show one request in full: its body, cost, status and files",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			r, err := a.hist.Find(args[0])
			if err != nil {
				return err
			}
			return emit(a.out, r, func() error {
				quote, charge, failure := "", "", ""
				if r.QuoteMicroUSD != nil {
					quote = output.USD(*r.QuoteMicroUSD)
				}
				if r.ChargeMicroUSD != nil {
					charge = output.USD(*r.ChargeMicroUSD)
				}
				if r.ErrorCode != "" || r.ErrorMessage != "" {
					failure = strings.TrimSpace(r.ErrorCode + " " + r.ErrorMessage)
				}
				if err := output.Fields(a.out, []output.Field{
					{Name: "ref", Value: r.Ref},
					{Name: "job", Value: r.JobID},
					{Name: "operation", Value: operationLabel(r)},
					{Name: "status", Value: r.Status},
					{Name: "error", Value: failure},
					{Name: "created", Value: r.CreatedAt.Local().Format(time.RFC3339)},
					{Name: "note", Value: r.Note},
					{Name: "quoted", Value: quote},
					{Name: "charged", Value: charge},
					{Name: "rerun of", Value: r.RerunOf},
					{Name: "idempotency key", Value: r.IdempotencyKey},
				}); err != nil {
					return err
				}
				if len(r.Body) > 0 {
					fmt.Fprintln(a.out, "request:")
					if err := printRawJSON(a.out, r.Body); err != nil {
						return err
					}
				}
				if len(r.Files) > 0 {
					fmt.Fprintln(a.out, "files:")
					for _, f := range r.Files {
						fmt.Fprintf(a.out, "  %s (%d bytes)\n", f.Path, f.Bytes)
					}
				}
				if r.Deleted {
					fmt.Fprintln(a.out, "erased on Pika's side")
				}
				return nil
			})
		},
	}
}

func newHistoryRerunCommand() *cobra.Command {
	o := &runFlags{}
	cmd := &cobra.Command{
		Use:   "rerun <id>",
		Short: "Run a past media request again, as a new paid job",
		Long: "Run a past media request again, as a new paid job with its own history entry.\n" +
			"It is quoted and asks first like `run`. --set changes inputs, such as the seed.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			r, err := a.hist.Find(args[0])
			if err != nil {
				return err
			}
			if r.Kind != history.KindMedia || r.APIID == "" || len(r.Body) == 0 {
				return fmt.Errorf("%s has no media request to run again", r.Ref)
			}
			op, err := a.catalog.Get(cmd.Context(), r.APIID)
			if err != nil {
				return fmt.Errorf("%s can no longer run: %w", r.Ref, err)
			}
			if op.Call == nil {
				return noMediaCall(op)
			}
			schema, err := catalog.ParseSchema(op.InputSchema)
			if err != nil {
				return err
			}
			decoded, err := catalog.DecodeJSON(r.Body)
			if err != nil {
				return err
			}
			body, ok := decoded.(map[string]any)
			if !ok {
				return errors.New("the saved request is not a JSON object")
			}
			if hook, ok := body["webhook_url"].(string); ok {
				if o.webhookURL == "" {
					o.webhookURL = hook
				}
				delete(body, "webhook_url")
			}
			if err := applySets(schema, body, o.set); err != nil {
				return err
			}
			if o.note == "" {
				o.note = r.Note
			}
			return a.execute(cmd.Context(), execution{op: op, schema: schema, body: body, flags: o, rerunOf: r.Ref})
		},
	}
	cmd.Flags().StringArrayVar(&o.set, "set", nil, "change an input, key=value (repeatable)")
	o.addExecution(cmd.Flags())
	return cmd
}

func newHistorySyncCommand() *cobra.Command {
	var noDownload bool
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Catch up on jobs left unfinished: status, charges and files",
		Long: "Catch up on jobs left unfinished, such as those started with --no-wait or\n" +
			"interrupted: record their status and charge, and save their files.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			if err := a.cfg.RequireKey(); err != nil {
				return err
			}
			records, _, err := a.hist.Load()
			if err != nil {
				return err
			}
			updated, failed, lost := 0, 0, 0
			for _, r := range records {
				if r.Kind != history.KindMedia || r.Deleted {
					continue
				}
				if r.JobID == "" {
					if r.Status == history.StatusUnknown {
						lost++
						a.logf("%s  never got an answer; it may have run. Repeat it safely with: pikactl history rerun %s --idempotency-key %s", r.Ref, r.Ref, r.IdempotencyKey)
					}
					continue
				}
				changed, err := a.syncRecord(cmd.Context(), r, noDownload)
				if err != nil {
					failed++
					a.logf("%s  %v", r.Ref, err)
					continue
				}
				if changed {
					updated++
				}
			}
			fmt.Fprintf(a.out, "Checked %d unfinished, %d could not be read, %d never got an answer.\n", updated, failed, lost)
			return nil
		},
	}
	cmd.Flags().BoolVar(&noDownload, "no-download", false, "do not save output files")
	return cmd
}
