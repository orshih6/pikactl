package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/orshih6/pikactl/internal/api"
	"github.com/orshih6/pikactl/internal/catalog"
	"github.com/orshih6/pikactl/internal/history"
	"github.com/orshih6/pikactl/internal/media"
	"github.com/orshih6/pikactl/internal/output"
)

// execution is one paid job to run.
type execution struct {
	op      *catalog.Op
	schema  *catalog.Schema
	body    map[string]any
	flags   *runFlags
	rerunOf string
}

func formatUSD(q *catalog.Quote) string {
	if q == nil {
		return "unknown"
	}
	return output.USD(q.MicroUSD)
}

// execute runs a job from price to files. The order matters: nothing is spent
// until the request has passed the free quote, the cap, the balance and the
// question, and it is written to the history before it is sent.
func (a *app) execute(ctx context.Context, x execution) error {
	o := x.flags
	uploads, err := a.uploadLocalFiles(ctx, x.schema, x.body)
	if err != nil {
		return err
	}
	submitting := false
	defer func() {
		if !submitting {
			a.discardUploads(uploads)
		}
	}()

	raw, err := json.Marshal(x.body)
	if err != nil {
		return err
	}
	quote, err := a.quoteFor(ctx, x.op.ID, raw)
	if err != nil {
		return err
	}

	fields := []output.Field{
		{Name: "operation", Value: x.op.ID},
		{Name: "request", Value: output.Truncate(string(raw), 300)},
		{Name: "price", Value: formatUSD(quote)},
	}
	if o.dryRun {
		_ = output.Fields(a.errOut, fields)
		return emit(a.out, map[string]any{"operation": x.op.ID, "path": x.op.Call.Path, "body": x.body, "quote": quote}, func() error {
			a.logf("dry run, nothing was sent")
			return nil
		})
	}

	if o.maxUSD > 0 {
		limit := int64(math.Round(o.maxUSD * 1e6))
		switch {
		case quote == nil:
			return fmt.Errorf("--max-usd is set but this request has no quote, so it was not sent")
		case quote.MicroUSD > limit:
			return fmt.Errorf("the quote of %s is above --max-usd %s, so it was not sent", output.USD(quote.MicroUSD), output.USD(limit))
		}
	}

	if err := a.cfg.RequireKey(); err != nil {
		return err
	}
	balance, err := a.readBalance(ctx)
	if err != nil {
		return err
	}
	fields = append(fields, output.Field{Name: "balance", Value: output.USD(balance.Available())})
	_ = output.Fields(a.errOut, fields)
	if available := balance.Available(); quote != nil && available < quote.MicroUSD {
		return fmt.Errorf("not enough balance, nothing was sent: this costs %s and %s is available; add funds at https://dev.pika.art/billing",
			output.USD(quote.MicroUSD), output.USD(available))
	} else if quote == nil && available <= 0 {
		return fmt.Errorf("not enough balance, nothing was sent: %s is available; add funds at https://dev.pika.art/billing", output.USD(available))
	}

	question := "Run this paid generation for " + formatUSD(quote) + "?"
	if quote == nil {
		question = "This request has no quote, so its price is unknown. Run it anyway?"
	}
	if err := confirm(a.cmd, question); err != nil {
		return err
	}

	submit := x.body
	if o.webhookURL != "" {
		// The quote's schema has no place for it, so it is added only now.
		submit = map[string]any{"webhook_url": o.webhookURL}
		for k, v := range x.body {
			submit[k] = v
		}
	}
	submitRaw, err := json.Marshal(submit)
	if err != nil {
		return err
	}
	key := o.idemKey
	if key == "" {
		key = media.NewIdempotencyKey()
	}
	ref := a.hist.NewRef()
	event := history.Event{
		Type: history.EventRequest, Ref: ref, Kind: history.KindMedia, APIID: x.op.ID,
		Method: x.op.Call.Method, Path: x.op.Call.Path, Body: submitRaw, IdempotencyKey: key,
		Note: o.note, RerunOf: x.rerunOf, Uploads: uploadMap(uploads),
	}
	if quote != nil {
		event.QuoteMicroUSD = &quote.MicroUSD
	}
	// This one is not best-effort: no money moves without a record of it.
	if err := a.hist.Append(event); err != nil {
		return fmt.Errorf("could not write to the history, so nothing was sent: %w", err)
	}

	submitting = true
	job, err := a.media.Submit(ctx, x.op.Call.Path, submitRaw, key)
	if job == nil {
		a.recordSubmitError(ref, err)
		return err
	}
	a.recordJob(ref, job)
	if err != nil {
		return err
	}
	if job.Status == media.StatusFailed {
		return a.finish(ctx, ref, job, o)
	}
	a.logf("job:        %s (history: %s)", job.ID, ref)
	if o.noWait {
		return emit(a.out, map[string]string{"ref": ref, "job_id": job.ID, "status": job.Status}, func() error {
			fmt.Fprintln(a.out, job.ID)
			return nil
		})
	}
	return a.waitAndFinish(ctx, ref, job.ID, o)
}

