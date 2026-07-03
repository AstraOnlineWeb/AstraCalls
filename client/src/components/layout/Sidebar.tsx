import { useState } from "react";
import { Loader2, Plus, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { ConfirmDialog } from "@/components/shared/ConfirmDialog";
import { setActiveSession, useSessions } from "@/stores/sessions";
import { createSession, deleteSession } from "@/services/sessions";
import type { SessionInfo, SessionState } from "@/types/session";

const dotClass: Record<SessionState, string> = {
  open: "bg-primary",
  qr: "bg-amber-500",
  connecting: "bg-muted-foreground/50",
  logged_out: "bg-destructive",
};

export const Sidebar = ({ onNavigate }: { onNavigate?: () => void }) => {
  const sessions = useSessions((s) => s.sessions);
  const activeId = useSessions((s) => s.activeId);
  const [creating, setCreating] = useState(false);
  const [toDelete, setToDelete] = useState<SessionInfo | null>(null);

  const onNew = async () => {
    setCreating(true);
    try {
      const { id } = await createSession("WhatsApp");
      setActiveSession(id);
      onNavigate?.();
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setCreating(false);
    }
  };

  const remove = async (id: string) => {
    try {
      await deleteSession(id);
    } catch (e) {
      toast.error((e as Error).message);
    }
  };

  return (
    <div className="flex h-full flex-col gap-1 p-3">
      <div className="flex items-center px-2 pb-2 pt-1">
        <span className="inline-flex dark:rounded-lg dark:bg-white dark:px-2 dark:py-1.5">
          <img src="/logoCalls.png" alt="AstraCalls" className="h-7 w-auto select-none" draggable={false} />
        </span>
      </div>
      <p className="px-3 pb-1 pt-2 text-[0.68rem] font-semibold uppercase tracking-wider text-muted-foreground">
        Contas
      </p>
      <div className="flex-1 space-y-1 overflow-y-auto pr-0.5">
        {sessions.map((s) => (
          <div
            key={s.id}
            role="button"
            tabIndex={0}
            onClick={() => {
              setActiveSession(s.id);
              onNavigate?.();
            }}
            className={cn(
              "group flex cursor-pointer items-center gap-2.5 rounded-xl px-3 py-2.5 text-sm transition-colors",
              s.id === activeId
                ? "bg-accent text-accent-foreground shadow-soft"
                : "text-foreground/80 hover:bg-muted",
            )}
          >
            <span className={cn("h-2.5 w-2.5 shrink-0 rounded-full ring-2 ring-card", dotClass[s.state])} />
            <div className="min-w-0 flex-1">
              <p className="truncate font-medium">{s.name}</p>
              {s.jid && <p className="truncate text-xs text-muted-foreground">{s.jid.split("@")[0]}</p>}
            </div>
            <button
              onClick={(e) => {
                e.stopPropagation();
                setToDelete(s);
              }}
              className="text-muted-foreground opacity-0 transition-opacity hover:text-destructive group-hover:opacity-100"
              aria-label={`Excluir ${s.name}`}
            >
              <Trash2 className="h-4 w-4" />
            </button>
          </div>
        ))}
        {sessions.length === 0 && (
          <p className="px-3 py-2 text-sm text-muted-foreground">Nenhuma conta ainda.</p>
        )}
      </div>
      <Button variant="outline" className="w-full" onClick={onNew} disabled={creating}>
        {creating ? <Loader2 className="h-4 w-4 animate-spin" /> : <Plus className="h-4 w-4" />}
        Nova conta
      </Button>

      <ConfirmDialog
        open={!!toDelete}
        onOpenChange={(o) => !o && setToDelete(null)}
        title="Excluir conta?"
        description={toDelete ? `${toDelete.name} será desconectada e removida.` : undefined}
        confirmLabel="Excluir"
        destructive
        onConfirm={() => {
          if (toDelete) void remove(toDelete.id);
        }}
      />
    </div>
  );
};
