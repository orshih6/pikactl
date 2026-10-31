# CLAUDE.md

Guidance for working in this repo. The command reference is `README.md`, and `skills/pikactl/SKILL.md` is
what AI assistants are told to do. This repo is public: keep
machine-specific paths, real keys and real generation output out of it.

## Working on the code

- Go, cobra CLI. `internal/api` is an HTTP client that knows nothing about models;
  `internal/catalog` reads operations, schemas, prices and docs; `internal/media` drives jobs, uploads
  and downloads; `internal/history` is the local record; `internal/cli` holds the commands.
- `make check && make build` before finishing. Tests use a local fake API and never contact Pika.
- **No model belongs in the code.** Paths, inputs and prices come from the live catalog
  (`GET /catalog/apis/{id}?expand=inputs`), and the protocol of a language model from its documentation
  (`/llms/{id}`). If a change would name a model, it is the wrong change. A new parameter or model must
  work with no code change; `TestFlagsComeFromTheLiveSchema` holds that.
- A plain new route is one entry in an `endpoint` table (`internal/cli/endpoints.go`). `pikactl api`
  reaches it even before that. Add new abilities to the README's tables and the skill.
  `TestDocumentedCommandsExist` fails if either mentions a command or flag that does not exist.
- `internal/catalog/testdata` holds real catalog answers with their descriptions and titles removed, because synthetic schemas missed a real shape:
  an object whose `anyOf` sits beside its `properties`. Add a real answer when a new shape appears.

## Invariants (it spends real money)

- `execute` runs in this order and it matters: upload, quote, `--max-usd`, balance, question, write to
  the history, submit. Nothing is spent until all of those pass.
- The request is written to the history **before** it is sent, and if that write fails it is not sent.
  Later writes only warn, because by then money may have moved and the result must still be shown.
- **Writes are never retried**, except a media submit that got no answer at all, repeated under the same
  `Idempotency-Key` (Pika's documented contract). A submit Pika answered and rejected is not repeated,
  because a failed job replays on its key; a retry needs a fresh one. A submit that never got an answer is
  reported as `unknown`, never as failed, because it may be running.
- Record a charge only when `billing.state` is `settled`. `pending` and `null` are not charges, and a
  settled `0` is. Money is integer micro-USD and is never rounded for display (`output.USD`).
- History is append-only: nothing rewrites or removes a line. Events are stored verbatim with unknown
  fields kept, unknown events ignored and damaged lines skipped, so an old file always stays readable.
  Change the format only by adding to it.
- The key travels in `X-API-Key` only, never in a URL, log line, error, the history or any output.
  - Bearer is not an alternative: the OpenAPI document reserves it on `/billing` and `/v1/webhooks` for
    console session tokens.
  - `net/http` strips only `Authorization` and cookies on a cross-host redirect, so `stripKeyOnRedirect`
    does it for this header.
  - Signed storage URLs, CDNs and the docs host use the client that never has the key (`Client.Plain`).
  - `CheckPath` keeps `pikactl api` on the API host.
- A `.env` file supplies the key and nothing else. It is often someone's free-form notes, and one in a
  cloned repo must not be able to redirect requests, so `PIKA_API_URL` and `PIKACTL_DATA` come only from
  the real environment or the config file.
- Voice cloning needs `--consent` from the person, every time. Never default it.
- Unset inputs are not sent, so the API's current defaults apply. Do not copy schema defaults into a body.

Exit statuses (3 auth, 4 API error or failed job, 5 rate-limited, 130 interrupted) are relied on by
scripts; keep them.

## Things that are true of the API

- The catalog rate-limits: bursts get `429`. Cache lists and do not fetch details in parallel.
- A rejected submit can carry a failed job as its body, with a 4xx or 5xx status, so `Submit` can return
  both a job and an error.
- Pika has no endpoint that lists past jobs, which is why the history exists.
- A job can settle at $0.00 against a real quote, with a `free_generation` object naming the promotion,
  although the docs say there is no free tier. Do not treat a zero charge as an error.
- The webhook secret routes are in the docs but not the OpenAPI document, and an API key gets `401` on
  them, so they have no command. The webhook delivery routes do accept an API key.
- Output URLs can be on `cdn.pika.art` rather than the API host, so downloads go out without the key.
