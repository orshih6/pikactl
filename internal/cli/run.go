package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/orshih6/pikactl/internal/catalog"
)

// runFlags are the options every operation shares. The rest of the flags come
// from the operation's own input schema.
type runFlags struct {
	set        []string
	body       string
	maxUSD     float64
	dryRun     bool
	noWait     bool
	noDownload bool
	timeout    time.Duration
	webhookURL string
	idemKey    string
	note       string
	output     string
}

func (o *runFlags) addRequest(fs *pflag.FlagSet) {
	fs.StringArrayVar(&o.set, "set", nil, "set an input by name, key=value (repeatable)")
	fs.StringVar(&o.body, "body", "", "start from this JSON body: literal JSON, @file.json, or - for stdin")
}

func (o *runFlags) addExecution(fs *pflag.FlagSet) {
	fs.Float64Var(&o.maxUSD, "max-usd", 0, "refuse to run if the quote is above this, or if there is no quote")
	fs.BoolVar(&o.dryRun, "dry-run", false, "show the request and its price, and send nothing")
	fs.BoolVar(&o.noWait, "no-wait", false, "submit and exit; finish later with: pikactl jobs wait <id>")
	fs.BoolVar(&o.noDownload, "no-download", false, "do not save the output files")
	fs.DurationVar(&o.timeout, "timeout", 30*time.Minute, "stop waiting after this long (the job keeps running)")
	fs.StringVar(&o.webhookURL, "webhook-url", "", "also send this job's result to this HTTPS URL")
	fs.StringVar(&o.idemKey, "idempotency-key", "", "reuse this key to safely repeat a submit whose answer was lost")
	fs.StringVar(&o.note, "note", "", "a label to keep with this request in the history")
	fs.StringVarP(&o.output, "output", "o", "", "also copy the output here (a directory if there are several files)")
}

// paramValue collects a schema-defined flag's raw values, coerced once the
// whole command line is known.
type paramValue struct {
	typ  string
	raws []string
}

func (v *paramValue) Set(s string) error { v.raws = append(v.raws, s); return nil }
func (v *paramValue) String() string     { return "" }
func (v *paramValue) Type() string       { return v.typ }

func dashes(_ *pflag.FlagSet, name string) pflag.NormalizedName {
	return pflag.NormalizedName(strings.ReplaceAll(name, "_", "-"))
}

// parsedOp is a command line resolved against an operation's live schema.
type parsedOp struct {
	op     *catalog.Op
	schema *catalog.Schema
	body   map[string]any
	help   bool
}

