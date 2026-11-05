#!/usr/bin/env python3
"""Assemble a short video from generated clips, a voice-over and music, with one caption per line.

Needs Python 3.9+, ffmpeg/ffprobe and Chrome or Chromium (captions are drawn as HTML, so any
script and emoji work). Spends nothing: the clips, voice, transcript and music are made first
with pikactl. See ../video.md for the whole workflow.

  video.py sheet CLIP... -o sheet.png [--frames 3]   frames from each clip, to look at before using it
  video.py timing project.json                       caption and shot times from the voice's transcript
  video.py overlays project.json                     caption images (PNG with transparency)
  video.py build project.json                        the finished video
  video.py check VIDEO                               length, loudness, size, and the first frame

project.json (paths are relative to the file):
{
  "size": [1080, 1920], "fps": 30, "crossfade": 0.5, "loudness": -15,
  "font": "-apple-system, 'Helvetica Neue', 'Noto Sans', sans-serif",
  "corner_left": "AI-generated", "corner_right": "@account",        optional labels at the top
  "voice": {"file": "audio/voice.mp3", "transcript": "audio/transcript.json", "at": 0.4},
  "music": {"file": "audio/music.mp3", "gain_db": -12, "duck": true},  optional
  "end_caption": "Tell us in the comments", "end_hold": 2.8,
  "out": "out/video.mp4",
  "lines": [
    {"clip": "clips/1.mp4", "say": "What will the city look like in twenty fifty?",
     "show": ["What will the city look like in 2050?"]},
    {"clip": "clips/1.mp4", "say": "Clean air.", "show": ["Clean air."]},
    {"clip": "clips/2.mp4", "say": "No traffic jams. Ever.", "show": ["No traffic jams.", "Ever."],
     "say_parts": ["No traffic jams.", "Ever."], "clip_start": 0, "clip_use": 4.0}
  ]
}
"say" is what the voice reads (numbers and abbreviations as words); "show" is the caption, one
or more pieces shown one after another, each with its "say_parts" piece. Consecutive lines with
the same clip share one shot. "clip_use" uses only the first N seconds of a clip (after
"clip_start"), slowed down smoothly if the shot needs longer.

The transcript is a speech-to-text result with word timings: {"words": [{"text", "start", "end",
"type"}]}, as ElevenLabs Scribe returns. `timing` matches it to the script word by word, so a
number read as words or a misheard word does not shift the captions.
"""
import argparse, difflib, html, json, os, re, shutil, subprocess, sys

CSS = """
*{margin:0;padding:0;box-sizing:border-box}
html,body{width:%(w)dpx;height:%(h)dpx;background:transparent;overflow:hidden}
body{font-family:%(font)s,"Apple Color Emoji","Noto Color Emoji";color:#fff;-webkit-font-smoothing:antialiased}
.cap{position:absolute;left:7.5%%;right:7.5%%;top:%(captop)dpx;text-align:center;font-weight:800;
  font-size:%(fs)dpx;line-height:1.16;letter-spacing:-.3px;
  text-shadow:0 0 2px rgba(0,0,0,.9),0 4px 18px rgba(0,0,0,.85),0 0 40px rgba(0,0,0,.6)}
.hook .cap{top:%(hooktop)dpx;font-size:%(hfs)dpx;line-height:1.08}
.pill{position:absolute;top:%(pilltop)dpx;font-size:%(pfs)dpx;font-weight:700;padding:10px 20px;
  border-radius:30px;background:rgba(0,0,0,.45)}
.l{left:5.4%%}.r{right:5.4%%}
"""


def die(msg):
    sys.exit(f"video.py: {msg}")


def sh(args, **kw):
    r = subprocess.run(args, capture_output=True, text=True, **kw)
    if r.returncode != 0:
        die(f"{args[0]} failed: {r.stderr.strip()[-800:]}")
    return r.stdout


def ffmpeg(args):
    sh(["ffmpeg", "-y", "-loglevel", "error"] + args)


def duration(path):
    return float(sh(["ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", path]).strip())


