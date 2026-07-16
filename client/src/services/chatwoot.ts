import { apiGet, apiPost, apiDelete } from "@/lib/api";

export type ChatwootConfig = {
  url: string;
  account_id: number;
  account_token?: string;
  inbox_id: number;
  inbox_identifier: string;
  /** Reflete no Chatwoot (como nota privada) o que for enviado pela API. */
  mirror_api?: boolean;
  /** Importa o histórico de conversas ao conectar a conta (HistorySync). */
  import_history?: boolean;
  /** Janela do histórico a importar, em dias (0 = padrão). */
  import_history_days?: number;
};

export const getChatwoot = (sid: string) =>
  apiGet<{ enabled: boolean; chatwoot: ChatwootConfig }>(`/api/sessions/${sid}/chatwoot`);

export const setChatwoot = (sid: string, cfg: ChatwootConfig) =>
  apiPost(`/api/sessions/${sid}/chatwoot`, cfg);

export const deleteChatwoot = (sid: string) => apiDelete(`/api/sessions/${sid}/chatwoot`);
