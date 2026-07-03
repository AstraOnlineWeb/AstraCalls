import { Mic, Volume2 } from "lucide-react";
import { cn } from "@/lib/utils";
import { useAudioDevices } from "@/hooks/useAudioDevices";
import { useDevices } from "@/stores/devices";

const selectClass = cn(
  "h-9 truncate rounded-full border border-input bg-card px-3 text-sm shadow-sm",
  "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
  // mobile: ocupa a largura disponível; desktop: largura fixa com truncamento
  "min-w-0 flex-1 sm:flex-none sm:w-56",
);

export const DeviceSelector = () => {
  const { mics, outs } = useAudioDevices();
  const micId = useDevices((s) => s.micId);
  const outId = useDevices((s) => s.outId);
  const setMic = useDevices((s) => s.setMic);
  const setOut = useDevices((s) => s.setOut);

  return (
    <div className="flex flex-col gap-2 sm:flex-row sm:flex-wrap sm:items-center sm:gap-3">
      <div className="flex min-w-0 items-center gap-2">
        <Mic className="h-4 w-4 shrink-0 text-muted-foreground" />
        <select value={micId ?? ""} onChange={(e) => setMic(e.target.value)} className={selectClass}>
          <option value="">Microfone padrão</option>
          {mics.map((d) => (
            <option key={d.deviceId} value={d.deviceId}>
              {d.label}
            </option>
          ))}
        </select>
      </div>
      <div className="flex min-w-0 items-center gap-2">
        <Volume2 className="h-4 w-4 shrink-0 text-muted-foreground" />
        <select value={outId ?? ""} onChange={(e) => setOut(e.target.value)} className={selectClass}>
          <option value="">Alto-falante padrão</option>
          {outs.map((d) => (
            <option key={d.deviceId} value={d.deviceId}>
              {d.label}
            </option>
          ))}
        </select>
      </div>
    </div>
  );
};
