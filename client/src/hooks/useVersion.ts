import { useQuery } from "@tanstack/react-query";
import { apiGet } from "@/lib/api";

// useVersion busca o build rodando (exposto em /api/config): `label` é o canal
// (nome do branch/tag: "develop", "v1.0.2"...) mostrado ao lado da logo, e `commit`
// é o SHA curto, usado no tooltip.
export const useVersion = (): { label: string; commit: string } => {
  const { data } = useQuery({
    queryKey: ["config-version"],
    queryFn: () => apiGet<{ version?: string; commit?: string }>("/api/config"),
    staleTime: Infinity,
    retry: false,
  });
  const label = data?.version ?? "";
  const c = data?.commit ?? "";
  const commit = c.length > 12 ? c.slice(0, 7) : c; // SHA longo -> curto
  return { label, commit };
};
