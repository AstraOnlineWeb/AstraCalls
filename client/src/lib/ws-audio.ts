/**
 * ws-audio.ts — Transporte de mídia via WebSocket.
 *
 * Substitui o RTCPeerConnection quando o ambiente usa proxy reverso que bloqueia
 * UDP (ex.: Cloudflare orange cloud). Funciona em WSS/443 que passa por qualquer
 * proxy HTTP. Seleção via transport.ts (opt-in explícito).
 *
 * Protocolo:
 *   - Uplink   (mic → servidor): frames binários PCM Int16 LE, 16 kHz, mono
 *   - Downlink (servidor → speaker): mesma codificação
 *
 * Uso:
 *   const bridge = new WSAudioBridge(sid, callId);
 *   await bridge.connect();
 *   bridge.disconnect(); // ao encerrar chamada
 */

import { getApiBase, getApiKey } from "./auth";

const SAMPLE_RATE = 16_000; // Hz — deve bater com o servidor Go
// bufferSize do ScriptProcessorNode: DEVE ser potência de 2 (256/512/1024…).
// 512 amostras a 16 kHz ≈ 32 ms por frame — bom equilíbrio latência × overhead.
const FRAME_SAMPLES = 512;
// Cushion inicial do jitter buffer de playback (segundos). Segura contra rede
// irregular sem adicionar latência perceptível.
const JITTER_CUSHION = 0.06; // 60 ms

export type WSAudioEvents = {
  onState?: (state: "connecting" | "connected" | "disconnected") => void;
  onError?: (err: Error) => void;
};

export class WSAudioBridge {
  /** Stream de microfone — disponível após connect() para o VU meter */
  micStream: MediaStream | null = null;

  /** Stream de áudio remoto — disponível após connect() para o VU meter */
  remoteStream: MediaStream | null = null;

  private ws: WebSocket | null = null;
  private audioCtx: AudioContext | null = null;
  private processorNode: ScriptProcessorNode | null = null;
  private sourceNode: MediaStreamAudioSourceNode | null = null;
  private playbackCtx: AudioContext | null = null;

  // Cursor de agendamento do playback (jitter buffer): próximo instante livre
  // na timeline do AudioContext de reprodução.
  private playCursor = 0;

  // Para o VU meter do peer (criamos uma MediaStream sintética)
  private remoteDestination: MediaStreamAudioDestinationNode | null = null;

  private events: WSAudioEvents;
  private micDeviceId: string | null;

  constructor(
    private sid: string,
    private callId: string,
    micDeviceId: string | null = null,
    events: WSAudioEvents = {},
  ) {
    this.micDeviceId = micDeviceId;
    this.events = events;
  }

