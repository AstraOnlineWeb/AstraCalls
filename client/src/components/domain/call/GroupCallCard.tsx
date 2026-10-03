import { useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Users, PhoneCall, PhoneOff, Loader2 } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { apiGet } from "@/lib/api";
import { WSAudioBridge } from "@/lib/ws-audio";
import { listGroups, startGroupCall, endGroupCall } from "@/services/groupCalls";

// GroupCallCard — chamada em GRUPO (experimental): escolhe um grupo, liga pros
// membros e conecta o áudio (ouvir o mix + falar) pelo bridge WS. Só aparece quando
// a instância tem a flag de grupo ligada (/api/config groupCalls).
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
  const bridgeRef = useRef<WSAudioBridge | null>(null);

  if (!config?.groupCalls) return null;

  const start = async () => {
    const target = jid || groups?.[0]?.jid;
    if (!target) {
      toast.error("Escolha um grupo");
      return;
    }
    setStatus("calling");
    try {
      await startGroupCall(sid, target);
      const bridge = new WSAudioBridge(sid, "group", null, {
        onState: (s) => setStatus(s === "connected" ? "in-call" : s === "disconnected" ? "idle" : "calling"),
        onError: (e) => toast.error(e.message),
      });
      bridgeRef.current = bridge;
      await bridge.connect();
      setStatus("in-call");
      toast.success("Chamada em grupo iniciada");
    } catch (e) {
      setStatus("idle");
      toast.error((e as Error).message || "Falha ao iniciar a chamada em grupo");
    }
  };

  const end = async () => {
    bridgeRef.current?.disconnect();
    bridgeRef.current = null;
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
        <div className="flex flex-col gap-2 sm:flex-row">
          <select
            value={jid}
            onChange={(e) => setJid(e.target.value)}
            className="flex-1 rounded-md border bg-background px-3 py-2 text-sm"
          >
            <option value="">Selecione um grupo…</option>
            {(groups ?? []).map((g) => (
              <option key={g.jid} value={g.jid}>
                {g.name} ({g.participants?.length ?? 0})
              </option>
            ))}
          </select>
          <Button onClick={start}>
            <PhoneCall className="h-4 w-4" /> Ligar no grupo
          </Button>
        </div>
      ) : (
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
      )}
    </Card>
  );
};
