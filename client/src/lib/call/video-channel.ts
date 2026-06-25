import { H264_CHANNEL_LABEL } from "@/constants/video";
import { VideoReceiver, VideoSender } from "./video-codec";

export type VideoChannel = {
  localVideoStream: MediaStream | null;
  remoteVideoStream: MediaStream | null;
  close: () => void;
};

const noVideoChannel: VideoChannel = {
  localVideoStream: null,
  remoteVideoStream: null,
  close: () => {},
};

export const setupVideoChannel = (
  pc: RTCPeerConnection,
  localStream: MediaStream,
  wantVideo: boolean,
): VideoChannel => {
  if (!wantVideo) return noVideoChannel;
  const camTrack = localStream.getVideoTracks()[0];
  if (!camTrack) return noVideoChannel;

  const localVideoStream = new MediaStream([camTrack]);
  const videoDc = pc.createDataChannel(H264_CHANNEL_LABEL, {
    ordered: false,
    maxRetransmits: 0,
  });
  videoDc.binaryType = "arraybuffer";

  const receiver = new VideoReceiver();
  let sender: VideoSender | null = null;
  videoDc.onmessage = (e: MessageEvent<ArrayBuffer>) => receiver.decode(e.data);
  videoDc.onopen = () => {
    sender = new VideoSender(camTrack, (au) => {
      if (videoDc.readyState === "open") videoDc.send(au);
    });
  };

  return {
    localVideoStream,
    remoteVideoStream: receiver.stream,
    close: () => {
      try {
        sender?.close();
      } catch {}
      try {
        receiver.close();
      } catch {}
    },
  };
};
