---
name: pikactl
description: "Use when the user wants to generate or edit media through the Pika API (dev.pika.art): video, images, music, speech, sound effects, transcription, dubbing, lipsync, upscaling, captions, merging or composing clips, including third-party models it hosts such as Seedance, Veo, Kling, Wan, GPT Image or ElevenLabs. Also for Pika API prices, balance and spend, for what was generated before, or to call a language or decision model through a Pika API key. Do it all with the pikactl CLI. Not for Pika's consumer app (pika.art) or its MCP plugin, which use a separate account and credits."
---

# pikactl

Runs every Pika API operation and keeps a local history of them. If it is not installed:
`go install github.com/orshih6/pikactl/cmd/pikactl@latest`. The command reference is `pikactl --help`.

## Before anything

```bash
pikactl auth status     # does the key work, and where does it come from?
pikactl balance         # what can be spent
```

If there is no key, the user creates one at https://dev.pika.art/keys and runs `pikactl auth login`
themselves. **Never ask them to paste the key into the chat**, and never print it or read it out of
a file. Browsing models and getting quotes need no key.

## Rules

1. **Every generation spends real money, and there is no test environment.** Before a paid run, tell
   the user the operation, the settings that matter, how many requests, and the quoted price. Pass
   `--yes` only after they have agreed to that request, and pass `--max-usd` with it.
2. **One yes covers one request.** Extra variants, a batch, or a retry at a different setting each
   need their own. Agreeing to a video does not agree to re-rolling it.
3. **Do not answer from memory.** Models, inputs and prices change: read them with `pikactl models`,
   `pikactl run <id> --help` and `pikactl quote`. Recommend one model, with one alternative only when
   the tradeoff in quality, speed or cost is worth raising.
4. **Quote first.** It is free, and the API validates the request as it prices it, so a refusal here
   costs nothing. Correct the request from the message.
5. **Never switch models by yourself after a failure.** Explain what failed and ask.
6. **An uncertain result is not a failure.** If a submit got no answer, the job may be running. Status
   `unknown` means do not start it again with a new key: run `pikactl history sync`, and repeat it only
   with the same `--idempotency-key`. If a wait timed out or was interrupted, the job is still running
   and may be charged: use `pikactl jobs wait <id>`.
7. **Do not work around moderation.** A `content_moderation` failure is the provider's decision. Tell
   the user, and change the prompt only in a way they would agree with.
8. **Clone a voice only with consent.** Pass `--consent` to `voices create` only when the user has
   told you they have the speaker's permission.
9. `jobs delete`, `uploads delete` and `voices delete` cannot be undone on Pika's side. Ask first.
10. The history holds the user's prompts. Share them only when asked.

On a failed job, `error.code` says what to do: `insufficient_balance` means report the shortfall and
stop; `rate_limited` means wait and try again; `invalid_input` means fix the request. For the
`provider_*` and `timed_out` codes, say what happened and ask before spending again.

## Making something

```bash
pikactl models --category video                  # what exists, from what price
pikactl models seedance                          # or search by name
pikactl models competitors seedance              # Pika's price beside other providers'
pikactl run pika/pika-h3-flex/text-to-video --help     # this model's inputs and prices, live

pikactl quote pika/pika-h3-flex/text-to-video --prompt "a paper boat" --duration 5
pikactl run pika/pika-h3-flex/text-to-video --prompt @prompt.txt --duration 5 --aspect-ratio 9:16 \
  --note "episode 3 intro" --max-usd 0.10 --yes -o intro.mp4
```

The IDs above are examples: take real ones from `pikactl models`. Each input in the model's schema is a
flag. A local file given to a media input is uploaded by itself, including inside a JSON input.
`@file` reads a long prompt or a JSON value from a file, and `--set name=value` sets any input by its
API name.

Results go to stdout and progress to stderr, so stdout is the saved file's path. Add `--json` for the
whole record. After a run, tell the user the model, the path and what it was charged. A quote is an
estimate and the charge is what was paid. A `$0.00` charge is real when pikactl names a promotion
beside it, so say that rather than treating it as an error.

Clips are short, so a longer video is several clips joined. Generate each one (each is its own paid
request) and join them with `pikactl models merge` or `pikactl models compose` to find the operation.
Read its `--help` first: it needs a total length it can price, and its quote can be far above the real
charge when the clips' lengths are not known up front.

## Looking back

```bash
pikactl history                            # newest first: status, cost, prompt
pikactl history --op h3-flex --since 7d
pikactl history show <ref>                 # the full request, charge and files
pikactl history rerun <ref> --set seed=7 --max-usd 0.10 --yes     # a new paid job: rule 2 applies
pikactl history sync                       # catch up on jobs left running
```

Pika has no endpoint that lists past jobs, so this history is the only list. Anywhere a job is named,
its Pika ID, its history ref, or any unique start of either works.

## Money and everything else

```bash
pikactl balance
pikactl billing spend --start-date 2026-10-01 --end-date 2026-10-31
pikactl chat <model> "a question"          # protocol is read from the model's docs; bills by tokens
pikactl decide <model> --state @state.json --questions @questions.json
pikactl voices list
pikactl models docs <id>                   # the operation's full specification
pikactl endpoints                          # every route in the live OpenAPI document
pikactl api GET /billing/balance           # call any route, including ones newer than this CLI
```

`chat` and `decide` have no quote and do not ask first, so rule 1 is yours to apply: bound a chat with
`--max-tokens` and say what you are about to send when it is large. Anything but GET through
`pikactl api` asks first, and may spend money.

Exit statuses: 3 means the key is missing or rejected, 4 the API refused or the job failed, 5
rate-limited, 130 interrupted.
