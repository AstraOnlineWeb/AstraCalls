import { useQuery } from "@tanstack/react-query";
import { apiGet } from "@/lib/api";

// useVersion busca a versão (commit) do build rodando, exposta em /api/config,
// para mostrar no painel ao lado da logo. Encurta o SHA do git p/ 7 chars.
export const useVersion = (): string => {
  const { data } = useQuery({
    queryKey: ["config-version"],
    queryFn: () => apiGet<{ version?: string }>("/api/config").then((c) => c.version ?? ""),
    staleTime: Infinity,
    retry: false,
  });
  const v = data ?? "";
  return v.length > 12 ? v.slice(0, 7) : v; // SHA longo -> curto; "dev" fica "dev"
};
