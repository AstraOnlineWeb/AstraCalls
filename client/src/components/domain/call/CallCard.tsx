import { useEffect, useRef, useState } from "react";
import { PhoneOff, Pause, Play, Video, VideoOff } from "lucide-react";
import { Card, CardContent } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { attachMeter } from "@/lib/audio-meter";
import { useCalls } from "@/stores/calls";
import { useDevices } from "@/stores/devices";
import { useEndCall } from "@/hooks/useEndCall";
import { useCallHold } from "@/hooks/useCallHold";
import { useCallVideo } from "@/hooks/useCallVideo";
import { formatCallDuration } from "@/utils/format";
import type { CallStatus, CallSummary } from "@/types/call";

const statusVariant: Record<CallStatus, "success" | "secondary" | "muted"> = {
  connected: "success",
  ringing: "secondary",
  starting: "secondary",
  ended: "muted",
};

const Meter = ({ label, db }: { label: string; db: number }) => {
  const pct = Math.max(0, Math.min(100, Math.round(((db + 60) / 60) * 100)));
  return (
    <div className="space-y-1">
      <p className="text-xs text-muted-foreground">{label}</p>
      <div className="h-2 overflow-hidden rounded-full bg-muted">
        <div className="h-full bg-primary transition-all" style={{ width: `${pct}%` }} />
      </div>
    </div>
  );
};

