# pikactl

Run every [Pika API](https://dev.pika.art) operation from the command line: video, image, audio,
music, speech, transcription, language models and decisions. Every request is kept in a local
history, with its prompt, price, charge and files.

```console
$ pikactl run pika/pika-h3-flex/text-to-video --prompt "a paper boat crossing a puddle" --duration 5
operation:  pika/pika-h3-flex/text-to-video
request:    {"duration":5,"prompt":"a paper boat crossing a puddle"}
price:      $0.10
balance:    $10.00
Run this paid generation for $0.10? [y/N] y
job:        media_8f3a2c91-… (history: 20261031-151023-a1b2c3)
status:     queued
status:     running
status:     completed
charged:    $0.10
/Users/you/.local/share/pikactl/outputs/media_8f3a2c91-…/video.mp4
```

> **Unofficial.** pikactl is an independent project, not made or endorsed by Pika Labs. You need your
> own Pika account and API key, and you pay Pika directly for what you generate.

## Why use it

- **It keeps up by itself.** No model is written into pikactl. Operations, their flags and their
  prices are read from Pika's live catalog, so a model or parameter added tomorrow works today:
  `pikactl run <id> --help` always describes what the API accepts right now.
- **You keep a history.** Pika has no endpoint that lists your past jobs. pikactl keeps an
  append-only record of every request, including what you asked for, what it cost and where the
  files went.
- **It is careful with money.** Each paid job is quoted, checked against your balance and confirmed
  before it is sent, and a lost connection cannot start it twice.

## Install

```sh
go install github.com/orshih6/pikactl/cmd/pikactl@latest
```

Or download a binary from the [releases page](https://github.com/orshih6/pikactl/releases).

## Set up

1. Sign in at <https://dev.pika.art/login>, which creates your organisation.
2. Create a key at <https://dev.pika.art/keys>.
3. Add funds at <https://dev.pika.art/billing>. Generation is prepaid. Pika's docs list no free tier,
   though a job covered by a promotion settles at $0.00, and pikactl names the promotion when one does.
4. Give pikactl the key, in any one of these ways:

```sh
pikactl auth login                    # asks for it, checks it, saves it (mode 0600)
pbpaste | pikactl auth login          # the same, from the clipboard
export PIKA_API_KEY=...               # or from the environment
echo 'PIKA_API_KEY=...' >> .env       # or from a .env here or in a parent directory
```

Browsing the catalog and getting quotes are free and need no key at all.

## Quick start

```sh
pikactl models --category video                 # what is available, from what price
pikactl models competitors seedance             # Pika's price beside other providers'
pikactl run pika/pika-h3-flex/text-to-video --help   # that operation's inputs and prices
pikactl quote pika/pika-h3-flex/text-to-video --prompt test --duration 10    # $0.20

pikactl run pika/pika-h3-flex/text-to-video --prompt @prompt.txt --aspect-ratio 9:16 -o boat.mp4
pikactl run pika/pika-h3-flex/image-to-video --image ./photo.png --prompt "she turns and smiles"
pikactl run pika/video-compose/compose-video --tracks @timeline.json

pikactl chat anthropic/claude-opus-5.5 "Five title ideas for a video about tides"
pikactl history
```

## Giving inputs

Each input in an operation's schema becomes a flag, in the type the schema asks for.

| You write | It means |
|---|---|
| `--duration 10` | A typed value. Enums are checked, and `--aspect_ratio` works as well as `--aspect-ratio`. |
| `--prompt @prompt.txt` | Text from a file, for a long prompt. |
| `--image ./photo.png` | A local file. It is uploaded first and its hosted URL is sent. URLs pass through as they are. This also works for files nested inside a JSON input. |
| `--image-urls a.png --image-urls b.png` | An array, by repeating the flag. |
| `--tracks @timeline.json` or `--tracks '[…]'` | A nested object or array, as JSON or from a file. |
| `--set seed=7` | Any input by its API name. |
| `--body @request.json` | A whole request body, which other flags then add to. `-` reads stdin. |

Inputs you do not set are not sent, so the API's current defaults apply.

## Money: read this once

Every generation is real money, and there is no test environment.

- `run` gets a free quote, which also makes the API validate the request, so a bad request costs
  nothing. It then reads your balance and asks. `--yes` skips the question and nothing else.
- `--max-usd 0.50` refuses anything quoted above that, or that has no quote. `--dry-run` shows the
  request and its price and sends nothing.
- With no answer, as in a script without `--yes`, the answer is no.
- A submit carries an idempotency key. If the connection drops, it is repeated under that key, which
  Pika guarantees cannot start a second job. If no answer ever arrives, pikactl says so and records
  the outcome as `unknown` rather than guessing, because the job may be running.
- Interrupting pikactl does not stop a job, which keeps running on Pika's side and may be charged.
  Pick it up with `pikactl jobs wait <id>`.
- A quote is Pika's estimate and the charge is what you paid. They can differ: token-priced models
  vary a little, and a request whose real length is not known up front, such as merging videos, can
  be quoted far above what it ends up costing. The history records both.
- `chat` and `decide` bill by the tokens used, which cannot be quoted, so they do not ask first.
  `--max-tokens` bounds a chat.

## History

Every request that can spend money or change something is written down *before* it is sent. If that
write fails, the request is not sent.

```sh
pikactl history                             # newest first, with cost and prompt
pikactl history --op h3-flex --since 7d     # filter by operation, status or age
pikactl history --note city-2050 --limit 0   # one project's requests and its total charge
pikactl history show <ref>                  # the full request, cost and files
pikactl history rerun <ref> --set seed=7    # the same request again, as a new paid job
pikactl history sync                        # catch up on jobs left running: status, charge, files
```

Anywhere a job is named, you can use its Pika ID, its history ref, or any unique start of either. A
`--note "episode 3 intro"` on `run` keeps a label with the entry.

The history is one [JSON Lines](https://jsonlines.org) file that nothing ever rewrites, so old
entries stay readable as the API and pikactl change: each line holds the request and the API's
answers verbatim, and a future version only ever adds to it. Erasing a job on Pika's side with
`jobs delete` keeps your local entry and files. The history holds your prompts but never your key,
and is readable only by you.

| What | Where | Override |
|---|---|---|
| History | `~/.local/share/pikactl/history.jsonl` | `PIKACTL_DATA` |
| Saved outputs | `~/.local/share/pikactl/outputs/<job id>/` | `PIKACTL_DATA` |
| Catalog cache | `~/.local/share/pikactl/cache/` | `PIKACTL_DATA` |
| Config | `~/.config/pikactl/config.yaml` | `--config`, `PIKACTL_CONFIG` |

Outputs are saved as soon as a job finishes, because Pika's documentation does not promise how long
its file URLs last. `-o` also copies them where you want; the kept copy stays where the history
points.

## Commands

| Command | What it does |
|---|---|
| `models [filter]` | List operations and starting prices (`--category`, `--vendor`, `--refresh`) |
| `models show <id>` | An operation's prices and inputs |
| `models docs <id>` | An operation's full specification from Pika's docs |
| `models competitors [filter]` | Pika's price beside the cheapest other provider's, per operation and setting |
| `run <id> …` | Run any job: quote, ask, submit, wait, save, record |
| `quote <id> …` | Price a request and validate it, for free |
| `chat <model> [prompt]` | Ask a language model, over whichever protocol it uses |
| `decide <model>` | Typed answers about a state (`--state`, `--questions`) |
| `jobs get\|wait\|content\|download\|delete <id>` | Follow, save and erase jobs |
| `history`, `history show\|rerun\|sync` | The local record |
| `uploads put <file>…`, `uploads delete <url>` | Host files to reuse across requests |
| `voices list\|get\|create\|delete` | Voices. `create` requires `--consent`: yours to give, never assumed |
| `balance` | What you can spend |
| `billing spend\|monthly\|monthly-csv` | Pika's own spend figures |
| `webhooks deliveries\|delivery\|attempts\|replay` | Inspect and resend webhook deliveries |
| `endpoints [filter]` | Every route in Pika's live OpenAPI document |
| `api <method> <path>` | Call any route directly, including ones newer than your pikactl |
| `auth login\|status\|logout` | The key |

Global flags: `--json` prints the result as JSON, `-y/--yes` skips questions, `--config` picks a
config file. Results go to stdout and progress to stderr, so `pikactl run … --yes > path.txt` holds
only the file path.

For a response streamed as it is generated, send the request with `pikactl api`, whose output is not
buffered:

```sh
pikactl api POST /v1/chat/completions -d @request.json --yes
```

## Use with an AI assistant

`skills/pikactl/SKILL.md` teaches an assistant to use pikactl and to be careful with your money: it
quotes before running, treats one approval as covering one request (or one agreed budget for a
project), and never asks you to paste your
key into the chat. For Claude Code:

```sh
git clone https://github.com/orshih6/pikactl
mkdir -p ~/.claude/skills && cp -R pikactl/skills/pikactl ~/.claude/skills/
```

It also carries a whole-video workflow (`skills/pikactl/video.md`): script, storyboard and one budget
for the video, shots, checking every clip, fixes, voice, music, then assembly by
`skills/pikactl/scripts/video.py` (Python, ffmpeg and Chrome; it spends nothing): captions timed to the
voice from its transcript, cross-faded shots, music that dips under the voice, and loudness for phones.
For livelier videos, `skills/pikactl/studio/` is a small Remotion project (run `npm install` in it once)
that renders a JSON list of scenes: clips, photos with a slow zoom, phone screenshots with a highlight
and a tap, title cards, lists, and word-by-word captions. `video.py studio` and `video.py render` drive it.

Other assistants that read the same skill format can load that file too. Nothing in it can stop an
assistant from spending: keep `--max-usd` in mind, and a low balance is your real limit.

## Exit statuses

| Status | Meaning |
|---|---|
| 0 | Success |
| 1 | Bad usage, or a local problem |
| 3 | The key is missing, invalid or inactive |
| 4 | The API refused the request, or the job failed |
| 5 | Rate-limited; try again later |
| 130 | Interrupted |

## Development

```sh
make check    # gofmt, vet, and the tests with the race detector
make build    # bin/pikactl
```

The tests use a local fake API and never contact Pika or spend money. Schema tests run against the shapes of
real catalog answers, kept in `internal/catalog/testdata`. To check the flag builder against every
operation, dump the catalog to a directory and point `PIKACTL_SCHEMA_DIR` at it.

## License

[MIT](LICENSE)