// parseOperation handles what cobra cannot: flags that depend on which
// operation was named. It finds the ID, reads that operation's schema, turns
// each input into a flag, and only then parses the command line properly.
func parseOperation(cmd *cobra.Command, args []string, o *runFlags, addFlags func(*pflag.FlagSet)) (*app, *parsedOp, error) {
	scout := pflag.NewFlagSet("scout", pflag.ContinueOnError)
	scout.ParseErrorsWhitelist.UnknownFlags = true
	scout.Usage = func() {}
	scout.SetOutput(io.Discard)
	addFlags(scout)
	scout.AddFlagSet(cmd.InheritedFlags())
	wantHelp := scout.BoolP("help", "h", false, "")
	if err := scout.Parse(args); err != nil {
		return nil, nil, err
	}
	a, err := newApp(cmd)
	if err != nil {
		return nil, nil, err
	}
	if scout.NArg() == 0 {
		if *wantHelp {
			return a, &parsedOp{help: true}, cmd.Help()
		}
		return nil, nil, errors.New("name an operation, such as pika/pika-h3-flex/text-to-video (see `pikactl models`)")
	}
	op, err := a.catalog.Get(cmd.Context(), scout.Arg(0))
	if err != nil {
		return nil, nil, err
	}
	if cmd.Name() == "run" && op.Call == nil {
		return nil, nil, noMediaCall(op)
	}
	schema, err := catalog.ParseSchema(op.InputSchema)
	if err != nil {
		return nil, nil, err
	}

	fs := pflag.NewFlagSet(cmd.Name(), pflag.ContinueOnError)
	fs.Usage = func() {}
	fs.SetOutput(io.Discard)
	fs.SetNormalizeFunc(dashes)
	fs.SortFlags = false
	addFlags(fs)
	fs.AddFlagSet(cmd.InheritedFlags())
	help := fs.BoolP("help", "h", false, "show this operation's parameters")

	paramFlags := pflag.NewFlagSet("params", pflag.ContinueOnError)
	paramFlags.SetNormalizeFunc(dashes)
	paramFlags.SortFlags = false
	values := map[string]*paramValue{}
	for _, p := range schema.Params() {
		name := p.Name
		// An input named like one of pikactl's own flags stays reachable.
		if fs.Lookup(name) != nil {
			name = "param-" + name
		}
		value := &paramValue{typ: p.TypeName()}
		values[p.Name] = value
		flag := paramFlags.VarPF(value, name, "", p.Usage())
		if value.typ == "bool" {
			flag.NoOptDefVal = "true"
		}
	}
	fs.AddFlagSet(paramFlags)
	if err := fs.Parse(args); err != nil {
		return nil, nil, fmt.Errorf("%w (see `pikactl %s %s --help`)", err, cmd.Name(), op.ID)
	}
	if fs.NArg() > 1 {
		return nil, nil, fmt.Errorf("unexpected argument %q: inputs are given as flags (see `pikactl %s %s --help`)", fs.Arg(1), cmd.Name(), op.ID)
	}
	if *help {
		common := pflag.NewFlagSet("common", pflag.ContinueOnError)
		common.SortFlags = false
		addFlags(common)
		printOperationHelp(a.out, cmd.Name(), op, paramFlags, common)
		return a, &parsedOp{help: true}, nil
	}

	body, err := startBody(cmd, o.body)
	if err != nil {
		return nil, nil, err
	}
	for _, p := range schema.Params() {
		if value := values[p.Name]; len(value.raws) > 0 {
			if err := schema.Apply(body, p.Name, value.raws); err != nil {
				return nil, nil, err
			}
		}
	}
	if err := applySets(schema, body, o.set); err != nil {
		return nil, nil, err
	}
	if missing := schema.Missing(body); len(missing) > 0 {
		return nil, nil, fmt.Errorf("missing required input: %s (see `pikactl %s %s --help`)", strings.Join(missing, ", "), cmd.Name(), op.ID)
	}
	return a, &parsedOp{op: op, schema: schema, body: body}, nil
}

func applySets(schema *catalog.Schema, body map[string]any, sets []string) error {
	for _, set := range sets {
		name, value, ok := strings.Cut(set, "=")
		if !ok {
			return fmt.Errorf("--set %q: expected key=value", set)
		}
		if err := schema.Apply(body, name, []string{value}); err != nil {
			return err
		}
	}
	return nil
}

// startBody reads --body, which the other flags then add to.
func startBody(cmd *cobra.Command, source string) (map[string]any, error) {
	body := map[string]any{}
	if source == "" {
		return body, nil
	}
	data := []byte(source)
	var err error
	switch {
	case source == "-":
		data, err = io.ReadAll(cmd.InOrStdin())
	case strings.HasPrefix(source, "@"):
		data, err = os.ReadFile(source[1:])
	}
	if err != nil {
		return nil, fmt.Errorf("reading --body: %w", err)
	}
	decoded, err := catalog.DecodeJSON(data)
	if err != nil {
		return nil, fmt.Errorf("--body: %w", err)
	}
	object, ok := decoded.(map[string]any)
	if !ok {
		return nil, errors.New("--body must be a JSON object")
	}
	return object, nil
}

