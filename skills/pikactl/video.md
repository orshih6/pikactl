# Making a whole video with pikactl

A short video (15–60 s, usually vertical) made end to end: script, shots, fixes, voice, music,
captions, assembly. pikactl makes every paid piece; `scripts/video.py` next to this file assembles
them locally and spends nothing. It needs ffmpeg and Chrome or Chromium. Work in one folder per video
(`clips/`, `audio/`, `prompts/`, `out/`) and keep every prompt in a file.

The rules of `SKILL.md` hold throughout. The one difference is how money is approved: **one budget per
video** (step 2).

## 1. Brief and script

- Ask only what changes the video: topic, audience and language, length, aspect ratio, voice or not,
  music or not, and where it will be posted. Anything else, pick and say what you picked.
- Write the script first: one short spoken line per shot (3–5 s each, about 10–14 characters a second
  for most languages), a question or surprise in the first line, a question to the viewer at the end.
- Each line has two forms: **what the voice says** (numbers, years and abbreviations written out as
  words, so a speech model reads them right) and **the caption** (digits and short forms are fine).
- One short caption on screen at a time, saying what the voice says. Paragraphs on screen don't get read.
- Have a reader of that language check the script before anything is generated (a second agent given
  only the script and the language's style rules catches what the writer can't).

## 2. Storyboard, price, budget

- One shot per line (two short lines may share a shot). For each shot: the prompt, its length, the model.
  Take models, inputs and prices from the live catalog. Don't pick from memory:

```bash
pikactl models --category video                 # what exists, from what price
pikactl run <video-model> --help                # its inputs, lengths, resolutions and prices
pikactl quote <video-model> --prompt @prompts/1-dawn.txt --duration 5
```
- Quote every planned request (free) and add room for fixes, about a third more.
- Show the user the storyboard, the script, and the total, and ask for **one budget** for the video.
  Their yes covers every request in that plan (shots, fixes, voice, music, checks) while the total
  spent stays inside it. Ask again before you go over it, for anything outside the plan, and before
  any retry after a failed job (`SKILL.md` rules 5–6).
- Label every request with the video's name, so its spend is one command:

```bash
pikactl run <video-model> --prompt @prompts/1-dawn.txt --duration 5 --note "city-2050: shot 1" --max-usd 0.20 --yes -o clips/1-dawn.mp4
pikactl history --note "city-2050" --limit 0     # every request of this video, and the total charged
```

## 3. Shots

- Prompts that look real name a camera and a light ("photorealistic, 35mm, natural colours, soft
  morning light, slow dolly"); stylised 3D objects (cubes, glowing icons) read as stock animation.
- Say what must not be in the picture: "no text, no readable signs, no logos, no flags". Models still
  add signs, shop fronts and app icons, often late in the clip.
- Watch for the wrong culture. A city at night drifts towards whatever the model has seen most (red
  paper lanterns, neon signs in another script). Keep buildings far from the camera, or name what the
  place really has.
- Run independent shots in parallel (one background job each) and check each one's charge.
- Where a model offers a cheap draft resolution and a "draft to video" finish, try an idea cheaply first
  and finish only the take you keep (read that operation's `--help` for how to pass the draft).

## 4. Look at every clip before using it

```bash
python3 scripts/video.py sheet clips/*.mp4 -o sheet.png --frames 3
```

Read the sheet image. Frames come from the start, middle and end of each clip, because problems often
appear late: text or logos, the wrong place or culture, deformed hands or faces, objects that shouldn't
be there. Zoom in (crop one frame with ffmpeg) when a detail could be writing.

## 5. Fixing a clip, cheapest first

1. **Use only the clean part.** In the project file, `"clip_use": 4.0` keeps the first 4 s, and the
   build slows it smoothly to the length the shot needs. It's free, and right when a problem enters late.
2. **Edit one area or subject**, using a video-to-video operation from the catalog. Pikaswaps replaces
   one area (described in words or marked with a mask image). Pikadditions adds a subject. Both make
   5 or 10 s clips. They keep the rest of the scene, but they are built for one clear subject: one try
   at removing many small objects everywhere (lanterns over a street) left them in place, and the
   output had no audio.
3. **Rewrite the whole clip** with a video-editing operation (summer to winter, day to night). Check its
   limits in `--help`: some keep only 10 s, some return 720p, which then needs an upscale.
4. **Make the shot again** with a better prompt. Often the surest fix when the problem is everywhere in
   the frame.

Each edit is a new generation and can change details you didn't ask for, so look at it again (step 4).

## 6. Voice

- Use a speech model that supports the language. Set `--language-code` when the model has it. The
  model's description may not list every language it speaks.
- Make the whole script as one request, so the delivery stays even. If the user wants to choose a voice
  by ear, make two voices and let them pick.
- Check it by transcribing it with language auto-detect (cheap). The detected language and the text show
  whether it really speaks the language. A word heard wrongly in the transcript is usually a word said
  wrongly: rephrase that line with an easier word rather than re-rolling.

```bash
pikactl run <speech-model> --text @voice.txt --voice <voice> --language-code <code> --note "city-2050: voice" --yes -o audio/voice.mp3
pikactl run <transcription-model> --audio-url audio/voice.mp3 --duration-seconds 29.4 --note "city-2050: voice check" --yes --json
```

The transcript JSON with word timings is saved under the request's files (`pikactl history show <ref>`).
Copy it next to the voice as `audio/transcript.json`.

## 7. Music

Ask for instrumental music in the prompt (and with the model's instrumental switch, if it has one), a
little longer than the video. If a music model fails with a `provider_*` error, say so and ask before
paying another model (rule 5). A video with voice and no music is a fine fallback.

## 8. Assemble

Write `project.json` (the format is at the top of `scripts/video.py`), then:

```bash
python3 scripts/video.py timing project.json      # matches the transcript to the script: caption and cut times
python3 scripts/video.py overlays project.json    # captions and corner labels as images
python3 scripts/video.py build project.json       # cross-faded shots, captions, voice, ducked music, loudness
python3 scripts/video.py check out/video.mp4      # length, loudness, the first frame
```

`timing` prints each caption with what the transcript heard, which shows mispronounced words in one
place. `build` never fades in from black: on most feeds the first frame is the cover, so it shows the
first shot with the opening question already on it. Loudness is normalised for phones (-15 LUFS).

## 9. Final check and hand-over

- `video.py sheet out/video.mp4 -o final.png --frames 6`, and read it: captions readable over every shot,
  nothing cut off, the first frame works as a cover, the end question visible before the fade.
- Give the user the file and the total from `pikactl history --note <video>`, and name what only they
  can judge: the sound, and any word the transcript heard differently.
- Posting it anywhere is a separate step that needs the user's go-ahead on the final text and file.
