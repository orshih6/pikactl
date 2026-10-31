// Package cli implements pikactl's command-line interface.
package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/orshih6/pikactl/internal/api"
	"github.com/orshih6/pikactl/internal/catalog"
	"github.com/orshih6/pikactl/internal/config"
	"github.com/orshih6/pikactl/internal/history"
	"github.com/orshih6/pikactl/internal/media"
	"github.com/orshih6/pikactl/internal/output"
)

// Exit statuses scripts can rely on.
const (
	exitAuth      = 3 // the key is missing, invalid or inactive
	exitAPI       = 4 // the API refused the request, or the job failed
	exitThrottled = 5 // the API is rate-limiting; try again later
	// interruptExitCode is the conventional shell status for a command
	// killed by SIGINT (128 + 2).
	interruptExitCode = 130
)

var (
	flagConfigPath string
	flagJSON       bool
	flagYes        bool
)

// version is stamped into release builds with -ldflags.
var version string

func buildVersion() string {
	if version != "" {
		return version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	if v := info.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" {
			if len(setting.Value) > 12 {
				return setting.Value[:12]
			}
			return setting.Value
		}
	}
	return "unknown"
}

func defaultConfigPath() string {
	if p := os.Getenv("PIKACTL_CONFIG"); p != "" {
		return p
	}
	dir := config.DefaultDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "config.yaml")
}

// app is what a command works with. It is built when the command runs rather
// than before, because `run` parses its own flags, --config among them.
type app struct {
	cmd     *cobra.Command
	cfg     config.Config
	api     *api.Client
	media   *media.Service
	catalog *catalog.Store
	hist    *history.Store
	out     io.Writer
	errOut  io.Writer
}

// configureClient lets tests skip the waits between polls and retries.
var configureClient = func(*api.Client) {}

func newApp(cmd *cobra.Command) (*app, error) {
	cfg, err := config.Resolve(flagConfigPath)
	if err != nil {
		return nil, err
	}
	client := api.New(cfg.BaseURL, cfg.Key, "pikactl/"+buildVersion())
	configureClient(client)
	a := &app{
		cmd:    cmd,
		cfg:    cfg,
		api:    client,
		media:  &media.Service{API: client},
		hist:   history.Open(cfg.DataDir),
		out:    cmd.OutOrStdout(),
		errOut: cmd.ErrOrStderr(),
	}
	a.catalog = &catalog.Store{
		API:      client,
		DocsURL:  cfg.DocsURL,
		CacheDir: filepath.Join(cfg.DataDir, "cache"),
		Warn:     func(msg string) { fmt.Fprintln(a.errOut, "warning:", msg) },
	}
	return a, nil
}

func (a *app) logf(format string, args ...any) {
	fmt.Fprintf(a.errOut, format+"\n", args...)
}

// record adds to the history. Once money may have moved, failing to write it
// down must not hide the result, so this only warns.
func (a *app) record(e history.Event) {
	if err := a.hist.Append(e); err != nil {
		a.logf("warning: could not write to the history (%s): %v", a.hist.Path, err)
	}
}

func (a *app) outputDir(jobID string) string {
	return filepath.Join(a.cfg.DataDir, "outputs", media.SafeName(jobID))
}

// confirm asks before something that spends money or cannot be undone, unless
// --yes was given. No answer, as in a script, means no.
func confirm(cmd *cobra.Command, question string) error {
	if flagYes {
		return nil
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "%s [y/N] ", question)
	answer, _ := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return nil
	}
	return errors.New("aborted, nothing was sent (pass --yes to skip the question)")
}

// emit prints v as JSON when --json was given, and otherwise runs text.
func emit(w io.Writer, v any, text func() error) error {
	if flagJSON {
		return output.JSON(w, v)
	}
	return text()
}

func newRootCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pikactl",
		Short: "Run every Pika API operation from the command line, and keep a history",
		Long: "Run every Pika API operation from the command line: video, image, audio,\n" +
			"music, speech, transcription, language models and decisions.\n\n" +
			"Operations and their flags come from Pika's live catalog, so a model added\n" +
			"tomorrow works today: `pikactl models` lists them and\n" +
			"`pikactl run <id> --help` shows one's parameters. Anything without a command\n" +
			"of its own is reachable with `pikactl api`.\n\n" +
			"Every request is written to a local history (`pikactl history`), with the\n" +
			"prompt, the price, the charge and where the files went. Generation costs real\n" +
			"money: `run` quotes the price and asks first.",
		Version:       buildVersion(),
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	pf := cmd.PersistentFlags()
	pf.StringVar(&flagConfigPath, "config", defaultConfigPath(), "path to config file")
	pf.BoolVar(&flagJSON, "json", false, "print the result as JSON")
	pf.BoolVarP(&flagYes, "yes", "y", false, "do not ask before spending money or deleting")

	cmd.AddCommand(
		newAuthCommand(),
		newModelsCommand(),
		newRunCommand(),
		newQuoteCommand(),
		newChatCommand(),
		newDecideCommand(),
		newJobsCommand(),
		newHistoryCommand(),
		newUploadsCommand(),
		newBalanceCommand(),
		newBillingCommand(),
		newVoicesCommand(),
		newWebhooksCommand(),
		newEndpointsCommand(),
		newAPICommand(),
	)
	return cmd
}

// Execute runs the root command, exiting the process on error.
func Execute() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	err := newRootCommand().ExecuteContext(ctx)
	if err == nil {
		return
	}
	if ctx.Err() != nil {
		fmt.Fprintln(os.Stderr, "interrupted")
		os.Exit(interruptExitCode)
	}
	fmt.Fprintln(os.Stderr, "pikactl:", err)
	os.Exit(exitStatus(err))
}

func exitStatus(err error) int {
	var apiErr *api.Error
	var failed *jobFailedError
	switch {
	case errors.As(err, &failed) && failed.Code == "rate_limited":
		return exitThrottled
	case errors.As(err, &failed):
		return exitAPI
	case errors.As(err, &apiErr) && apiErr.Auth():
		return exitAuth
	case errors.As(err, &apiErr) && apiErr.RateLimited():
		return exitThrottled
	case errors.As(err, &apiErr):
		return exitAPI
	case strings.HasPrefix(err.Error(), "no API key"):
		return exitAuth
	}
	return 1
}

// jobFailedError is a job that reached the failed state.
type jobFailedError struct {
	ID, Code, Message string
}

func (e *jobFailedError) Error() string {
	return fmt.Sprintf("job %s failed: %s: %s", e.ID, e.Code, e.Message)
}
