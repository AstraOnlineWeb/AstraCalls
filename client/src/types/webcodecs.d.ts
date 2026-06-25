declare class MediaStreamTrackProcessor<T = VideoFrame> {
  constructor(init: { track: MediaStreamTrack });
  readonly readable: ReadableStream<T>;
}

declare class MediaStreamTrackGenerator<T = VideoFrame> extends MediaStreamTrack {
  constructor(init: { kind: string });
  readonly writable: WritableStream<T>;
}
