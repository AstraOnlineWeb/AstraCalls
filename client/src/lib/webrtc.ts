import { apiPost } from "./api";
import { VideoReceiver, VideoSender, videoSupported } from "./video-codec";
import {
  H264_CHANNEL_LABEL,
  VIDEO_FPS,
  VIDEO_HEIGHT,
  VIDEO_WIDTH,
} from "../constants/video";

export type OpenCallOptions = {
  video?: boolean;
  camDeviceId?: string | null;
};

export type OpenCall = {
  pc: RTCPeerConnection;
  micStream: MediaStream;
  remoteStream: MediaStream | null;
  localVideoStream: MediaStream | null;
  remoteVideoStream: MediaStream | null;
  close: () => void;
};

export const openCall = async (
  sid: string,
  callId: string,
  micDeviceId: string | null,
  opts: OpenCallOptions = {},
): Promise<OpenCall> => {
  const wantVideo = !!opts.video && videoSupported();
  if (opts.video && !wantVideo) {
    console.warn("video requested but WebCodecs/insertable-streams unsupported; audio only");
  }

  const localStream = await navigator.mediaDevices.getUserMedia({
    audio: micDeviceId ? { deviceId: { exact: micDeviceId } } : true,
    video: wantVideo
      ? {
          deviceId: opts.camDeviceId ? { exact: opts.camDeviceId } : undefined,
          width: { ideal: VIDEO_WIDTH },
          height: { ideal: VIDEO_HEIGHT },
          frameRate: { ideal: VIDEO_FPS },
        }
      : false,
  });
  const pc = new RTCPeerConnection({ iceServers: [] });

  // Audio: carried over an Opus RTP media track (our transport). Only the audio
  // track is added to the peer connection; the camera track (if any) is encoded
  // with WebCodecs and sent over the h264 data channel below, not as RTP.
  localStream.getAudioTracks().forEach((t) => pc.addTrack(t, localStream));
  pc.addTransceiver("audio", { direction: "recvonly" });
  const remoteHolder: { stream: MediaStream | null } = { stream: null };
  pc.ontrack = (ev) => {
    if (ev.streams[0]) remoteHolder.stream = ev.streams[0];
  };

  // Video: H264 access units over an out-of-order data channel, encoded/decoded
  // with WebCodecs on the browser side.
  let videoSender: VideoSender | null = null;
  let videoReceiver: VideoReceiver | null = null;
  let localVideoStream: MediaStream | null = null;
  let remoteVideoStream: MediaStream | null = null;
  if (wantVideo) {
    const camTrack = localStream.getVideoTracks()[0];
    if (camTrack) {
      localVideoStream = new MediaStream([camTrack]);
      const videoDc = pc.createDataChannel(H264_CHANNEL_LABEL, { ordered: false, maxRetransmits: 0 });
      videoDc.binaryType = "arraybuffer";
      videoReceiver = new VideoReceiver();
      remoteVideoStream = videoReceiver.stream;
      videoDc.onmessage = (e: MessageEvent<ArrayBuffer>) => videoReceiver?.decode(e.data);
      videoDc.onopen = () => {
        videoSender = new VideoSender(camTrack, (au) => {
          if (videoDc.readyState === "open") videoDc.send(au);
        });
      };
    }
  }

  const offer = await pc.createOffer();
  await pc.setLocalDescription(offer);
  await new Promise<void>((resolve) => {
    if (pc.iceGatheringState === "complete") resolve();
    else
      pc.addEventListener("icegatheringstatechange", () => {
        if (pc.iceGatheringState === "complete") resolve();
      });
  });
  const { sdp_answer } = await apiPost<{ sdp_answer: string }>(
    `/api/sessions/${sid}/calls/${callId}/webrtc`,
    { sdp_offer: pc.localDescription!.sdp },
  );
  await pc.setRemoteDescription({ type: "answer", sdp: sdp_answer });
  return {
    pc,
    micStream: localStream,
    get remoteStream() {
      return remoteHolder.stream;
    },
    localVideoStream,
    remoteVideoStream,
    close: () => {
      try {
        videoSender?.close();
      } catch {}
      try {
        videoReceiver?.close();
      } catch {}
      try {
        localStream.getTracks().forEach((t) => t.stop());
      } catch {}
      try {
        pc.close();
      } catch {}
    },
  } as OpenCall;
};
