import { apiGet, apiPost, apiDelete } from "@/lib/api";

export type WebhookConfig = {
  webhook: string;
  /** true se há secret configurado (o valor nunca é devolvido). */
  hasSecret: boolean;
  /** Filtro de eventos (vazio = todos). */
  events: string[] | null;
  /** Não entrega eventos de grupo. */
  ignoreGroups: boolean;
};

export type WebhookInput = {
  url: string;
  /** Ausente = mantém o secret atual; "" = remove. */
  secret?: string;
  events: string[];
  ignoreGroups: boolean;
};

export const getWebhook = (sid: string) => apiGet<WebhookConfig>(`/api/sessions/${sid}/webhook`);

export const setWebhook = (sid: string, cfg: WebhookInput) =>
  apiPost<WebhookConfig>(`/api/sessions/${sid}/webhook`, cfg);

export const deleteWebhook = (sid: string) => apiDelete(`/api/sessions/${sid}/webhook`);
