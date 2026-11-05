/**
 * The JSON a video is rendered from (passed with --props). Times are in seconds, positions
 * and sizes inside a picture are fractions (0–1). Media paths are relative to --public-dir.
 */

export type Word = { text: string; start: number; end: number };

export type Caption = {
  text: string;
  start: number;
  end: number;
  /** Word timings, for "words" captions. Without them the caption's time is shared out by length. */
  words?: Word[];
  /** "hook" draws the first caption bigger. */
  style?: "hook" | "line";
};

export type Box = { x: number; y: number; w: number; h: number };

export type Scene = {
  type: "video" | "image" | "screen" | "title" | "list";
  from: number;
  duration: number;
  /** video, image, screen: the file. title, list: an optional picture blurred behind. */
  src?: string;
  /** video: seconds into the clip to start from. */
  start?: number;
  /** video: playback speed; 0.8 slows it down to fill a longer scene. */
  rate?: number;
  /** video: its own sound, 0–1 (default 0, muted). */
  volume?: number;
  /** cover (default) fills the frame and crops; blur shows the whole picture over a blurred copy. */
  fit?: "cover" | "blur";
  /** Scale at the start and the end of the scene (default [1, 1.06]: a slow push in). */
  zoom?: [number, number];
  /** The part of the picture to keep in frame and zoom towards (default the centre). */
  focus?: { x: number; y: number };
  /** screen: a box to draw round part of the screenshot, from `at` seconds; the camera moves in on it. */
  highlight?: Box & { at?: number; zoom?: number };
  /** screen: a tap ripple at a point of the screenshot. */
  tap?: { x: number; y: number; at: number };
  /** title: the big line. list: the heading. */
  title?: string;
  /** title: a smaller line under it. */
  text?: string;
  /** list: the items, shown one after another across the scene (or at itemTimes). */
  items?: string[];
  itemTimes?: number[];
  /** title, list: a CSS background when there is no src (default a dark gradient). */
  background?: string;
  /** A big emoji sticker. */
  emoji?: string;
};

export type Spec = {
  width: number;
  height: number;
  fps: number;
  duration: number;
  /** Montserrat (default), Inter, Roboto or Rubik; all carry Latin, Cyrillic and extended Cyrillic. */
  font?: "Montserrat" | "Inter" | "Roboto" | "Rubik";
  /** Highlight colour: the active caption word, list numbers, the highlight box. */
  accent?: string;
  /** Seconds each scene fades into the next; 0 for straight cuts. */
  crossfade?: number;
  labels?: { left?: string; right?: string };
  captions?: {
    mode: "words" | "lines" | "none";
    items: Caption[];
    /** Words drawn bigger in a second colour (any case). */
    emphasis?: string[];
    /** Top of the caption block, as a fraction of the height (default 0.6). */
    top?: number;
    uppercase?: boolean;
  };
  voice?: { src: string; at?: number; volume?: number };
  /** duck: the music's volume factor while the voice speaks (default 0.35). start: seconds into the track. */
  music?: { src: string; volume?: number; duck?: number; start?: number; loop?: boolean };
  sfx?: { src: string; at: number; volume?: number }[];
  scenes: Scene[];
};
