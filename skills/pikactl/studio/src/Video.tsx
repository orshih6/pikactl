import { loadFont as loadInter } from "@remotion/google-fonts/Inter";
import { loadFont as loadMontserrat } from "@remotion/google-fonts/Montserrat";
import { loadFont as loadRoboto } from "@remotion/google-fonts/Roboto";
import { loadFont as loadRubik } from "@remotion/google-fonts/Rubik";
import { Audio, Video as Clip } from "@remotion/media";
import { AbsoluteFill, Img, interpolate, Sequence, spring, staticFile, useCurrentFrame, useVideoConfig } from "remotion";
import type { Caption, Scene, Spec, Word } from "./types";

const subsets = ["latin", "cyrillic", "cyrillic-ext"] as const;
const LOADERS = {
  Montserrat: () => loadMontserrat("normal", { weights: ["600", "800"], subsets: [...subsets] }).fontFamily,
  Inter: () => loadInter("normal", { weights: ["600", "800"], subsets: [...subsets] }).fontFamily,
  Roboto: () => loadRoboto("normal", { weights: ["500", "700"], subsets: [...subsets] }).fontFamily,
  Rubik: () => loadRubik("normal", { weights: ["600", "800"], subsets: [...subsets] }).fontFamily,
};
const fonts: Partial<Record<keyof typeof LOADERS, string>> = {};
const fontFamily = (name: keyof typeof LOADERS) => (fonts[name] ??= LOADERS[name]());

const clamp = { extrapolateLeft: "clamp", extrapolateRight: "clamp" } as const;
const HOT = "#FF3B5C";
const SHADOW = "0 0 2px rgba(0,0,0,.9), 0 4px 18px rgba(0,0,0,.85), 0 0 40px rgba(0,0,0,.6)";

// --- pictures --------------------------------------------------------------------------------

const Picture: React.FC<{ scene: Scene; frames: number; fit: "cover" | "contain" }> = ({ scene, frames, fit }) => {
  const frame = useCurrentFrame();
  const { fps } = useVideoConfig();
  const [z0, z1] = scene.zoom ?? [1, 1.06];
  const scale = fit === "contain" ? 1 : interpolate(frame, [0, frames], [z0, z1], clamp);
  const fx = (scene.focus?.x ?? 0.5) * 100;
  const fy = (scene.focus?.y ?? 0.5) * 100;
  const style: React.CSSProperties = {
    width: "100%",
    height: "100%",
    objectFit: fit,
    objectPosition: `${fx}% ${fy}%`,
    transform: `scale(${scale})`,
    transformOrigin: `${fx}% ${fy}%`,
  };
  if (!scene.src) return null;
  if (scene.type === "video") {
    return (
      <Clip
        src={staticFile(scene.src)}
        muted={!scene.volume}
        volume={scene.volume ?? 0}
        playbackRate={scene.rate ?? 1}
        trimBefore={Math.round((scene.start ?? 0) * fps)}
        objectFit={fit}
        style={style}
      />
    );
  }
  return <Img src={staticFile(scene.src)} style={style} />;
};

const Media: React.FC<{ scene: Scene; frames: number }> = ({ scene, frames }) =>
  scene.fit === "blur" ? (
    <>
      <AbsoluteFill style={{ filter: "blur(40px) brightness(0.55)", transform: "scale(1.15)" }}>
        <Picture scene={scene} frames={frames} fit="cover" />
      </AbsoluteFill>
      <AbsoluteFill>
        <Picture scene={scene} frames={frames} fit="contain" />
      </AbsoluteFill>
    </>
  ) : (
    <AbsoluteFill style={{ overflow: "hidden" }}>
      <Picture scene={scene} frames={frames} fit="cover" />
    </AbsoluteFill>
  );

const Backdrop: React.FC<{ scene: Scene; frames: number }> = ({ scene, frames }) =>
  scene.src ? (
    <AbsoluteFill style={{ filter: "blur(36px) brightness(0.45)", transform: "scale(1.15)" }}>
      <Picture scene={{ ...scene, type: /\.(mp4|mov|webm)$/i.test(scene.src) ? "video" : "image" }} frames={frames} fit="cover" />
    </AbsoluteFill>
  ) : (
    <AbsoluteFill style={{ background: scene.background ?? "radial-gradient(circle at 50% 35%, #2a2f45 0%, #0d0f18 70%)" }} />
  );