func (a *app) recordSubmitError(ref string, err error) {
	event := history.Event{Type: history.EventResponse, Ref: ref, ErrorMessage: err.Error()}
	var lost *media.LostError
	var apiErr *api.Error
	switch {
	case errors.As(err, &lost):
		event.Status = history.StatusUnknown
	case errors.As(err, &apiErr):
		event.Status = media.StatusFailed
		event.HTTPStatus, event.ErrorCode, event.ErrorMessage = apiErr.Status, apiErr.Code, apiErr.Message
	default:
		event.Status = history.StatusUnknown
	}
	a.record(event)
}

func (a *app) recordJob(ref string, job *media.Job) {
	event := history.Event{Type: history.EventResponse, Ref: ref, JobID: job.ID, Status: job.Status, Response: job.Raw}
	if job.Error != nil {
		event.ErrorCode, event.ErrorMessage = job.Error.Code, job.Error.Message
	}
	a.record(event)
}

func (a *app) waitAndFinish(ctx context.Context, ref, jobID string, o *runFlags) error {
	job, err := a.media.Wait(ctx, jobID, media.WaitOptions{
		Timeout: o.timeout,
		OnChange: func(j *media.Job) {
			a.logf("status:     %s", j.Status)
			if !j.Terminal() {
				a.record(history.Event{Type: history.EventResponse, Ref: ref, JobID: j.ID, Status: j.Status})
			}
		},
	})
	if err != nil {
		a.logf("the job keeps running on Pika's side and may be charged; pick it up with: pikactl jobs wait %s", jobID)
		return err
	}
	a.recordJob(ref, job)
	return a.finish(ctx, ref, job, o)
}

// finish records a terminal job's charge and files and reports the result. The
// job is paid for by now, so a failed download says how to retry and the URL
// stays in the history.
func (a *app) finish(ctx context.Context, ref string, job *media.Job, o *runFlags) error {
	job = a.media.Settle(ctx, job)
	if charge := job.Charge(); charge != nil {
		a.record(history.Event{Type: history.EventBilling, Ref: ref, ChargeMicroUSD: charge})
		if job.FreeGeneration != nil {
			a.logf("charged:    %s (free generation: %s)", output.USD(*charge), job.FreeGeneration.PromoID)
		} else {
			a.logf("charged:    %s", output.USD(*charge))
		}
	} else {
		a.logf("charged:    not settled yet (`pikactl history sync` picks it up)")
	}
	if job.Status == media.StatusFailed {
		failed := &jobFailedError{ID: job.ID, Code: "unknown", Message: "no reason given"}
		if job.Error != nil {
			failed.Code, failed.Message = job.Error.Code, job.Error.Message
		}
		return failed
	}

	var files []history.File
	if !o.noDownload {
		saved, err := a.saveOutputs(ctx, ref, job)
		if err != nil {
			a.logf("the job finished but saving failed; retry with: pikactl jobs download %s", job.ID)
			return err
		}
		files = saved
		if err := copyOutputs(files, o.output); err != nil {
			return fmt.Errorf("the files are saved, but copying them to %s failed: %w", o.output, err)
		}
	}

	if flagJSON {
		record, err := a.hist.Find(ref)
		if err != nil {
			return err
		}
		return output.JSON(a.out, record)
	}
	for _, f := range files {
		fmt.Fprintln(a.out, f.Path)
	}
	if len(media.Assets(job.Output)) == 0 && len(job.Output) > 0 {
		// Some operations answer in the output itself, with no file to save.
		fmt.Fprintln(a.out, string(job.Output))
	}
	return nil
}

func (a *app) saveOutputs(ctx context.Context, ref string, job *media.Job) ([]history.File, error) {
	var files []history.File
	for _, asset := range media.Assets(job.Output) {
		file, err := a.media.Download(ctx, asset, a.outputDir(job.ID))
		if err != nil {
			if len(files) > 0 {
				a.record(history.Event{Type: history.EventFiles, Ref: ref, Files: files})
			}
			return nil, fmt.Errorf("saving %s: %w", asset.Name, err)
		}
		files = append(files, history.File(file))
	}
	if len(files) > 0 {
		a.record(history.Event{Type: history.EventFiles, Ref: ref, Files: files})
	}
	return files, nil
}

