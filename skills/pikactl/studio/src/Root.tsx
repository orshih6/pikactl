import { Composition } from "remotion";
import { Video } from "./Video";
import type { Spec } from "./types";

// One composition for every video; its size, speed and length come from the props.
const preview: Spec = {
  width: 1080,
  height: 1920,
  fps: 30,
  duration: 4,
  labels: { left: "studio", right: "preview" },
  captions: { mode: "words", items: [{ text: "Render with --props", start: 0.3, end: 3.5, style: "hook" }] },
  scenes: [{ type: "title", from: 0, duration: 4, title: "pikactl studio", text: "Pass a spec with --props", emoji: "🎬" }],
};

export const RemotionRoot: React.FC = () => (
  <Composition
    id="Video"
    component={Video}
    width={1080}
    height={1920}
    fps={30}
    durationInFrames={120}
    defaultProps={preview}
    calculateMetadata={({ props }) => ({
      width: props.width,
      height: props.height,
      fps: props.fps,
      durationInFrames: Math.ceil(props.duration * props.fps),
    })}
  />
);
