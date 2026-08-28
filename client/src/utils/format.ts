import type { CallStatus } from "@/types/call";

// phoneFromJid extrai e formata o número de um JID do WhatsApp
// ("5561999999999@s.whatsapp.net", "5561999999999:12@s.whatsapp.net", com
// sufixo de device/LID etc.). Formata BR (+55 (DD) 9XXXX-XXXX) quando reconhece;
// senão devolve "+<dígitos>". String vazia quando não há número.
export const phoneFromJid = (jid: string): string => {
  if (!jid) return "";
  const user = jid.split("@")[0].split(":")[0].split(".")[0];
  const d = user.replace(/\D/g, "");
  if (!d) return "";
  // Brasil: 55 + DDD(2) + 8 ou 9 dígitos
  if (d.startsWith("55") && (d.length === 12 || d.length === 13)) {
    const ddd = d.slice(2, 4);
    const rest = d.slice(4);
    const mid = rest.length === 9 ? `${rest.slice(0, 5)}-${rest.slice(5)}` : `${rest.slice(0, 4)}-${rest.slice(4)}`;
    return `+55 (${ddd}) ${mid}`;
  }
  return `+${d}`;
};

export const formatCallDuration = (startedAt: number, status: CallStatus): string => {
  if (status !== "connected") return status;
  const s = Math.floor((Date.now() - startedAt) / 1000);
  return `${String(Math.floor(s / 60)).padStart(2, "0")}:${String(s % 60).padStart(2, "0")}`;
};