func copyOutputs(files []history.File, to string) error {
	if to == "" || len(files) == 0 {
		return nil
	}
	info, err := os.Stat(to)
	isDir := err == nil && info.IsDir()
	if len(files) > 1 || isDir || strings.HasSuffix(to, string(filepath.Separator)) {
		for _, f := range files {
			if err := media.CopyFile(f.Path, filepath.Join(to, filepath.Base(f.Path))); err != nil {
				return err
			}
		}
		return nil
	}
	return media.CopyFile(files[0].Path, to)
}

// quoteFor prices a request. A request the API calls invalid stops here, for
// free. A quote that merely cannot be had leaves the price unknown, which the
// caller then has to accept out loud.
func (a *app) quoteFor(ctx context.Context, id string, raw []byte) (*catalog.Quote, error) {
	quote, err := a.catalog.GetQuote(ctx, id, raw)
	if err == nil {
		return quote, nil
	}
	if errors.Is(err, catalog.ErrNoQuote) {
		return nil, nil
	}
	var apiErr *api.Error
	if errors.As(err, &apiErr) && !api.Transient(err) {
		return nil, fmt.Errorf("the API refused this request, nothing was sent: %w", err)
	}
	if ctx.Err() != nil {
		return nil, err
	}
	a.logf("warning: could not get a quote: %v", err)
	return nil, nil
}

// uploadLocalFiles replaces every local path at a media input with its hosted
// URL, at any depth, and returns what it uploaded.
func (a *app) uploadLocalFiles(ctx context.Context, schema *catalog.Schema, body map[string]any) ([]*media.Upload, error) {
	var uploads []*media.Upload
	seen := map[string]string{}
	_, err := schema.MapMedia(body, func(value string) (string, error) {
		if strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") || strings.HasPrefix(value, "data:") {
			return value, nil
		}
		if hosted, ok := seen[value]; ok {
			return hosted, nil
		}
		path := expandHome(value)
		if _, err := os.Stat(path); err != nil {
			return "", fmt.Errorf("%q is neither a URL nor a file that exists", value)
		}
		if err := a.cfg.RequireKey(); err != nil {
			return "", err
		}
		a.logf("uploading:  %s", path)
		upload, err := a.media.Upload(ctx, path)
		if err != nil {
			return "", err
		}
		uploads = append(uploads, upload)
		seen[value] = upload.URL
		return upload.URL, nil
	})
	if err != nil {
		a.discardUploads(uploads)
		return nil, err
	}
	return uploads, nil
}

func expandHome(path string) string {
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, rest)
		}
	}
	return path
}

// discardUploads removes files uploaded for a request that was never sent. It
// must run even after Ctrl-C, so it does not use the command's context.
func (a *app) discardUploads(uploads []*media.Upload) {
	for _, upload := range uploads {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		if err := a.media.DeleteUpload(ctx, upload.URL); err != nil {
			a.logf("warning: could not remove the upload of %s: %v", upload.LocalPath, err)
		}
		cancel()
	}
}

func uploadMap(uploads []*media.Upload) map[string]string {
	if len(uploads) == 0 {
		return nil
	}
	out := make(map[string]string, len(uploads))
	for _, upload := range uploads {
		out[upload.LocalPath] = upload.URL
	}
	return out
}

// balanceInfo is the balance object.
type balanceInfo struct {
	BalanceMicroUSD int64 `json:"balance_micro_usd"`
	PrepaidMicroUSD int64 `json:"prepaid_micro_usd"`
	CreditMicroUSD  int64 `json:"credit_micro_usd"`
	Postpaid        *struct {
		Active bool `json:"active"`
		Cycle  *struct {
			RemainingMicroUSD int64 `json:"remaining_micro_usd"`
		} `json:"cycle"`
	} `json:"postpaid"`
}

// Available is what can be spent now: the balance, or on an active invoice
// plan what remains of the cycle's limit, which then replaces it as the gate.
func (b *balanceInfo) Available() int64 {
	if b.Postpaid != nil && b.Postpaid.Active && b.Postpaid.Cycle != nil {
		return b.Postpaid.Cycle.RemainingMicroUSD
	}
	return b.BalanceMicroUSD
}

func (a *app) readBalance(ctx context.Context) (*balanceInfo, error) {
	resp, err := a.api.Do(ctx, api.Request{Method: http.MethodGet, Path: "/billing/balance"})
	if err != nil {
		return nil, err
	}
	var balance balanceInfo
	if err := json.Unmarshal(resp.Body, &balance); err != nil {
		return nil, fmt.Errorf("reading the balance: %w", err)
	}
	return &balance, nil
}