func printOperationHelp(w io.Writer, command string, op *catalog.Op, params, common *pflag.FlagSet) {
	fmt.Fprintf(w, "%s (%s)\n", op.Name, op.ID)
	if op.Description != "" {
		fmt.Fprintf(w, "\n%s\n", op.Description)
	}
	if lines := op.PriceLines(); len(lines) > 0 {
		fmt.Fprintf(w, "\nPrices:\n  %s\n", strings.Join(lines, "\n  "))
	}
	fmt.Fprintf(w, "\nUsage:\n  pikactl %s %s [flags]\n", command, op.ID)
	if params.HasFlags() {
		fmt.Fprintf(w, "\nParameters (from Pika's live catalog):\n%s", params.FlagUsagesWrapped(100))
	}
	fmt.Fprintf(w, "\nOptions:\n%s", common.FlagUsagesWrapped(100))
	fmt.Fprintf(w, "\nFull specification: pikactl models docs %s\n", op.ID)
}

func newRunCommand() *cobra.Command {
	o := &runFlags{}
	addFlags := func(fs *pflag.FlagSet) {
		o.addRequest(fs)
		o.addExecution(fs)
	}
	cmd := &cobra.Command{
		Use:   "run <id> [--input value ...]",
		Short: "Run any operation in the catalog",
		Long: "Run any operation in the catalog: quote it, ask, submit it, wait, save the\n" +
			"files and write it all to the history.\n\n" +
			"The inputs are flags made from the operation's live schema, so\n" +
			"`pikactl run <id> --help` lists them for that operation. A local file given\n" +
			"to a media input is uploaded first. Put <id> right after `run`.",
		Example: `  pikactl run pika/pika-h3-flex/text-to-video --prompt "a paper boat on a puddle" --duration 5
  pikactl run pika/pika-h3-flex/text-to-video --help
  pikactl run pika/pika-h3-flex/image-to-video --image ./photo.png --prompt "she turns and smiles"
  pikactl run pika/video-compose/compose-video --tracks @timeline.json --max-usd 0.50 --yes`,
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, parsed, err := parseOperation(cmd, args, o, addFlags)
			if err != nil || parsed.help {
				return err
			}
			return a.execute(cmd.Context(), execution{op: parsed.op, schema: parsed.schema, body: parsed.body, flags: o})
		},
	}
	addFlags(cmd.Flags())
	return cmd
}

func noMediaCall(op *catalog.Op) error {
	if op.Category == "decision" {
		return fmt.Errorf("%s answers typed questions: use `pikactl decide %s`", op.ID, op.ID)
	}
	return fmt.Errorf("%s is not a job: use `pikactl chat %s`, or see `pikactl models docs %s`", op.ID, op.ID, op.ID)
}

func newQuoteCommand() *cobra.Command {
	o := &runFlags{}
	cmd := &cobra.Command{
		Use:   "quote <id> [--input value ...]",
		Short: "Price a request without running it (free, and needs no key)",
		Long: "Price a request without running it. It is free and needs no key, and the API\n" +
			"checks the inputs on the way, so it also finds a bad request for nothing.\n" +
			"A local file given to a media input is uploaded for the quote, which needs a\n" +
			"key, and removed again.",
		Example:            `  pikactl quote pika/pika-h3-flex/text-to-video --prompt test --duration 10 --resolution 1080p`,
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, parsed, err := parseOperation(cmd, args, o, o.addRequest)
			if err != nil || parsed.help {
				return err
			}
			uploads, err := a.uploadLocalFiles(cmd.Context(), parsed.schema, parsed.body)
			if err != nil {
				return err
			}
			defer a.discardUploads(uploads)
			raw, err := json.Marshal(parsed.body)
			if err != nil {
				return err
			}
			quote, err := a.catalog.GetQuote(cmd.Context(), parsed.op.ID, raw)
			if errors.Is(err, catalog.ErrNoQuote) {
				return fmt.Errorf("%s cannot be priced in advance; its rates are in `pikactl models show %s`", parsed.op.ID, parsed.op.ID)
			}
			if err != nil {
				return err
			}
			return emit(a.out, quote, func() error {
				fmt.Fprintln(a.out, formatUSD(quote))
				return nil
			})
		},
	}
	o.addRequest(cmd.Flags())
	return cmd
}