const Shade: React.FC = () => (
  <AbsoluteFill
    style={{
      background:
        "linear-gradient(to bottom, rgba(0,0,0,.45) 0%, rgba(0,0,0,0) 22%, rgba(0,0,0,0) 45%, rgba(0,0,0,.55) 75%, rgba(0,0,0,.75) 100%)",
    }}
  />
);

// --- phone screen ----------------------------------------------------------------------------

const Screen: React.FC<{ scene: Scene; frames: number; accent: string }> = ({ scene, frames, accent }) => {
  const frame = useCurrentFrame();
  const { fps, width, height } = useVideoConfig();
  const h = Math.min(width * 0.74 * (19.5 / 9), height * 0.74);
  const w = (h * 9) / 19.5;
  const left = (width - w) / 2;
  const top = (height - h) / 2 - height * 0.02;
  const hl = scene.highlight;
  const hat = (hl?.at ?? 0.6) * fps;
  const p = hl ? spring({ frame: frame - hat, fps, config: { damping: 200 }, durationInFrames: 20 }) : 0;
  const cx = hl ? left + (hl.x + hl.w / 2) * w : width / 2;
  const cy = hl ? top + (hl.y + hl.h / 2) * h : height / 2;
  const s = 1 + ((hl?.zoom ?? 1.6) - 1) * p;
  const tap = scene.tap;
  const tk = tap ? frame - tap.at * fps : -1;
  const radius = w * 0.025;
  return (
    <AbsoluteFill>
      <Backdrop scene={{ ...scene, src: scene.src }} frames={frames} />
      <AbsoluteFill
        style={{
          transformOrigin: `${cx}px ${cy}px`,
          transform: `translate(${(width / 2 - cx) * p}px, ${(height * 0.42 - cy) * p}px) scale(${s})`,
        }}
      >
        <div
          style={{
            position: "absolute",
            left: left - radius,
            top: top - radius,
            width: w + 2 * radius,
            height: h + 2 * radius,
            borderRadius: w * 0.13,
            background: "#0b0b0d",
            boxShadow: "0 30px 80px rgba(0,0,0,.6), inset 0 0 0 3px #2b2b30",
          }}
        />
        <div style={{ position: "absolute", left, top, width: w, height: h, borderRadius: w * 0.11, overflow: "hidden" }}>
          {scene.src ? <Img src={staticFile(scene.src)} style={{ width: "100%", height: "100%", objectFit: "cover" }} /> : null}
          {hl ? (
            <div
              style={{
                position: "absolute",
                left: hl.x * w,
                top: hl.y * h,
                width: hl.w * w,
                height: hl.h * h,
                borderRadius: w * 0.03,
                border: `${w * 0.012}px solid ${accent}`,
                boxShadow: `0 0 ${w * 0.05}px ${accent}`,
                opacity: interpolate(frame - hat, [0, 8], [0, 1], clamp),
              }}
            />
          ) : null}
          {tap && tk >= 0 ? (
            <div
              style={{
                position: "absolute",
                left: tap.x * w,
                top: tap.y * h,
                width: 1,
                height: 1,
              }}
            >
              {[0, 7].map((delay) => {
                const k = tk - delay;
                const r = interpolate(k, [0, 18], [w * 0.03, w * 0.13], clamp);
                return k < 0 ? null : (
                  <div
                    key={delay}
                    style={{
                      position: "absolute",
                      left: -r,
                      top: -r,
                      width: 2 * r,
                      height: 2 * r,
                      borderRadius: "50%",
                      border: `${w * 0.01}px solid ${accent}`,
                      boxShadow: "0 0 0 3px rgba(0,0,0,.45)",
                      background: delay === 0 ? "rgba(0,0,0,.28)" : "transparent",
                      opacity: interpolate(k, [0, 18], [0.95, 0], clamp),
                    }}
                  />
                );
              })}
            </div>
          ) : null}
        </div>
      </AbsoluteFill>
    </AbsoluteFill>
  );
};

