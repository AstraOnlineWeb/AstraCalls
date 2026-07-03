import { useState } from "react";
import { Phone } from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { DeviceSelector } from "@/components/form/DeviceSelector";
import { useStartCall } from "@/hooks/useStartCall";
import { useDevices } from "@/stores/devices";

export const Dialer = ({ sid }: { sid: string }) => {
  const [phone, setPhone] = useState("");
  const micId = useDevices((s) => s.micId);
  const startCall = useStartCall(sid, micId);

  const submit = () => {
    if (!phone.trim() || startCall.isPending) return;
    startCall.mutate({ phone: phone.trim(), record: false }, { onSuccess: () => setPhone("") });
  };

  return (
    <Card>
      <CardHeader>
        <CardTitle>Discador</CardTitle>
      </CardHeader>
      <CardContent className="space-y-4">
        <DeviceSelector />
        <div className="flex flex-wrap items-center gap-2">
          <Input
            value={phone}
            onChange={(e) => setPhone(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") submit();
            }}
            placeholder="+55 11 99999 9999"
            inputMode="tel"
            className="min-w-0 flex-1"
          />
          <Button onClick={submit} disabled={startCall.isPending || !phone.trim()}>
            <Phone className="h-4 w-4" />
            {startCall.isPending ? "Ligando…" : "Ligar"}
          </Button>
        </div>
        <p className="text-xs text-muted-foreground">
          Você pode fazer várias ligações ao mesmo tempo — disque outro número e uma nova chamada aparece abaixo.
        </p>
      </CardContent>
    </Card>
  );
};