def chrome():
    for c in [os.environ.get("CHROME", ""), "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
              "/Applications/Chromium.app/Contents/MacOS/Chromium"]:
        if c and os.path.exists(c):
            return c
    for name in ["google-chrome", "google-chrome-stable", "chromium", "chromium-browser"]:
        if shutil.which(name):
            return shutil.which(name)
    die("no Chrome or Chromium found; set CHROME to its path")


class Project:
    def __init__(self, path):
        self.path = os.path.abspath(path)
        self.dir = os.path.dirname(self.path)
        self.p = json.load(open(self.path))
        self.w, self.h = self.p.get("size", [1080, 1920])
        self.timeline_path = self.rel(self.p.get("timeline", "timeline.json"))

    def rel(self, p):
        return p if os.path.isabs(p) else os.path.join(self.dir, p)

    def timeline(self):
        if not os.path.exists(self.timeline_path):
            die(f"no {self.timeline_path}: run `video.py timing` first")
        return json.load(open(self.timeline_path))


# --- sheet -------------------------------------------------------------------------------------

def cmd_sheet(a):
    tmp = os.path.join(os.path.dirname(os.path.abspath(a.output)) or ".", ".sheet-tmp")
    os.makedirs(tmp, exist_ok=True)
    rows = []
    for k, clip in enumerate(a.clips):
        d = duration(clip)
        frames = []
        for i in range(a.frames):
            t = d * (i + 0.5) / a.frames          # middle of each slice, so late problems show too
            f = os.path.join(tmp, f"{k}-{i}.png")
            ffmpeg(["-ss", f"{t:.2f}", "-i", clip, "-frames:v", "1", "-vf", f"scale={a.width}:{a.width*16//9}:force_original_aspect_ratio=decrease,pad={a.width}:{a.width*16//9}:(ow-iw)/2:(oh-ih)/2", f])
            frames.append(f)
        row = os.path.join(tmp, f"row{k}.png")
        if len(frames) == 1:
            shutil.copy(frames[0], row)
        else:
            ffmpeg(sum([["-i", f] for f in frames], []) + ["-filter_complex", f"hstack=inputs={len(frames)}", row])
        rows.append(row)
        print(f"{clip}: {d:.2f}s, frames at " + ", ".join(f"{d*(i+0.5)/a.frames:.1f}s" for i in range(a.frames)))
    if len(rows) == 1:
        shutil.copy(rows[0], a.output)
    else:
        ffmpeg(sum([["-i", r] for r in rows], []) + ["-filter_complex", f"vstack=inputs={len(rows)}", a.output])
    shutil.rmtree(tmp, ignore_errors=True)
    print(a.output)


# --- timing ------------------------------------------------------------------------------------

def tokens(text):
    return [t for t in re.split(r"[^\w]+", text.lower()) if t]