// --- title and list cards --------------------------------------------------------------------

const Title: React.FC<{ scene: Scene; frames: number; font: string }> = ({ scene, frames, font }) => {
  const frame = useCurrentFrame();
  const { fps, width } = useVideoConfig();
  const pop = spring({ frame, fps, config: { damping: 14 }, durationInFrames: 18 });
  return (
    <AbsoluteFill>
      <Backdrop scene={scene} frames={frames} />
      <AbsoluteFill style={{ justifyContent: "center", alignItems: "center", padding: width * 0.09, textAlign: "center" }}>
        {scene.emoji ? <div style={{ fontSize: width * 0.16, marginBottom: width * 0.03, scale: String(pop) }}>{scene.emoji}</div> : null}
        <div
          style={{
            fontFamily: font,
            fontWeight: 800,
            fontSize: width * 0.088,
            lineHeight: 1.08,
            color: "white",
            textShadow: SHADOW,
            scale: String(0.85 + 0.15 * pop),
            opacity: pop,
          }}
        >
          {scene.title}
        </div>
        {scene.text ? (
          <div
            style={{
              fontFamily: font,
              fontWeight: 600,
              fontSize: width * 0.045,
              lineHeight: 1.3,
              color: "rgba(255,255,255,.9)",
              marginTop: width * 0.035,
              textShadow: SHADOW,
              opacity: interpolate(frame, [8, 18], [0, 1], clamp),
            }}
          >
            {scene.text}
          </div>
        ) : null}
      </AbsoluteFill>
    </AbsoluteFill>
  );
};

const List: React.FC<{ scene: Scene; frames: number; font: string; accent: string }> = ({ scene, frames, font, accent }) => {
  const frame = useCurrentFrame();
  const { fps, width, height } = useVideoConfig();
  const items = scene.items ?? [];
  const times = items.map((_, i) => scene.itemTimes?.[i] ?? 0.4 + (i * (scene.duration - 1.2)) / Math.max(1, items.length));
  return (
    <AbsoluteFill>
      <Backdrop scene={scene} frames={frames} />
      <div style={{ position: "absolute", left: width * 0.08, right: width * 0.08, top: height * 0.22 }}>
        {scene.title ? (
          <div
            style={{
              fontFamily: font,
              fontWeight: 800,
              fontSize: width * 0.07,
              lineHeight: 1.1,
              color: "white",
              textShadow: SHADOW,
              marginBottom: width * 0.06,
            }}
          >
            {scene.title}
          </div>
        ) : null}
        {items.map((item, i) => {
          const k = frame - times[i] * fps;
          if (k < 0) return null;
          const p = spring({ frame: k, fps, config: { damping: 16 }, durationInFrames: 14 });
          return (
            <div
              key={i}
              style={{
                display: "flex",
                alignItems: "center",
                gap: width * 0.035,
                marginBottom: width * 0.04,
                opacity: p,
                transform: `translateX(${(1 - p) * -width * 0.12}px)`,
              }}
            >
              <div
                style={{
                  flex: "none",
                  width: width * 0.1,
                  height: width * 0.1,
                  borderRadius: width * 0.05,
                  background: accent,
                  color: "#111",
                  fontFamily: font,
                  fontWeight: 800,
                  fontSize: width * 0.055,
                  display: "flex",
                  alignItems: "center",
                  justifyContent: "center",
                }}
              >
                {i + 1}
              </div>
              <div style={{ fontFamily: font, fontWeight: 700, fontSize: width * 0.052, lineHeight: 1.2, color: "white", textShadow: SHADOW }}>
                {item}
              </div>
            </div>
          );
        })}
      </div>
    </AbsoluteFill>
  );
};

const Sticker: React.FC<{ emoji: string; card: boolean }> = ({ emoji, card }) => {
  const frame = useCurrentFrame();
  const { width, height } = useVideoConfig();
  if (card) return null; // titles draw their own
  const pop = interpolate(frame, [0, 5, 9], [0, 1.15, 1], clamp);
  const size = width * 0.2;
  return (
    <div
      style={{
        position: "absolute",
        right: width * 0.08,
        top: height * 0.24,
        fontSize: size * 0.85,
        lineHeight: 1,
        scale: String(pop),
        rotate: `${Math.sin(frame / 6) * 6}deg`,
        filter: "drop-shadow(0 12px 0 rgba(0,0,0,.35))",
      }}
    >
      {emoji}
    </div>
  );
};

