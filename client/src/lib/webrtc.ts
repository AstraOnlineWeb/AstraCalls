import { apiPost } from "./api";
import { setupVideoChannel } from "./call/video-channel";
import { videoSupported } from "./call/video-codec";
import { VIDEO_FPS, VIDEO_HEIGHT, VIDEO_WIDTH } from "../constants/video";
import { getTransportMode } from "./transport";
import { openWSCall } from "./ws-audio";

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
  // Liga/desliga a câmera no meio da chamada (upgrade/downgrade). Resolve para
  // true se o vídeo ficou ligado, false caso contrário (ex.: WebCodecs ausente).
  setLocalVideo: (on: boolean, camDeviceId?: string | null) => Promise<boolean>;
  close: () => void;
};

const cameraConstraints = (camDeviceId?: string | null): MediaTrackConstraints => ({
  deviceId: camDeviceId ? { exact: camDeviceId } : undefined,
  width: { ideal: VIDEO_WIDTH },
  height: { ideal: VIDEO_HEIGHT },
  frameRate: { ideal: VIDEO_FPS },
});

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
    video: wantVideo ? cameraConstraints(opts.camDeviceId) : false,
  });
  const pc = new RTCPeerConnection({ iceServers: [] });

  // Áudio: uma track RTP Opus (nosso transporte). Só a track de áudio entra no
  // peer connection; a câmera é tratada pelo canal de vídeo (WebCodecs) abaixo.
  localStream.getAudioTracks().forEach((t) => pc.addTrack(t, localStream));
  pc.addTransceiver("audio", { direction: "recvonly" });
  const remoteHolder: { stream: MediaStream | null } = { stream: null };
  pc.ontrack = (ev) => {
    if (ev.streams[0]) remoteHolder.stream = ev.streams[0];
  };

  // Vídeo: H264 sobre datachannel out-of-order, SEMPRE aberto (mesmo em chamada
  // de áudio) para permitir upgrade mid-call sem renegociar SDP.
  const video = setupVideoChannel(pc);

  // Estado da câmera local. Começa ligada só se a chamada nasceu em vídeo.
  let camTrack: MediaStreamTrack | null = wantVideo ? localStream.getVideoTracks()[0] ?? null : null;
  const local: { stream: MediaStream | null } = { stream: null };
  if (camTrack) {
    local.stream = new MediaStream([camTrack]);
    video.startSender(camTrack);
  }

  const setLocalVideo = async (on: boolean, camDeviceId?: string | null): Promise<boolean> => {
    if (on) {
      if (camTrack) return true; // já ligada
      if (!videoSupported()) return false;
      const cam = await navigator.mediaDevices.getUserMedia({ video: cameraConstraints(camDeviceId) });
      camTrack = cam.getVideoTracks()[0] ?? null;
      if (!camTrack) return false;
      local.stream = new MediaStream([camTrack]);
      video.startSender(camTrack);
      return true;
    }
    video.stopSender();
    if (camTrack) {
      try {
        camTrack.stop();
      } catch {}
    }
    camTrack = null;
    local.stream = null;
    return false;
  };

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
    get localVideoStream() {
      return local.stream;
    },
    remoteVideoStream: video.remoteVideoStream,
    setLocalVideo,
    close: () => {
      video.close();
      try {
        localStream.getTracks().forEach((t) => t.stop());
      } catch {}
      try {
        if (camTrack) camTrack.stop();
      } catch {}
      try {
        pc.close();
      } catch {}
    },
  } as OpenCall;
};

// =============================================================================
// Transporte adaptativo — escolhe WebRTC ou WebSocket conforme transport.ts
// =============================================================================

// Tempo (ms) que esperamos o ICE do WebRTC conectar antes de cair pro WebSocket
// no modo "auto". Curto o suficiente pra não deixar o usuário no vácuo, folgado
// o suficiente pra redes lentas fecharem o ICE.
const ICE_FALLBACK_MS = 6000;

// waitIceConnected resolve true quando o PeerConnection conecta o ICE, e false
// se falhar/fechar ou estourar o timeout (aí o chamador cai pro WebSocket).
const waitIceConnected = (pc: RTCPeerConnection, timeoutMs: number): Promise<boolean> =>
  new Promise((resolve) => {
    let settled = false;
    const finish = (v: boolean) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      pc.removeEventListener("iceconnectionstatechange", onChange);
      resolve(v);
    };
    const onChange = () => {
      const s = pc.iceConnectionState;
      if (s === "connected" || s === "completed") finish(true);
      else if (s === "failed" || s === "closed") finish(false);
      // "disconnected"/"checking": transitório — aguarda timeout
    };
    const timer = setTimeout(() => finish(false), timeoutMs);
    pc.addEventListener("iceconnectionstatechange", onChange);
    onChange(); // caso já tenha conectado antes do listener
  });

// wsAdapter embrulha openWSCall no formato OpenCall (campos de vídeo nulos).
const wsAdapter = async (
  sid: string,
  callId: string,
  micDeviceId: string | null,
): Promise<OpenCall> => {
  const wsConn = await openWSCall(sid, callId, micDeviceId);
  return {
    pc: null as unknown as RTCPeerConnection, // não usado fora do webrtc.ts
    micStream: wsConn.micStream,
    get remoteStream() {
      return wsConn.remoteStream;
    },
    localVideoStream: null,
    remoteVideoStream: null,
    setLocalVideo: wsConn.setLocalVideo,
    close: wsConn.close,
  };
};

/**
 * openAdaptiveCall — ponto de entrada das chamadas. Consulta getTransportMode():
 *  - "webrtc"    (forçado): só WebRTC (áudio + vídeo), sem fallback.
 *  - "websocket" (forçado): só WebSocket (áudio-only).
 *  - "auto"      (padrão): tenta WebRTC; se o ICE não conectar em ICE_FALLBACK_MS
 *    (proxy/firewall bloqueando UDP), fecha e cai pro WebSocket — sem abrir porta
 *    nem intervenção do operador. Mantém vídeo quando o WebRTC funciona.
 *
 * A interface de retorno é compatível com OpenCall em todos os campos de áudio.
 */
export const openAdaptiveCall = async (
  sid: string,
  callId: string,
  micDeviceId: string | null,
  opts: OpenCallOptions = {},
): Promise<OpenCall> => {
  const mode = getTransportMode();

  if (mode === "websocket") {
    if (opts.video) {
      console.warn("[transport] modo WebSocket: vídeo indisponível, seguindo em áudio-only");
    }
    return wsAdapter(sid, callId, micDeviceId);
  }

  if (mode === "webrtc") {
    return openCall(sid, callId, micDeviceId, opts);
  }

  // mode === "auto": tenta WebRTC e cai pro WebSocket se o ICE não conectar.
  // Trocar o transporte não derruba a chamada no WhatsApp: o servidor substitui
  // a ponte (setWSBridge faz DisableTerminate na ponte WebRTC antiga).
  try {
    const call = await openCall(sid, callId, micDeviceId, opts);
    if (await waitIceConnected(call.pc, ICE_FALLBACK_MS)) {
      return call;
    }
    console.warn(
      `[transport] WebRTC não conectou (ICE) em ${ICE_FALLBACK_MS}ms — caindo para WebSocket (áudio-only)`,
    );
    try {
      call.close();
    } catch {
      /* ignore */
    }
  } catch (err) {
    console.warn("[transport] WebRTC falhou ao abrir — caindo para WebSocket (áudio-only)", err);
  }
  return wsAdapter(sid, callId, micDeviceId);
};
