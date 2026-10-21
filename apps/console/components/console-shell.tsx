"use client";
import { useState } from "react";
import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";
import {
  Button,
  Tooltip,
  Modal,
  ModalContent,
  ModalBody,
  ModalHeader,
  addToast,
} from "@heroui/react";
import {
  Activity,
  ArrowUpRight,
  Coins,
  GitBranch,
  KeyRound,
  LayoutDashboard,
  LogOut,
  Menu,
  Network,
  RefreshCw,
  Server,
  Users,
  Waypoints,
} from "lucide-react";
import { en as t } from "@/i18n/en";
import { api, errorMessage } from "@/lib/api";
import { Brand, ThemeToggle } from "./shared";
import { useConsole } from "./console-provider";
const customerNav = [
  { id: "", label: t.overview, icon: LayoutDashboard },
  { id: "keys", label: t.keys, icon: KeyRound },
  { id: "models", label: t.models, icon: Waypoints },
  { id: "usage", label: t.usage, icon: Activity },
  { id: "billing", label: t.billing, icon: Coins },
];
const operatorNav = [
  { id: "upstreams", label: t.upstreams, icon: Server },
  { id: "proxies", label: t.proxies, icon: Network },
  { id: "routing", label: t.routing, icon: GitBranch },
  { id: "customers", label: t.customers, icon: Users },
];
export function ConsoleShell({ children }: { children: React.ReactNode }) {
  const { state, refresh, refreshing } = useConsole();
  const path = usePathname();
  const router = useRouter();
  const [menu, setMenu] = useState(false);
  const [loggingOut, setLoggingOut] = useState(false);
  const title =
    [...customerNav, ...operatorNav].find(
      (item) => path === "/app" + (item.id ? "/" + item.id : ""),
    )?.label || t.overview;
  const nav = (
    <div className="flex h-full flex-col">
      <div className="mb-7 rounded-lg border border-default-200 px-3 py-3">
        <p className="truncate text-sm font-medium">{state.tenant.name}</p>
        <p className="mt-1 text-xs text-default-500">
          {state.user.operator ? t.operator : t.member}
        </p>
      </div>
      <nav aria-label={t.workspace} className="space-y-1">
        {customerNav.map((item) => (
          <Button
            key={item.id}
            as={Link}
            href={`/app${item.id ? `/${item.id}` : ""}`}
            onPress={() => setMenu(false)}
            variant={
              path === `/app${item.id ? `/${item.id}` : ""}` ? "flat" : "light"
            }
            className={`w-full justify-start ${path === `/app${item.id ? `/${item.id}` : ""}` ? "font-semibold" : "text-default-600"}`}
            startContent={<item.icon size={17} strokeWidth={1.6} />}
          >
            {item.label}
          </Button>
        ))}
      </nav>
      {state.user.operator && (
        <>
          <p className="mb-3 mt-9 px-3 text-[11px] font-medium uppercase tracking-wider text-default-400">
            {t.operations}
          </p>
          <nav aria-label={t.operations} className="space-y-1">
            {operatorNav.map((item) => (
              <Button
                key={item.id}
                as={Link}
                href={`/app/${item.id}`}
                onPress={() => setMenu(false)}
                variant={path.startsWith(`/app/${item.id}`) ? "flat" : "light"}
                className="w-full justify-start text-default-600"
                startContent={<item.icon size={17} strokeWidth={1.6} />}
              >
                {item.label}
              </Button>
            ))}
          </nav>
        </>
      )}
      <div className="mt-auto border-t border-default-200 pt-5">
        <p className="truncate px-3 text-sm">{state.user.name}</p>
        <p className="mt-1 truncate px-3 text-xs text-default-500">
          {state.user.email}
        </p>
        <Button
          className="mt-3 w-full justify-start text-default-500"
          variant="light"
          startContent={<LogOut size={16} />}
          isLoading={loggingOut}
          onPress={async () => {
            setLoggingOut(true);
            try {
              await api("/api/auth/logout", {});
              router.replace("/");
            } catch (e) {
              addToast({ title: errorMessage(e), color: "danger" });
              setLoggingOut(false);
            }
          }}
        >
          {t.signOut}
        </Button>
      </div>
    </div>
  );
  return (
    <div className="flex h-dvh flex-col overflow-hidden">
      <header className="flex h-[72px] shrink-0 items-center justify-between border-b border-default-200 px-4 sm:px-7">
        <div className="flex items-center gap-3">
          <Button
            isIconOnly
            className="lg:hidden"
            variant="light"
            aria-label={t.menu}
            onPress={() => setMenu(true)}
          >
            <Menu size={20} />
          </Button>
          <Brand />
          <span className="mx-3 hidden text-default-300 sm:block">/</span>
          <span className="hidden text-sm text-default-500 sm:block">
            {t.workspace}
          </span>
        </div>
        <div className="flex items-center gap-1">
          <Tooltip content={t.refresh}>
            <Button
              isIconOnly
              variant="light"
              aria-label={t.refresh}
              isLoading={refreshing}
              onPress={() => void refresh()}
            >
              <RefreshCw size={16} />
            </Button>
          </Tooltip>
          <ThemeToggle />
          <Tooltip content={t.home}>
            <Button
              isIconOnly
              as={Link}
              href="/"
              variant="light"
              aria-label={t.home}
            >
              <ArrowUpRight size={17} />
            </Button>
          </Tooltip>
        </div>
      </header>
      <div className="flex min-h-0 flex-1">
        <aside className="hidden w-60 shrink-0 overflow-y-auto border-r border-default-200 p-5 lg:block">
          {nav}
        </aside>
        <div className="flex min-w-0 flex-1 flex-col">
          <div className="flex h-12 shrink-0 items-center border-b border-default-100 px-6 text-xs text-default-500">
            {state.project.name}
            <span className="mx-2 text-default-300">/</span>
            <span className="text-foreground">{title}</span>
          </div>
          <main className="flex-1 overflow-y-auto px-5 py-7 sm:px-8">
            <div className="mx-auto max-w-7xl">{children}</div>
          </main>
          <footer className="shrink-0 border-t border-default-100 px-6 py-3 text-[11px] text-default-400">
            {t.privacyNote}
          </footer>
        </div>
      </div>
      {menu && (
        <Modal
          isOpen
          onClose={() => setMenu(false)}
          size="sm"
          placement="top"
          scrollBehavior="inside"
        >
          <ModalContent>
            <ModalHeader>{t.workspace}</ModalHeader>
            <ModalBody className="min-h-[70dvh] pb-5">{nav}</ModalBody>
          </ModalContent>
        </Modal>
      )}
    </div>
  );
}