// --- captions --------------------------------------------------------------------------------

const timedWords = (c: Caption): Word[] => {
  if (c.words?.length) return c.words;
  const parts = c.text.split(/\s+/).filter(Boolean);
  const total = parts.reduce((n, p) => n + p.length + 1, 0);
  let t = c.start;
  return parts.map((p) => {
    const d = ((c.end - c.start) * (p.length + 1)) / total;
    const w = { text: p, start: t, end: t + d };
    t += d;
    return w;
  });
};

type Page = { words: Word[]; start: number };
const paginate = (words: Word[]): Page[] => {
  const pages: Page[] = [];
  let cur: Word[] = [];
  for (const w of words) {
    cur.push(w);
    if (cur.length >= 3 || /[.,!?;:…—]$/.test(w.text)) {
      pages.push({ words: cur, start: cur[0].start });
      cur = [];
    }
  }
  if (cur.length) pages.push({ words: cur, start: cur[0].start });
  return pages;
};

const norm = (s: string) => s.toLowerCase().replace(/[^\p{L}\p{N}%$]/gu, "");

const Captions: React.FC<{ spec: Spec; font: string; accent: string }> = ({ spec, font, accent }) => {
  const frame = useCurrentFrame();
  const { fps, width, height } = useVideoConfig();
  const cap = spec.captions;
  if (!cap || cap.mode === "none") return null;
  const t = frame / fps;
  const c = cap.items.find((x) => t >= x.start && t < x.end);
  if (!c) return null;
  const hook = c.style === "hook";
  const base: React.CSSProperties = {
    position: "absolute",
    left: width * 0.075,
    right: width * 0.075,
    top: height * (cap.top ?? 0.6) - (hook ? height * 0.04 : 0),
    textAlign: "center",
    fontFamily: font,
    fontWeight: 800,
    color: "white",
    lineHeight: 1.12,
    textShadow: SHADOW,
    textTransform: cap.uppercase ? "uppercase" : "none",
  };
  if (cap.mode === "lines") {
    const fadeIn = c.start <= 0.01 ? 1 : interpolate(t, [c.start, c.start + 0.15], [0, 1], clamp);
    const fadeOut = interpolate(t, [c.end - 0.15, c.end], [1, 0], clamp);
    return <div style={{ ...base, fontSize: width * (hook ? 0.081 : 0.061), opacity: Math.min(fadeIn, fadeOut) }}>{c.text}</div>;
  }
  const emph = new Set((cap.emphasis ?? []).map(norm));
  const pages = paginate(timedWords(c));
  const page = pages.filter((p) => p.start <= t || p === pages[0]).pop();
  if (!page) return null;
  return (
    <div style={{ ...base, display: "flex", flexWrap: "wrap", justifyContent: "center", columnGap: width * 0.022 }}>
      {page.words.map((w, i) => {
        const k = (t - w.start) * fps;
        if (k < 0 && !(c.start <= 0.01 && page === pages[0])) return null;
        const big = emph.has(norm(w.text));
        const pop = interpolate(k, [0, 3, 6], [0.7, 1.08, 1], clamp);
        const active = t >= w.start && t < w.end + 0.05;
        return (
          <span
            key={i}
            style={{
              display: "inline-block",
              fontSize: width * (big ? 0.095 : hook ? 0.085 : 0.075),
              color: big ? HOT : active ? accent : "white",
              scale: String(pop),
            }}
          >
            {w.text}
          </span>
        );
      })}
    </div>
  );
};

