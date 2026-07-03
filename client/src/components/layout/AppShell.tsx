import { useState, type ReactNode } from "react";
import { Menu, BookOpen, LogOut } from "lucide-react";
import { Button } from "@/components/ui/button";
import { isAuthed, clearAuth } from "@/lib/auth";
import { Sheet, SheetContent, SheetTitle, SheetTrigger } from "@/components/ui/sheet";
import { Sidebar } from "./Sidebar";
import { ThemeToggle } from "./ThemeToggle";

export const AppShell = ({ children }: { children: ReactNode }) => {
  const [mobileOpen, setMobileOpen] = useState(false);

  return (
    <div className="min-h-screen bg-background">
      <div className="mx-auto flex min-h-screen w-full max-w-[1440px] gap-4 p-3 sm:p-4">
        {/* Sidebar flutuante (desktop) */}
        <aside className="hidden w-64 shrink-0 md:block">
          <div className="sticky top-4 flex h-[calc(100vh-2rem)] flex-col overflow-hidden rounded-2xl border bg-card shadow-soft">
            <Sidebar />
          </div>
        </aside>

        {/* Coluna principal */}
        <div className="flex min-w-0 flex-1 flex-col gap-4">
          {/* Top bar */}
          <header className="flex items-center justify-between gap-3">
            <div className="flex min-w-0 items-center gap-2">
              <Sheet open={mobileOpen} onOpenChange={setMobileOpen}>
                <SheetTrigger asChild>
                  <Button variant="outline" size="icon" className="md:hidden" aria-label="Contas">
                    <Menu className="h-4 w-4" />
                  </Button>
                </SheetTrigger>
                <SheetContent side="left" className="w-72 border-0 p-0">
                  <SheetTitle className="sr-only">Contas</SheetTitle>
                  <Sidebar onNavigate={() => setMobileOpen(false)} />
                </SheetContent>
              </Sheet>
              <span className="inline-flex md:hidden dark:rounded-lg dark:bg-white dark:px-1.5 dark:py-1">
                <img src="/logoCalls.png" alt="AstraCalls" className="h-6 w-auto select-none" draggable={false} />
              </span>
              <h1 className="hidden truncate text-2xl font-bold tracking-tight text-foreground md:block">
                Chamadas
              </h1>
            </div>
            <div className="flex items-center gap-2">
              <Button variant="outline" size="icon" className="rounded-full" asChild>
                <a
                  href="/api-docs.html"
                  target="_blank"
                  rel="noopener noreferrer"
                  aria-label="Documentação da API"
                  title="Documentação da API"
                >
                  <BookOpen className="h-4 w-4" />
                </a>
              </Button>
              <ThemeToggle />
              {isAuthed() && (
                <Button
                  variant="outline"
                  size="icon"
                  className="rounded-full"
                  aria-label="Sair"
                  title="Sair"
                  onClick={() => {
                    clearAuth();
                    location.reload();
                  }}
                >
                  <LogOut className="h-4 w-4" />
                </Button>
              )}
            </div>
          </header>

          {/* Conteúdo */}
          <main className="flex-1 pb-4">{children}</main>
        </div>
      </div>
    </div>
  );
};