def cmd_timing(a):
    pr = Project(a.project)
    p = pr.p
    lines = p["lines"]
    voice = p.get("voice")
    if not voice:
        die("timing needs a voice with a transcript")
    tr = json.load(open(pr.rel(voice["transcript"])))
    words = [w for w in tr.get("words", []) if w.get("type", "word") == "word"]
    if not words:
        die("the transcript has no word timings")
    at = voice.get("at", 0.4)

    # the script, as pieces (one per caption), each a run of tokens
    pieces = []          # (line index, caption text, token count)
    script = []
    for i, ln in enumerate(lines):
        show = ln["show"] if isinstance(ln["show"], list) else [ln["show"]]
        parts = ln.get("say_parts") or ([ln["say"]] if len(show) == 1 else None)
        if not parts or len(parts) != len(show):
            die(f"line {i+1}: give one say_parts piece per caption in show")
        for cap, part in zip(show, parts):
            tk = tokens(part)
            pieces.append((i, cap, len(tk)))
            script += tk
    heard = [tokens(w["text"]) for w in words]
    flat, owner = [], []
    for j, tk in enumerate(heard):
        for t in tk:
            flat.append(t)
            owner.append(j)

    # match script words to heard words; a replaced run maps to the whole heard run
    span = [None] * len(script)
    sm = difflib.SequenceMatcher(None, script, flat, autojunk=False)
    for tag, i1, i2, j1, j2 in sm.get_opcodes():
        if tag in ("equal", "replace") and j2 > j1:
            for k in range(i1, i2):
                jj = j1 + (k - i1) if tag == "equal" else None
                lo = owner[jj] if jj is not None else owner[j1]
                hi = owner[jj] if jj is not None else owner[j2 - 1]
                span[k] = (words[lo]["start"], words[hi]["end"])
    matched = sum(1 for s in span if s)
    if matched < len(script) * 0.7:
        die(f"only {matched} of {len(script)} script words were heard: is this the right transcript?")
    # words not heard at all borrow their neighbours' times
    for k in range(len(span)):
        if span[k] is None:
            prev = next((span[x] for x in range(k - 1, -1, -1) if span[x]), None)
            nxt = next((span[x] for x in range(k + 1, len(span)) if span[x]), None)
            span[k] = ((prev or nxt)[1], (nxt or prev)[0])

    caps, line_span, k = [], {}, 0
    for i, cap, n in pieces:
        s = span[k:k + n]
        a0, b0 = min(x[0] for x in s) + at, max(x[1] for x in s) + at
        caps.append({"text": cap, "a": a0, "b": b0})
        lo, hi = line_span.get(i, (a0, b0))
        line_span[i] = (min(lo, a0), max(hi, b0))
        heard_text = " ".join(w["text"] for w in words if a0 - at - 0.01 <= w["start"] and w["end"] <= b0 - at + 0.01)
        print(f"{a0:6.2f}-{b0:6.2f}  {cap}\n               heard: {heard_text}")
        k += n

    voice_end = max(b for _, b in line_span.values())
    total = round(voice_end + p.get("end_hold", 2.8), 2)
    out_caps = []
    for n, c in enumerate(caps):
        nxt = caps[n + 1]["a"] - 0.05 if n + 1 < len(caps) else voice_end + 0.35
        out_caps.append({"text": c["text"], "start": 0.0 if n == 0 else round(c["a"] - 0.08, 3),
                         "end": round(max(c["b"] + 0.15, nxt), 3), "style": "hook" if n == 0 else "line"})
    if p.get("end_caption"):
        out_caps.append({"text": p["end_caption"], "start": round(voice_end + 0.4, 3), "end": round(total - 0.1, 3),
                         "style": "line"})

    # shots: consecutive lines on one clip share a shot; cut halfway between lines
    shots, cuts = [], [0.0]
    for i in range(len(lines)):
        if i == 0 or lines[i]["clip"] != lines[i - 1]["clip"]:
            if i > 0:
                cuts.append(round((line_span[i - 1][1] + line_span[i][0]) / 2, 3))
            shots.append({k2: lines[i][k2] for k2 in ("clip", "clip_start", "clip_use") if k2 in lines[i]})
    cuts.append(total)
    for j, s in enumerate(shots):
        s["dur"] = round(cuts[j + 1] - cuts[j], 3)
    json.dump({"total": total, "shots": shots, "captions": out_caps}, open(pr.timeline_path, "w"),
              ensure_ascii=False, indent=1)
    print(f"total {total}s; shots " + ", ".join(f"{os.path.basename(s['clip'])} {s['dur']}s" for s in shots))
    print(pr.timeline_path)


# --- overlays ----------------------------------------------------------------------------------

def render(pr, name, body_class, inner):
    d = os.path.join(pr.dir, "overlays")
    os.makedirs(d, exist_ok=True)
    w, h = pr.w, pr.h
    css = CSS % {"w": w, "h": h, "font": pr.p.get("font", "-apple-system,'Helvetica Neue','Noto Sans',sans-serif"),
                 "captop": int(h * 0.604), "hooktop": int(h * 0.5625), "fs": int(w * 0.061), "hfs": int(w * 0.081),
                 "pilltop": int(h * 0.161), "pfs": int(w * 0.03)}
    base = os.path.join(d, name)
    open(base + ".html", "w").write(f'<!doctype html><html><head><meta charset="utf-8"><style>{css}</style></head>'
                                    f'<body class="{body_class}">{inner}</body></html>')
    sh([chrome(), "--headless=new", "--disable-gpu", "--hide-scrollbars", "--default-background-color=00000000",
        f"--window-size={w},{h}", f"--screenshot={base}.png", "file://" + base + ".html"])
    return base + ".png"