const Labels: React.FC<{ spec: Spec; font: string }> = ({ spec, font }) => {
  const { width, height } = useVideoConfig();
  const pill: React.CSSProperties = {
    position: "absolute",
    top: height * 0.161,
    fontFamily: font,
    fontWeight: 700,
    fontSize: width * 0.03,
    color: "white",
    padding: `${width * 0.009}px ${width * 0.019}px`,
    borderRadius: width * 0.03,
    background: "rgba(0,0,0,.45)",
  };
  return (
    <>
      {spec.labels?.left ? <div style={{ ...pill, left: width * 0.054 }}>{spec.labels.left}</div> : null}
      {spec.labels?.right ? <div style={{ ...pill, right: width * 0.054 }}>{spec.labels.right}</div> : null}
    </>
  );
};

// --- the video -------------------------------------------------------------------------------

const SceneView: React.FC<{ scene: Scene; frames: number; fade: number; font: string; accent: string }> = ({
  scene,
  frames,
  fade,
  font,
  accent,
}) => {
  const frame = useCurrentFrame();
  const opacity = fade > 0 ? interpolate(frame, [0, fade], [0, 1], clamp) : 1;
  const card = scene.type === "title" || scene.type === "list";
  return (
    <AbsoluteFill style={{ opacity }}>
      {scene.type === "video" || scene.type === "image" ? (
        <>
          <Media scene={scene} frames={frames} />
          <Shade />
        </>
      ) : scene.type === "screen" ? (
        <Screen scene={scene} frames={frames} accent={accent} />
      ) : scene.type === "title" ? (
        <Title scene={scene} frames={frames} font={font} />
      ) : (
        <List scene={scene} frames={frames} font={font} accent={accent} />
      )}
      {scene.emoji ? <Sticker emoji={scene.emoji} card={card} /> : null}
    </AbsoluteFill>
  );
};

export const Video: React.FC<Spec> = (spec) => {
  const { fps } = useVideoConfig();
  const font = fontFamily(spec.font ?? "Montserrat");
  const accent = spec.accent ?? "#FFE11A";
  const xf = Math.round((spec.crossfade ?? 0.4) * fps);
  const talk = (spec.captions?.items ?? []).map((c) => [c.start, c.end] as const);
  // How far (in seconds) a moment is from someone speaking: the music dips near speech.
  const away = (t: number) =>
    talk.reduce((d, [a, b]) => Math.min(d, t < a ? a - t : t > b ? t - b : 0), Infinity);
  const duck = spec.music?.duck ?? 0.35;
  const musicVolume = spec.music?.volume ?? 0.15;
  return (
    <AbsoluteFill style={{ background: "#000" }}>
      {spec.scenes.map((s, i) => {
        const from = Math.round(s.from * fps);
        const last = i === spec.scenes.length - 1;
        const frames = Math.max(1, Math.round(s.duration * fps) + (last ? 0 : xf));
        return (
          <Sequence key={i} name={`${i + 1} ${s.type}`} from={from} durationInFrames={frames}>
            <SceneView scene={s} frames={frames} fade={i === 0 ? 0 : xf} font={font} accent={accent} />
          </Sequence>
        );
      })}
      <Labels spec={spec} font={font} />
      <Captions spec={spec} font={font} accent={accent} />
      {spec.voice ? (
        <Sequence from={Math.round((spec.voice.at ?? 0) * fps)}>
          <Audio src={staticFile(spec.voice.src)} volume={spec.voice.volume ?? 1} />
        </Sequence>
      ) : null}
      {spec.music ? (
        <Audio
          src={staticFile(spec.music.src)}
          trimBefore={Math.round((spec.music.start ?? 0) * fps)}
          loop={spec.music.loop ?? true}
          volume={(f) => {
            const t = f / fps;
            const near = spec.voice ? Math.min(1, away(t) / 0.3) : 1;
            const fadeIn = Math.min(1, t / 1);
            const fadeOut = Math.min(1, Math.max(0, (spec.duration - t) / 1.5));
            return musicVolume * (duck + (1 - duck) * near) * fadeIn * fadeOut;
          }}
        />
      ) : null}
      {(spec.sfx ?? []).map((x, i) => (
        <Sequence key={`sfx${i}`} from={Math.round(x.at * fps)}>
          <Audio src={staticFile(x.src)} volume={x.volume ?? 0.6} />
        </Sequence>
      ))}
    </AbsoluteFill>
  );
};
