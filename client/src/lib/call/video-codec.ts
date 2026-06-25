import {
  VIDEO_BITRATE,
  VIDEO_CODEC,
  VIDEO_FPS,
  VIDEO_HEIGHT,
  VIDEO_KEYFRAME_INTERVAL,
  VIDEO_WIDTH,
} from "@/constants/video";

export const videoSupported = (): boolean =>
  typeof window !== "undefined" &&
  "VideoEncoder" in window &&
  "VideoDecoder" in window &&
  "MediaStreamTrackProcessor" in window &&
  "MediaStreamTrackGenerator" in window;

export class VideoSender {
  private encoder: VideoEncoder;
  private reader: ReadableStreamDefaultReader<VideoFrame>;
  private frameCount = 0;
  private closed = false;

  constructor(track: MediaStreamTrack, send: (au: ArrayBuffer) => void) {
    this.encoder = new VideoEncoder({
      output: (chunk) => {
        const buf = new Uint8Array(chunk.byteLength);
        chunk.copyTo(buf);
        send(buf.buffer);
      },
      error: (e) => console.error("video encoder error", e),
    });
    this.encoder.configure({
      codec: VIDEO_CODEC,
      width: VIDEO_WIDTH,
      height: VIDEO_HEIGHT,
      bitrate: VIDEO_BITRATE,
      framerate: VIDEO_FPS,
      latencyMode: "realtime",
      avc: { format: "annexb" },
    });
    const processor = new MediaStreamTrackProcessor({ track });
    this.reader = processor.readable.getReader();
    void this.pump();
  }

  private async pump(): Promise<void> {
    while (!this.closed) {
      const { value: frame, done } = await this.reader.read();
      if (done || !frame) break;
      if (this.encoder.encodeQueueSize < 2) {
        const keyFrame = this.frameCount % VIDEO_KEYFRAME_INTERVAL === 0;
        this.encoder.encode(frame, { keyFrame });
        this.frameCount += 1;
      }
      frame.close();
    }
  }

  close(): void {
    this.closed = true;
    try {
      void this.reader.cancel();
    } catch {}
    try {
      this.encoder.close();
    } catch {}
  }
}

export class VideoReceiver {
  private decoder: VideoDecoder;
  private writer: WritableStreamDefaultWriter<VideoFrame>;
  private ts = 0;
  private started = false;
  private writing = false;
  readonly stream: MediaStream;

  constructor() {
    const generator = new MediaStreamTrackGenerator({ kind: "video" });
    this.writer = generator.writable.getWriter();
    this.stream = new MediaStream([generator]);
    this.decoder = new VideoDecoder({
      output: (frame) => {
        if (this.writing) {
          frame.close();
          return;
        }
        this.writing = true;
        this.writer
          .write(frame)
          .catch(() => frame.close())
          .finally(() => {
            this.writing = false;
          });
      },
      error: (e) => console.error("video decoder error", e),
    });
    this.decoder.configure({ codec: VIDEO_CODEC, optimizeForLatency: true });
  }

  decode(data: ArrayBuffer): void {
    const bytes = new Uint8Array(data);
    const key = isAnnexBKeyframe(bytes);
    if (!this.started && !key) return;
    this.started = true;
    const chunk = new EncodedVideoChunk({
      type: key ? "key" : "delta",
      timestamp: this.ts,
      data: bytes,
    });
    this.ts += 1_000_000 / VIDEO_FPS;
    try {
      this.decoder.decode(chunk);
    } catch (e) {
      console.error("video decode error", e);
    }
  }

  close(): void {
    try {
      this.decoder.close();
    } catch {}
    try {
      void this.writer.close();
    } catch {}
  }
}

function isAnnexBKeyframe(b: Uint8Array): boolean {
  for (let i = 0; i + 4 < b.length; i += 1) {
    if (b[i] !== 0 || b[i + 1] !== 0) continue;
    let sc = 0;
    if (b[i + 2] === 1) sc = 3;
    else if (b[i + 2] === 0 && b[i + 3] === 1) sc = 4;
    if (sc === 0) continue;
    const t = b[i + sc] & 0x1f;
    if (t === 5 || t === 7) return true;
    i += sc;
  }
  return false;
}