def cmd_overlays(a):
    pr = Project(a.project)
    tl = pr.timeline()
    pills = ""
    if pr.p.get("corner_left"):
        pills += f'<div class="pill l">{html.escape(pr.p["corner_left"])}</div>'
    if pr.p.get("corner_right"):
        pills += f'<div class="pill r">{html.escape(pr.p["corner_right"])}</div>'
    render(pr, "frame", "", pills)
    for i, c in enumerate(tl["captions"]):
        render(pr, f"cap{i}", c.get("style", "line"), f'<div class="cap">{html.escape(c["text"])}</div>')
    print(f"{len(tl['captions']) + 1} overlays in {os.path.join(pr.dir, 'overlays')}")


# --- build -------------------------------------------------------------------------------------

def cmd_build(a):
    pr = Project(a.project)
    p, tl = pr.p, pr.timeline()
    W, H, FPS, XF = pr.w, pr.h, p.get("fps", 30), p.get("crossfade", 0.5)
    work = os.path.join(pr.dir, ".build")
    os.makedirs(work, exist_ok=True)
    total = tl["total"]

    parts = []
    for i, s in enumerate(tl["shots"]):
        clip = pr.rel(s["clip"])
        start = s.get("clip_start", 0)
        need = s["dur"] + (XF if i < len(tl["shots"]) - 1 else 0)
        have = min(duration(clip) - start - 0.05, s.get("clip_use", 1e9))
        if have < need - 0.01:   # slow it down smoothly rather than freeze
            vt = (f"trim=start={start}:duration={have},setpts=(PTS-STARTPTS)*{need/have:.4f},"
                  f"minterpolate=fps={FPS}:mi_mode=mci:mc_mode=aobmc:vsbmc=1")
            print(f"shot {i+1}: {os.path.basename(clip)} slowed {need/have:.2f}x")
        else:
            vt = f"trim=start={start}:duration={need},setpts=PTS-STARTPTS,fps={FPS}"
        part = os.path.join(work, f"shot{i}.mp4")
        ffmpeg(["-i", clip, "-vf", f"{vt},scale={W}:{H}:force_original_aspect_ratio=increase,crop={W}:{H},setsar=1,"
                f"format=yuv420p", "-an", "-c:v", "libx264", "-preset", "slow", "-crf", "16", "-t", f"{need}", part])
        parts.append((part, s["dur"]))

    bg = os.path.join(work, "bg.mp4")
    if len(parts) == 1:
        shutil.copy(parts[0][0], bg)
    else:
        args, fil, prev, off = [], [], "0:v", 0.0
        for part, _ in parts:
            args += ["-i", part]
        for i in range(1, len(parts)):
            off += parts[i - 1][1]
            fil.append(f"[{prev}][{i}:v]xfade=transition=fade:duration={XF}:offset={off:.3f}[x{i}]")
            prev = f"x{i}"
        ffmpeg(args + ["-filter_complex", ";".join(fil), "-map", f"[{prev}]", "-c:v", "libx264", "-preset", "slow",
                       "-crf", "16", bg])

    ov = os.path.join(pr.dir, "overlays")
    # no fade in from black: the first frame is the feed's cover
    args = ["-i", bg, "-loop", "1", "-t", f"{total}", "-i", os.path.join(ov, "frame.png")]
    fil = [f"[0:v]fade=t=out:st={total-0.6}:d=0.6[v0]", "[1:v]format=rgba[fr]", "[v0][fr]overlay=0:0[v1]"]
    cur, n = "v1", 2
    for i, c in enumerate(tl["captions"]):
        d = c["end"] - c["start"]
        args += ["-loop", "1", "-t", f"{d:.3f}", "-i", os.path.join(ov, f"cap{i}.png")]
        fin = "" if c["start"] <= 0.01 else "fade=t=in:st=0:d=0.18:alpha=1,"
        fil.append(f"[{n}:v]format=rgba,{fin}fade=t=out:st={d-0.18:.3f}:d=0.18:alpha=1,"
                   f"setpts=PTS-STARTPTS+{c['start']:.3f}/TB[c{i}]")
        fil.append(f"[{cur}][c{i}]overlay=0:0:eof_action=pass[v{n}]")
        cur, n = f"v{n}", n + 1

    audio = []
    voice, music = p.get("voice"), p.get("music")
    if voice:
        ms = int(voice.get("at", 0.4) * 1000)
        args += ["-i", pr.rel(voice["file"])]
        fil.append(f"[{n}:a]aresample=48000,aformat=channel_layouts=stereo,adelay={ms}|{ms},"
                   f"volume={voice.get('gain_db', 0)}dB,asplit=2[va][vsc]")
        audio.append("[va]")
        n += 1
    ducked = False
    if music and os.path.exists(pr.rel(music["file"])):
        args += ["-i", pr.rel(music["file"])]
        fil.append(f"[{n}:a]aresample=48000,aformat=channel_layouts=stereo,atrim=0:{total},"
                   f"volume={music.get('gain_db', -12)}dB,afade=t=in:d=1.0,afade=t=out:st={total-1.5}:d=1.5[ma]")
        if voice and music.get("duck", True):   # the music dips while the voice speaks
            fil.append("[ma][vsc]sidechaincompress=threshold=0.03:ratio=6:attack=20:release=400[mad]")
            audio.append("[mad]")
            ducked = True
        else:
            audio.append("[ma]")
        n += 1
    elif music:
        print(f"warning: no music file at {music['file']}; building without music")
    if voice and not ducked:
        fil.append("[vsc]anullsink")
    if not audio:
        args += ["-f", "lavfi", "-t", f"{total}", "-i", "anullsrc=r=48000:cl=stereo"]
        audio.append(f"[{n}:a]")
    fil.append(f"{''.join(audio)}amix=inputs={len(audio)}:duration=longest:normalize=0,"
               f"loudnorm=I={p.get('loudness', -15)}:TP=-1.5:LRA=11,aresample=48000[a]")
    out = pr.rel(p.get("out", "out/video.mp4"))
    os.makedirs(os.path.dirname(out), exist_ok=True)
    ffmpeg(args + ["-filter_complex", ";".join(fil), "-map", f"[{cur}]", "-map", "[a]", "-t", f"{total}",
                   "-c:v", "libx264", "-preset", "slow", "-crf", "18", "-pix_fmt", "yuv420p",
                   "-c:a", "aac", "-b:a", "192k", "-movflags", "+faststart", out])
    shutil.rmtree(work, ignore_errors=True)
    print(out)


