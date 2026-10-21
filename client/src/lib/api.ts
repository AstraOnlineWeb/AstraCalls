import { getClientId } from "./client-id";
import { apiUrl, getApiKey, clearAuth } from "./auth";

const baseHeaders = (): HeadersInit => ({
  "X-Client-Id": getClientId(),
  "X-API-Key": getApiKey(),
  "Content-Type": "application/json",
});

// Em 401 (key inválida/expirada) limpa a auth e volta pra tela de login.
const guard = (status: number) => {
  if (status === 401) {
    clearAuth();
    location.reload();
  }
};

export const apiGet = async <T>(path: string): Promise<T> => {
  const r = await fetch(apiUrl(path), { headers: baseHeaders() });
  if (!r.ok) {
    guard(r.status);
    throw new Error(`${path} ${r.status}`);
  }
  return r.json() as Promise<T>;
};

export const apiPost = async <T>(path: string, body: unknown): Promise<T> => {
  const r = await fetch(apiUrl(path), { method: "POST", headers: baseHeaders(), body: JSON.stringify(body) });
  if (!r.ok) {
    guard(r.status);
    const text = await r.text().catch(() => "");
    throw new Error(`${path} ${r.status} ${text}`);
  }
  return r.json() as Promise<T>;
};

export const apiPut = async <T>(path: string, body: unknown): Promise<T> => {
  const r = await fetch(apiUrl(path), { method: "PUT", headers: baseHeaders(), body: JSON.stringify(body) });
  if (!r.ok) {
    guard(r.status);
    const text = await r.text().catch(() => "");
    throw new Error(`${path} ${r.status} ${text}`);
  }
  return r.json() as Promise<T>;
};

export const apiDelete = async (path: string): Promise<void> => {
  const r = await fetch(apiUrl(path), { method: "DELETE", headers: baseHeaders() });
  if (!r.ok) {
    guard(r.status);
    throw new Error(`${path} ${r.status}`);
  }
};