  async connect(): Promise<void> {
    this.events.onState?.("connecting");

    // Solicita microfone
    this.micStream = await navigator.mediaDevices.getUserMedia({
      audio: this.micDeviceId
        ? { deviceId: { exact: this.micDeviceId }, sampleRate: SAMPLE_RATE, channelCount: 1,
            echoCancellation: true, noiseSuppression: true, autoGainControl: true }
        : { sampleRate: SAMPLE_RATE, channelCount: 1,
            echoCancellation: true, noiseSuppression: true, autoGainControl: true },
    });

    // Monta a URL WebSocket a partir da URL da API (http→ws, https→wss)
    const base = getApiBase();
    const wsBase = base
      .replace(/^https:\/\//, "wss://")
      .replace(/^http:\/\//, "ws://");
    const apiKey = getApiKey();
    const url =
      `${wsBase}/api/sessions/${this.sid}/calls/${this.callId}/ws` +
      (apiKey ? `?apiKey=${encodeURIComponent(apiKey)}` : "");

    this.ws = new WebSocket(url, ["pcm16"]);
    this.ws.binaryType = "arraybuffer";

    await new Promise<void>((resolve, reject) => {
      const ws = this.ws!;
      ws.onopen = () => {
        this.events.onState?.("connected");
        resolve();
      };
      ws.onerror = () => {
        this.events.onError?.(new Error("WebSocket connection failed"));
        reject(new Error("WebSocket connection failed"));
      };
      ws.onclose = () => {
        this.events.onState?.("disconnected");
        this.stopCapture();
      };
      ws.onmessage = (ev) => {
        if (ev.data instanceof ArrayBuffer) {
          this.playPCM(ev.data);
        }
        // mensagens texto (controle) são ignoradas aqui
      };
    });

    this.startCapture();
  }

  private startCapture(): void {
    if (!this.micStream || !this.ws) return;

    // AudioContext a 16 kHz — reduz o trabalho de resampling no servidor
    this.audioCtx = new AudioContext({ sampleRate: SAMPLE_RATE });
    this.sourceNode = this.audioCtx.createMediaStreamSource(this.micStream);

    // ScriptProcessorNode — coleta FRAME_SAMPLES por callback.
    // Nota: a API é deprecated mas funciona em todos os browsers atuais.
    // AudioWorklet é o sucessor correto, mas exige Worker e ficheiro separado.
    this.processorNode = this.audioCtx.createScriptProcessor(FRAME_SAMPLES, 1, 1);
    this.processorNode.onaudioprocess = (ev) => {
      if (this.ws?.readyState !== WebSocket.OPEN) return;
      const float32 = ev.inputBuffer.getChannelData(0);
      this.ws.send(float32ToInt16LE(float32).buffer);
    };

    this.sourceNode.connect(this.processorNode);
    // Necessário conectar ao destination para o ScriptProcessor funcionar
    this.processorNode.connect(this.audioCtx.destination);
  }

  private stopCapture(): void {
    this.processorNode?.disconnect();
    this.sourceNode?.disconnect();
    this.audioCtx?.close().catch(() => {});
    this.audioCtx = null;
    this.processorNode = null;
    this.sourceNode = null;
    this.micStream?.getTracks().forEach((t) => t.stop());
    this.micStream = null;
  }

  private playPCM(buffer: ArrayBuffer): void {
    // Lazy-init do AudioContext de reprodução
    if (!this.playbackCtx) {
      this.playbackCtx = new AudioContext({ sampleRate: SAMPLE_RATE });
      // Cria um destino MediaStream para o VU meter do peer
      this.remoteDestination = this.playbackCtx.createMediaStreamDestination();
      this.remoteStream = this.remoteDestination.stream;
    }
    const ctx = this.playbackCtx;

    // PCM Int16 LE → Float32
    const int16 = new Int16Array(buffer);
    if (int16.length === 0) return;
    const float32 = new Float32Array(int16.length);
    for (let i = 0; i < int16.length; i++) {
      float32[i] = int16[i] / 32768;
    }

    const audioBuffer = ctx.createBuffer(1, float32.length, SAMPLE_RATE);
    audioBuffer.copyToChannel(float32, 0);

    const src = ctx.createBufferSource();
    src.buffer = audioBuffer;
    src.connect(ctx.destination);
    if (this.remoteDestination) {
      src.connect(this.remoteDestination);
    }

    // Jitter buffer: agenda cada chunk sequencialmente numa timeline contínua em
    // vez de tocar imediatamente. Se o cursor atrasou (rede engasgou), reancora
    // com uma pequena folga; senão, encadeia sem gaps nem sobreposição.
    const now = ctx.currentTime;
    if (this.playCursor < now + 0.005) {
      this.playCursor = now + JITTER_CUSHION;
    }
    src.start(this.playCursor);
    this.playCursor += audioBuffer.duration;
  }

  /** Silencia/retoma o microfone (hold). */
  setMicEnabled(enabled: boolean): void {
    this.micStream?.getAudioTracks().forEach((t) => {
      t.enabled = enabled;
    });
  }

  disconnect(): void {
    this.stopCapture();
    this.playbackCtx?.close().catch(() => {});
    this.playbackCtx = null;
    this.playCursor = 0;
    if (this.ws && this.ws.readyState === WebSocket.OPEN) {
      this.ws.close(1000, "call ended");
    }
    this.ws = null;
  }
}

/** Converte Float32 [-1, 1] → Int16 LE [-32768, 32767] */
function float32ToInt16LE(input: Float32Array): Int16Array {
  const out = new Int16Array(input.length);
  for (let i = 0; i < input.length; i++) {
    const s = Math.max(-1, Math.min(1, input[i]));
    out[i] = s < 0 ? s * 32768 : s * 32767;
  }
  return out;
}

/**
 * openWSCall — interface equivalente a openCall() de webrtc.ts.
 * Retorna um objeto compatível (campos de vídeo nulos — WS é áudio-only).
 */
export type OpenWSCall = {
  micStream: MediaStream;
  remoteStream: MediaStream | null;
  localVideoStream: null;
  remoteVideoStream: null;
  setLocalVideo: () => Promise<boolean>;
  setMicEnabled: (on: boolean) => void;
  close: () => void;
};

export const openWSCall = async (
  sid: string,
  callId: string,
  micDeviceId: string | null,
): Promise<OpenWSCall> => {
  const bridge = new WSAudioBridge(sid, callId, micDeviceId);
  await bridge.connect();

  return {
    get micStream() {
      return bridge.micStream!;
    },
    get remoteStream() {
      return bridge.remoteStream;
    },
    localVideoStream: null,
    remoteVideoStream: null,
    setLocalVideo: async () => false,
    setMicEnabled: (on: boolean) => bridge.setMicEnabled(on),
    close: () => bridge.disconnect(),
  };
};
