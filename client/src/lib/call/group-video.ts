import { getApiBase, getApiKey } from "@/lib/auth";
import { VideoSender, VideoReceiver, videoSupported } from "./video-codec";

// GroupVideoBridge transporta vídeo H264 da chamada em GRUPO entre o navegador e o
// gateway, pelo WS /calls/group/video-ws. Envia a câmera do atendente (VideoSender)
// e recebe o vídeo de cada participante (um VideoReceiver por pid), expondo um
// MediaStream por participante p/ renderizar.
//
// Protocolo binário (igual ao backend handleGroupVideoWS):
//   gateway → navegador: [1 byte len(pid)][pid ascii][access unit H264 annexb]
//   navegador → gateway: access unit H264 annexb puro (câmera)
export class GroupVideoBridge {
  private ws: WebSocket | null = null;
  private sender: VideoSender | null = null;
  private receivers = new Map<string, VideoReceiver>();
  private closed = false;

  /** Stream local da câmera (preview). Disponível após connect(). */
  localStream: MediaStream | null = null;

  /** Chamado quando o vídeo de um participante aparece (novo pid). */
  onParticipantStream: ((pid: string, stream: MediaStream) => void) | null = null;
  /** Chamado quando um participante some (stream encerrado). */
  onParticipantGone: ((pid: string) => void) | null = null;

  constructor(private sid: string) {}

  static supported(): boolean {
    return videoSupported();
  }

  async connect(): Promise<void> {
    // Câmera do atendente.
    this.localStream = await navigator.mediaDevices.getUserMedia({
      video: { width: { ideal: 320 }, height: { ideal: 240 }, frameRate: { ideal: 15 } },
      audio: false,
    });

    const base = getApiBase();
    const wsBase = base.replace(/^https:\/\//, "wss://").replace(/^http:\/\//, "ws://");
    const apiKey = getApiKey();
    const url =
      `${wsBase}/api/sessions/${this.sid}/calls/group/video-ws` +
      (apiKey ? `?apiKey=${encodeURIComponent(apiKey)}` : "");

    await new Promise<void>((resolve, reject) => {
      const ws = new WebSocket(url, ["h264-group"]);
      ws.binaryType = "arraybuffer";
      this.ws = ws;
      ws.onopen = () => resolve();
      ws.onerror = () => reject(new Error("falha ao conectar o vídeo do grupo"));
      ws.onclose = () => {
        if (!this.closed) this.close();
      };
      ws.onmessage = (ev) => this.onMessage(ev.data as ArrayBuffer);
    });

    // Envia a câmera (encode annexb → WS binário).
    const track = this.localStream.getVideoTracks()[0];
    if (track) {
      this.sender = new VideoSender(track, (au) => {
        if (this.ws && this.ws.readyState === WebSocket.OPEN) {
          this.ws.send(au);
        }
      });
    }
  }

  private onMessage(data: ArrayBuffer): void {
    if (!data || data.byteLength < 2) return;
    const bytes = new Uint8Array(data);
    const pidLen = bytes[0];
    if (data.byteLength < 1 + pidLen) return;
    const pid = new TextDecoder().decode(bytes.subarray(1, 1 + pidLen));
    const au = bytes.subarray(1 + pidLen);

    let rx = this.receivers.get(pid);
    if (!rx) {
      rx = new VideoReceiver();
      this.receivers.set(pid, rx);
      this.onParticipantStream?.(pid, rx.stream);
    }
    rx.decode(au.buffer.slice(au.byteOffset, au.byteOffset + au.byteLength));
  }

  close(): void {
    this.closed = true;
    try {
      this.sender?.close();
    } catch {}
    this.sender = null;
    for (const [pid, rx] of this.receivers) {
      try {
        rx.close();
      } catch {}
      this.onParticipantGone?.(pid);
    }
    this.receivers.clear();
    try {
      this.localStream?.getTracks().forEach((t) => t.stop());
    } catch {}
    this.localStream = null;
    try {
      this.ws?.close();
    } catch {}
    this.ws = null;
  }
}
