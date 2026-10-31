package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/orshih6/pikactl/internal/history"
	"github.com/orshih6/pikactl/internal/media"
	"github.com/orshih6/pikactl/internal/output"
)

func newJobsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "jobs",
		Short: "Follow, download and delete jobs",
		Long: "Follow, download and delete jobs. A job can be named by its Pika ID or by its\n" +
			"history ref, and any unique start of either. Pika has no endpoint listing past\n" +
			"jobs; `pikactl history` is the list.",
	}
	cmd.AddCommand(newJobsGetCommand(), newJobsWaitCommand(), newJobsContentCommand(), newJobsDownloadCommand(), newJobsDeleteCommand())
	return cmd
}

// jobID accepts a history ref as well as a job ID. An ID the history has not
// seen is passed on as it is, for a job submitted elsewhere.
func (a *app) jobID(id string) string {
	if record, err := a.hist.Find(id); err == nil && record.JobID != "" {
		return record.JobID
	}
	return id
}

// refFor returns a job's history ref, starting an entry for a job submitted
// elsewhere so that the history stays complete.
func (a *app) refFor(job *media.Job) string {
	if record, err := a.hist.Find(job.ID); err == nil {
		return record.Ref
	}
	ref := a.hist.NewRef()
	a.record(history.Event{
		Type: history.EventRequest, Ref: ref, Kind: history.KindMedia, APIID: job.ModelID,
		JobID: job.ID, Status: job.Status,
	})
	return ref
}

func newJobsGetCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "get <id>",
		Short: "Print a job as the API reports it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			if err := a.cfg.RequireKey(); err != nil {
				return err
			}
			job, err := a.media.Get(cmd.Context(), a.jobID(args[0]))
			if err != nil {
				return err
			}
			return printRawJSON(a.out, job.Raw)
		},
	}
}

func newJobsWaitCommand() *cobra.Command {
	o := &runFlags{}
	cmd := &cobra.Command{
		Use:   "wait <id>",
		Short: "Wait for a job, then record its charge and save its files",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			if err := a.cfg.RequireKey(); err != nil {
				return err
			}
			id := a.jobID(args[0])
			job, err := a.media.Get(cmd.Context(), id)
			if err != nil {
				return err
			}
			return a.waitAndFinish(cmd.Context(), a.refFor(job), job.ID, o)
		},
	}
	o.addExecution(cmd.Flags())
	for _, name := range []string{"max-usd", "dry-run", "no-wait", "webhook-url", "idempotency-key", "note"} {
		_ = cmd.Flags().MarkHidden(name)
	}
	return cmd
}

func newJobsContentCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "content <id>",
		Short: "Print a completed job's download URL",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			if err := a.cfg.RequireKey(); err != nil {
				return err
			}
			url, err := a.media.Content(cmd.Context(), a.jobID(args[0]))
			if err != nil {
				return err
			}
			return emit(a.out, map[string]string{"url": url}, func() error {
				fmt.Fprintln(a.out, url)
				return nil
			})
		},
	}
}

func newJobsDownloadCommand() *cobra.Command {
	var to string
	cmd := &cobra.Command{
		Use:   "download <id>",
		Short: "Save a completed job's files and record them in the history",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			if err := a.cfg.RequireKey(); err != nil {
				return err
			}
			job, err := a.media.Get(cmd.Context(), a.jobID(args[0]))
			if err != nil {
				return err
			}
			if job.Status != media.StatusCompleted {
				return fmt.Errorf("job %s is %s, not completed", job.ID, job.Status)
			}
			files, err := a.saveOutputs(cmd.Context(), a.refFor(job), job)
			if err != nil {
				return err
			}
			if err := copyOutputs(files, to); err != nil {
				return err
			}
			return emit(a.out, files, func() error {
				for _, f := range files {
					fmt.Fprintln(a.out, f.Path)
				}
				return nil
			})
		},
	}
	cmd.Flags().StringVarP(&to, "output", "o", "", "also copy the output here (a directory if there are several files)")
	return cmd
}

func newJobsDeleteCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <id>",
		Short: "Erase a job's stored media and request on Pika's side",
		Long: "Erase a job's stored media and captured request on Pika's side. This cannot be\n" +
			"undone there. Files already saved and the local history entry are kept.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			if err := a.cfg.RequireKey(); err != nil {
				return err
			}
			id := a.jobID(args[0])
			if err := confirm(cmd, "Erase job "+id+" on Pika's side?"); err != nil {
				return err
			}
			if err := a.media.Delete(cmd.Context(), id); err != nil {
				return err
			}
			if record, err := a.hist.Find(id); err == nil {
				a.record(history.Event{Type: history.EventDeleted, Ref: record.Ref})
			} else if !errors.Is(err, history.ErrNotFound) {
				a.logf("warning: %v", err)
			}
			fmt.Fprintf(a.out, "Erased %s\n", id)
			return nil
		},
	}
}

// syncRecord brings one history entry up to date with the API and returns
// whether anything was missing. A failed job has nothing left to fetch but its
// charge; a completed one may also be missing its files.
func (a *app) syncRecord(ctx context.Context, record *history.Record, noDownload bool) (bool, error) {
	wantsCharge := record.ChargeMicroUSD == nil
	wantsFiles := record.Status != media.StatusFailed && len(record.Files) == 0 && !noDownload
	if record.Terminal() && !wantsCharge && !wantsFiles {
		return false, nil
	}
	job, err := a.media.Get(ctx, record.JobID)
	if err != nil {
		return true, err
	}
	if job.Status != record.Status {
		a.recordJob(record.Ref, job)
	}
	if !job.Terminal() {
		a.logf("%s  still %s", record.Ref, job.Status)
		return true, nil
	}
	job = a.media.Settle(ctx, job)
	if charge := job.Charge(); charge != nil && wantsCharge {
		a.record(history.Event{Type: history.EventBilling, Ref: record.Ref, ChargeMicroUSD: charge})
		a.logf("%s  charged %s", record.Ref, output.USD(*charge))
	}
	if job.Status == media.StatusCompleted && wantsFiles {
		files, err := a.saveOutputs(ctx, record.Ref, job)
		if err != nil {
			return true, err
		}
		for _, f := range files {
			a.logf("%s  saved %s", record.Ref, f.Path)
		}
	}
	return true, nil
}