export const CallCard = ({ call }: { call: CallSummary }) => {
  const conn = useCalls((s) => s.ownConnections.get(call.callId));
  const outDeviceId = useDevices((s) => s.outId);
  const endCall = useEndCall();
  const hold = useCallHold();
  const video = useCallVideo(call);
  const held = call.held ?? false;
  const [, force] = useState(0);
  const [micDb, setMicDb] = useState(-60);
  const [peerDb, setPeerDb] = useState(-60);
  const audioRef = useRef<HTMLAudioElement>(null);
  const remoteVideoRef = useRef<HTMLVideoElement>(null);
  const localVideoRef = useRef<HTMLVideoElement>(null);
  const active = call.status === "connected";
  const showVideo = video.state.peerVideo || video.state.localVideo;

  useEffect(() => {
    const t = setInterval(() => force((n) => n + 1), 1000);
    return () => clearInterval(t);
  }, []);

  useEffect(() => {
    if (!conn) return;
    const offMic = attachMeter(conn.micStream, setMicDb);
    let offPeer: (() => void) | null = null;
    const wait = setInterval(() => {
      if (conn.remoteStream && audioRef.current) {
        audioRef.current.srcObject = conn.remoteStream;
        audioRef.current.play().catch(() => {});
        offPeer = attachMeter(conn.remoteStream, setPeerDb);
        clearInterval(wait);
      }
    }, 200);
    return () => {
      offMic();
      offPeer?.();
      clearInterval(wait);
    };
  }, [conn]);

  // Em espera: silencia o mic local (o backend já não envia o áudio do atendente, mas
  // isto zera o medidor e evita uplink à toa) e retoma ao sair da espera.
  useEffect(() => {
    if (!conn) return;
    for (const t of conn.micStream.getAudioTracks()) t.enabled = !held;
  }, [conn, held]);

  useEffect(() => {
    const el = audioRef.current as (HTMLAudioElement & { setSinkId?: (id: string) => Promise<void> }) | null;
    if (!el || !outDeviceId || typeof el.setSinkId !== "function") return;
    el.setSinkId(outDeviceId).catch(() => {});
  }, [outDeviceId, conn]);

  useEffect(() => {
    if (!conn) return;
    if (remoteVideoRef.current && video.state.peerVideo && conn.remoteVideoStream) {
      remoteVideoRef.current.srcObject = conn.remoteVideoStream;
      remoteVideoRef.current.play().catch(() => {});
    }
    if (localVideoRef.current && video.state.localVideo && conn.localVideoStream) {
      localVideoRef.current.srcObject = conn.localVideoStream;
      localVideoRef.current.play().catch(() => {});
    }
  }, [conn, video.state.peerVideo, video.state.localVideo]);

  return (
    <Card>
      <CardContent className="space-y-3 p-4">
        <div className="flex items-start justify-between gap-3">
          <div className="min-w-0">
            <p className="truncate font-medium">{call.peer}</p>
            <div className="mt-1 flex items-center gap-2">
              <Badge variant={held ? "secondary" : statusVariant[call.status]}>
                {formatCallDuration(call.startedAt, call.status)}
              </Badge>
              {held && <Badge variant="muted">Em espera</Badge>}
            </div>
          </div>
          <div className="flex items-center gap-2">
            {active && (
              <Tooltip>
                <TooltipTrigger asChild>
                  <Button
                    variant={held ? "secondary" : "outline"}
                    size="icon"
                    disabled={hold.isPending}
                    onClick={() =>
                      hold.mutate({ sid: call.sessionId, callId: call.callId, hold: !held })
                    }
                    aria-label={held ? "Retomar" : "Colocar em espera"}
                  >
                    {held ? <Play className="h-4 w-4" /> : <Pause className="h-4 w-4" />}
                  </Button>
                </TooltipTrigger>
                <TooltipContent>{held ? "Retomar" : "Colocar em espera"}</TooltipContent>
              </Tooltip>
            )}
            {active && !held && (
              <Tooltip>
                <TooltipTrigger asChild>
                  <Button
                    variant={video.state.localVideo ? "secondary" : "outline"}
                    size="icon"
                    disabled={video.busy}
                    onClick={() => (video.state.localVideo ? video.disable() : video.enable())}
                    aria-label={video.state.localVideo ? "Desligar vídeo" : "Ligar vídeo"}
                  >
                    {video.state.localVideo ? <Video className="h-4 w-4" /> : <VideoOff className="h-4 w-4" />}
                  </Button>
                </TooltipTrigger>
                <TooltipContent>
                  {video.state.localVideo ? "Desligar vídeo" : "Ativar vídeo"}
                </TooltipContent>
              </Tooltip>
            )}
            <Tooltip>
              <TooltipTrigger asChild>
                <Button
                  variant="destructive"
                  size="icon"
                  onClick={() => endCall.mutate({ sid: call.sessionId, callId: call.callId })}
                  aria-label="End call"
                >
                  <PhoneOff className="h-4 w-4" />
                </Button>
              </TooltipTrigger>
              <TooltipContent>Encerrar</TooltipContent>
            </Tooltip>
          </div>
        </div>

        {video.state.upgradeIncoming && (
          <div className="flex items-center justify-between gap-2 rounded-md border border-primary/40 bg-primary/10 px-3 py-2">
            <p className="text-sm">O cliente quer ativar o vídeo.</p>
            <div className="flex gap-2">
              <Button size="sm" disabled={video.busy} onClick={() => video.accept()}>
                Aceitar
              </Button>
              <Button size="sm" variant="outline" disabled={video.busy} onClick={() => video.reject()}>
                Recusar
              </Button>
            </div>
          </div>
        )}
        {video.state.upgradeOutgoing && !video.state.peerVideo && (
          <p className="text-xs text-muted-foreground">Aguardando o cliente aceitar o vídeo…</p>
        )}

        {showVideo && (
          <div className="relative overflow-hidden rounded-md bg-black">
            <video
              ref={remoteVideoRef}
              autoPlay
              playsInline
              className="aspect-video w-full bg-black object-cover"
            />
            {video.state.localVideo && (
              <video
                ref={localVideoRef}
                autoPlay
                playsInline
                muted
                className="absolute bottom-2 right-2 w-24 rounded border border-white/20 object-cover"
              />
            )}
          </div>
        )}
        <Meter label="Mic" db={micDb} />
        <Meter label="Peer" db={peerDb} />
        <audio ref={audioRef} autoPlay />
      </CardContent>
    </Card>
  );
};
