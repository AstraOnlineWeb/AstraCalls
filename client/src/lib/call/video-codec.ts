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
  private forceKey = false;

  /** Força o próximo frame a ser keyframe (um participante pediu PLI/FIR). */
  forceKeyframe(): void {
    this.forceKey = true;
  }

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
        const keyFrame = this.forceKey || this.frameCount % VIDEO_KEYFRAME_INTERVAL === 0;
        this.forceKey = false;
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
  private closed = false;
  readonly stream: MediaStream;

  /**
   * Chamado quando o decoder PRECISA de um keyframe pra (re)começar: ainda não
   * abriu (só chegam deltas) ou deu erro de decodificação (frame corrompido por
   * perda). Quem recebe pede um PLI ao participante.
   */
  onNeedKeyframe: (() => void) | null = null;

  constructor() {
    const generator = new MediaStreamTrackGenerator({ kind: "video" });
    this.writer = generator.writable.getWriter();
    this.stream = new MediaStream([generator]);
    this.decoder = this.newDecoder();
  }

  private newDecoder(): VideoDecoder {
    const decoder = new VideoDecoder({
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
      error: (e) => {
        console.error("video decoder error", e);
        this.recover();
      },
    });
    decoder.configure({ codec: VIDEO_CODEC, optimizeForLatency: true });
    return decoder;
  }

  /** Recria o decoder e volta a esperar keyframe (pede um). */
  private recover(): void {
    if (this.closed) return;
    try {
      this.decoder.close();
    } catch {}
    this.decoder = this.newDecoder();
    this.started = false;
    this.onNeedKeyframe?.();
  }

  decode(data: ArrayBuffer): void {
    if (this.closed) return;
    const bytes = new Uint8Array(data);
    const key = isAnnexBKeyframe(bytes);
    if (!this.started && !key) {
      this.onNeedKeyframe?.();
      return;
    }
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
      this.recover();
    }
  }

  close(): void {
    this.closed = true;
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
