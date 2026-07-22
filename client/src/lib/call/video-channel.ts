import { H264_CHANNEL_LABEL } from "@/constants/video";
import { VideoReceiver, VideoSender, videoSupported } from "./video-codec";

export type VideoChannel = {
  // Presente quando o navegador suporta WebCodecs: renderiza o vídeo do peer.
  remoteVideoStream: MediaStream | null;
  // Liga/desliga o envio da nossa câmera sob demanda (upgrade/downgrade mid-call).
  startSender: (track: MediaStreamTrack) => void;
  stopSender: () => void;
  close: () => void;
};

const noVideoChannel: VideoChannel = {
  remoteVideoStream: null,
  startSender: () => {},
  stopSender: () => {},
  close: () => {},
};

// setupVideoChannel abre o datachannel "h264" e o receptor — mesmo em chamadas que
// começam só de áudio — para que um upgrade mid-call não precise renegociar SDP. O
// envio da câmera fica lazy (startSender). Espelha o backend, que mantém a pipeline
// de vídeo keyada em toda chamada. IMPORTANTE: se o navegador não tiver WebCodecs, ou
// algo falhar aqui, NUNCA deve derrubar o áudio — cai no canal no-op e a call segue.
export const setupVideoChannel = (pc: RTCPeerConnection): VideoChannel => {
  if (!videoSupported()) return noVideoChannel;
  try {
    return buildVideoChannel(pc);
  } catch (e) {
    console.warn("video channel setup failed; audio-only call", e);
    return noVideoChannel;
  }
};

const buildVideoChannel = (pc: RTCPeerConnection): VideoChannel => {
  const receiver = new VideoReceiver();
  const videoDc = pc.createDataChannel(H264_CHANNEL_LABEL, {
    ordered: false,
    maxRetransmits: 0,
  });
  videoDc.binaryType = "arraybuffer";
  videoDc.onmessage = (e: MessageEvent<ArrayBuffer>) => receiver.decode(e.data);

  let sender: VideoSender | null = null;
  let pending: MediaStreamTrack | null = null;
  let open = false;

  const startNow = (track: MediaStreamTrack) => {
    sender?.close();
    sender = new VideoSender(track, (au) => {
      if (videoDc.readyState === "open") videoDc.send(au);
    });
  };

  videoDc.onopen = () => {
    open = true;
    if (pending) {
      startNow(pending);
      pending = null;
    }
  };

  return {
    remoteVideoStream: receiver.stream,
    startSender: (track) => {
      if (open) startNow(track);
      else pending = track; // envia assim que o canal abrir
    },
    stopSender: () => {
      try {
        sender?.close();
      } catch {}
      sender = null;
      pending = null;
    },
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