# --- check -------------------------------------------------------------------------------------

def cmd_check(a):
    d = duration(a.video)
    info = sh(["ffprobe", "-v", "error", "-show_entries", "stream=codec_type,width,height", "-of", "csv=p=0", a.video])
    vol = subprocess.run(["ffmpeg", "-i", a.video, "-af", "volumedetect", "-f", "null", "-"],
                         capture_output=True, text=True).stderr
    mean = re.search(r"mean_volume: (\S+) dB", vol)
    peak = re.search(r"max_volume: (\S+) dB", vol)
    first = os.path.splitext(a.video)[0] + "-first-frame.png"
    ffmpeg(["-i", a.video, "-frames:v", "1", "-vf", "scale=360:-2", first])
    print(f"{a.video}: {d:.2f}s, {os.path.getsize(a.video)/1e6:.1f} MB, streams {info.strip().replace(chr(10), '; ')}")
    print(f"audio mean {mean.group(1) if mean else '?'} dB, peak {peak.group(1) if peak else '?'} dB")
    print(f"first frame (the feed's cover): {first}")


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = ap.add_subparsers(dest="cmd", required=True)
    s = sub.add_parser("sheet")
    s.add_argument("clips", nargs="+")
    s.add_argument("-o", "--output", required=True)
    s.add_argument("--frames", type=int, default=3)
    s.add_argument("--width", type=int, default=240)
    for name in ("timing", "overlays", "build"):
        sub.add_parser(name).add_argument("project")
    sub.add_parser("check").add_argument("video")
    a = ap.parse_args()
    for tool in ("ffmpeg", "ffprobe"):
        if not shutil.which(tool):
            die(f"{tool} is not installed")
    {"sheet": cmd_sheet, "timing": cmd_timing, "overlays": cmd_overlays, "build": cmd_build,
     "check": cmd_check}[a.cmd](a)


if __name__ == "__main__":
    main()
