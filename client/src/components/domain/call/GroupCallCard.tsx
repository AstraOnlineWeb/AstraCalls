import { useEffect, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Users, PhoneCall, PhoneOff, Loader2, Video } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { apiGet } from "@/lib/api";
import { WSAudioBridge } from "@/lib/ws-audio";
import { GroupVideoBridge } from "@/lib/call/group-video";
import { listGroups, startGroupCall, endGroupCall, getGroupRoster, type RosterEntry } from "@/services/groupCalls";

// VideoTile liga um MediaStream a um <video> (srcObject não é prop do React).
const VideoTile = ({ stream, label, muted }: { stream: MediaStream; label: string; muted?: boolean }) => {
  const ref = useRef<HTMLVideoElement>(null);
  useEffect(() => {
    if (ref.current) ref.current.srcObject = stream;
  }, [stream]);
  return (
    <div className="relative overflow-hidden rounded-md bg-black">
      <video ref={ref} autoPlay playsInline muted={muted} className="h-32 w-full object-cover" />
      <span className="absolute bottom-1 left-1 rounded bg-black/60 px-1.5 py-0.5 text-[0.6rem] text-white">
        {label}
      </span>
    </div>
  );
};

// GroupCallCard — chamada em GRUPO (experimental): escolhe um grupo, liga pros
// membros e conecta áudio (ouvir/falar) e, opcionalmente, VÍDEO (câmera + vídeo dos
// participantes). Só aparece quando a instância tem a flag de grupo ligada.
export const GroupCallCard = ({ sid }: { sid: string }) => {
  const { data: config } = useQuery({
    queryKey: ["config-groupcalls"],
    queryFn: () => apiGet<{ groupCalls?: boolean }>("/api/config"),
    staleTime: Infinity,
    retry: false,
  });

  const { data: groups } = useQuery({
    queryKey: ["groups", sid],
    queryFn: () => listGroups(sid),
    enabled: !!config?.groupCalls,
    staleTime: 60_000,
    retry: false,
  });

  const [jid, setJid] = useState("");
  const [status, setStatus] = useState<"idle" | "calling" | "in-call">("idle");
  const [localStream, setLocalStream] = useState<MediaStream | null>(null);
  const [peers, setPeers] = useState<Array<{ pid: string; stream: MediaStream }>>([]);
  const [roster, setRoster] = useState<Record<string, RosterEntry>>({});
  const bridgeRef = useRef<WSAudioBridge | null>(null);
  const videoRef = useRef<GroupVideoBridge | null>(null);

  if (!config?.groupCalls) return null;

  const start = async (withVideo: boolean) => {
    const target = jid || groups?.[0]?.jid;
    if (!target) {
      toast.error("Escolha um grupo");
      return;
    }
    if (withVideo && !GroupVideoBridge.supported()) {
      toast.error("Seu navegador não suporta vídeo (WebCodecs)");
      return;
    }
    setStatus("calling");
    try {
      await startGroupCall(sid, target, withVideo);
      getGroupRoster(sid).then(setRoster).catch(() => {});
      const bridge = new WSAudioBridge(sid, "group", null, {
        onState: (s) => setStatus(s === "connected" ? "in-call" : s === "disconnected" ? "idle" : "calling"),
        onError: (e) => toast.error(e.message),
      });
      bridgeRef.current = bridge;
      await bridge.connect();
      setStatus("in-call");
      if (withVideo) {
        const vb = new GroupVideoBridge(sid);
        vb.onParticipantStream = (pid, stream) =>
          setPeers((prev) => (prev.some((p) => p.pid === pid) ? prev : [...prev, { pid, stream }]));
        vb.onParticipantGone = (pid) => setPeers((prev) => prev.filter((p) => p.pid !== pid));
        videoRef.current = vb;
        await vb.connect();
        setLocalStream(vb.localStream);
      }
      toast.success(withVideo ? "Chamada de vídeo em grupo iniciada" : "Chamada em grupo iniciada");
    } catch (e) {
      await cleanup();
      setStatus("idle");
      toast.error((e as Error).message || "Falha ao iniciar a chamada em grupo");
    }
  };

  const cleanup = async () => {
    videoRef.current?.close();
    videoRef.current = null;
    bridgeRef.current?.disconnect();
    bridgeRef.current = null;
    setLocalStream(null);
    setPeers([]);
    setRoster({});
  };

  // labelFor traduz o pid (ex.: "144946606653478:55@lid") em número + nome via roster.
  const labelFor = (pid: string) => {
    const lidNum = pid.split(/[:@]/)[0];
    const e = roster[lidNum];
    if (!e) return pid.slice(-4);
    const phone = e.phone ? `+${e.phone}` : "";
    if (e.name && phone) return `${e.name} · ${phone}`;
    return e.name || phone || pid.slice(-4);
  };

  const end = async () => {
    await cleanup();
    try {
      await endGroupCall(sid);
    } catch {
      /* ignore */
    }
    setStatus("idle");
  };

  return (
    <Card className="space-y-3 p-4">
      <div className="flex items-center gap-2 text-sm font-medium">
        <Users className="h-4 w-4" /> Chamada em grupo
        <span className="rounded bg-muted px-1.5 py-0.5 text-[0.6rem] uppercase text-muted-foreground">
          experimental
        </span>
      </div>

      {status === "idle" ? (
        <div className="flex flex-col gap-2">
          <select
            value={jid}
            onChange={(e) => setJid(e.target.value)}
            className="rounded-md border bg-background px-3 py-2 text-sm"
          >
            <option value="">Selecione um grupo…</option>
            {(groups ?? []).map((g) => (
              <option key={g.jid} value={g.jid}>
                {g.name} ({g.participants?.length ?? 0})
              </option>
            ))}
          </select>
          <div className="flex gap-2">
            <Button onClick={() => start(false)} className="flex-1">
              <PhoneCall className="h-4 w-4" /> Ligar (áudio)
            </Button>
            <Button onClick={() => start(true)} variant="secondary" className="flex-1">
              <Video className="h-4 w-4" /> Ligar com vídeo
            </Button>
          </div>
        </div>
      ) : (
        <div className="space-y-3">
          <div className="flex items-center justify-between">
            <span className="flex items-center gap-2 text-sm text-muted-foreground">
              {status === "calling" ? (
                <>
                  <Loader2 className="h-4 w-4 animate-spin" /> Conectando…
                </>
              ) : (
                <>
                  <span className="h-2.5 w-2.5 rounded-full bg-primary" /> Em chamada de grupo
                </>
              )}
            </span>
            <Button variant="destructive" onClick={end}>
              <PhoneOff className="h-4 w-4" /> Encerrar
            </Button>
          </div>
          {(localStream || peers.length > 0) && (
            <div className="grid grid-cols-2 gap-2 sm:grid-cols-3">
              {localStream && <VideoTile stream={localStream} label="Você" muted />}
              {peers.map((p) => (
                <VideoTile key={p.pid} stream={p.stream} label={labelFor(p.pid)} />
              ))}
            </div>
          )}
        </div>
      )}
    </Card>
  );
};
