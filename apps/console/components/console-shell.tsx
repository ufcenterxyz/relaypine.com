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
  ChevronRight,
  Coins,
  GitBranch,
  Github,
  KeyRound,
  LayoutDashboard,
  LogOut,
  Menu,
  Network,
  RefreshCw,
  Server,
  ShieldCheck,
  Users,
  Waypoints,
} from "lucide-react";
import { en as t } from "@/i18n/en";
import { consoleEn as c } from "@/i18n/console-en";
import { api, errorMessage } from "@/lib/api";
import { Brand, ThemeToggle } from "./shared";
import { useConsole } from "./console-provider";
import { GITHUB_REPOSITORY_URL } from "@/config/links";
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
  const isOperator = state.surface === "admin" && state.user.operator;
  const isActive = (id: string) =>
    id ? path.startsWith(`/app/${id}`) : path === "/app";
  const title =
    [...customerNav, ...operatorNav].find((item) => isActive(item.id))?.label ||
    t.overview;
  const initials = state.user.name
    .split(" ")
    .map((word) => word[0])
    .join("")
    .slice(0, 2)
    .toUpperCase();
  const navItems = (items: typeof customerNav) =>
    items.map((item) => (
      <Button
        key={item.id}
        as={Link}
        href={`/app${item.id ? `/${item.id}` : ""}`}
        onPress={() => setMenu(false)}
        variant="light"
        aria-current={isActive(item.id) ? "page" : undefined}
        className={`rp-console-nav-item ${isActive(item.id) ? "is-active" : ""}`}
        startContent={<item.icon size={17} strokeWidth={1.65} />}
      >
        <span>{item.label}</span>
        {isActive(item.id) && (
          <ChevronRight
            className="rp-console-nav-indicator"
            size={14}
            aria-hidden="true"
          />
        )}
      </Button>
    ));
  const nav = (
    <div className="rp-console-navigation">
      <div className="rp-console-workspace">
        <div className="rp-console-workspace-monogram" aria-hidden="true">
          {state.tenant.name.charAt(0).toUpperCase()}
        </div>
        <div className="min-w-0">
          <p title={state.tenant.name}>{state.tenant.name}</p>
          <span>{state.project.name}</span>
        </div>
      </div>
      <p className="rp-console-nav-label">{c.workspaceNavigation}</p>
      <nav aria-label={t.workspace}>{navItems(customerNav)}</nav>
      {isOperator && (
        <>
          <p className="rp-console-nav-label rp-console-operations-label">
            {c.infrastructureNavigation}
          </p>
          <nav aria-label={t.operations}>{navItems(operatorNav)}</nav>
        </>
      )}
      <div className="rp-console-account">
        <Button
          as="a"
          role="link"
          href={GITHUB_REPOSITORY_URL}
          target="_blank"
          rel="noopener noreferrer"
          variant="light"
          className="rp-console-nav-item rp-console-source"
          aria-label={t.githubSourceNewTab}
          startContent={<Github size={17} strokeWidth={1.65} />}
          endContent={<ArrowUpRight size={14} className="ml-auto" />}
        >
          {t.githubSource}
        </Button>
        <div className="rp-console-user-row">
          <span className="rp-console-avatar" aria-hidden="true">
            {initials}
          </span>
          <div className="min-w-0">
            <p>{state.user.name}</p>
            <span title={state.user.email}>{state.user.email}</span>
          </div>
        </div>
        <Button
          className="rp-console-signout"
          variant="light"
          startContent={<LogOut size={16} strokeWidth={1.7} />}
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
    <div className="rp-console-shell">
      <aside className="rp-console-sidebar">
        <div className="rp-console-brand">
          <Brand />
          <span className="rp-console-edition">{c.consoleLabel}</span>
        </div>
        <div className="rp-console-sidebar-inner">{nav}</div>
      </aside>
      <div className="rp-console-main">
        <header className="rp-console-topbar">
          <div className="rp-console-breadcrumb">
            <Button
              isIconOnly
              className="lg:hidden"
              variant="light"
              aria-label={t.menu}
              onPress={() => setMenu(true)}
            >
              <Menu size={20} />
            </Button>
            <span className="rp-console-mobile-brand">
              <Brand />
            </span>
            <span className="rp-console-project">{state.project.name}</span>
            <ChevronRight
              className="rp-console-breadcrumb-divider"
              size={13}
              strokeWidth={1.6}
            />
            <span className="rp-console-current-page">{title}</span>
          </div>
          <div className="rp-console-topbar-actions">
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
            <span className="rp-console-topbar-separator" />
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
        <main className="rp-console-content" tabIndex={0} aria-label={title}>
          <div className="rp-console-content-inner">{children}</div>
        </main>
        <footer className="rp-console-footer">
          <span>
            <ShieldCheck size={13} strokeWidth={1.65} />
            {t.privacyNote}
          </span>
          <span className="rp-console-footer-brand">
            {t.brand}
            <span>/</span>
            {c.consoleLabel}
          </span>
        </footer>
      </div>
      {menu && (
        <Modal
          isOpen
          disableAnimation
          onClose={() => setMenu(false)}
          size="sm"
          placement="top"
          scrollBehavior="inside"
          classNames={{
            wrapper: "rp-console-drawer-wrapper",
            base: "rp-console-drawer max-h-[100dvh]",
          }}
        >
          <ModalContent>
            <ModalHeader className="rp-console-drawer-header">
              <Brand />
            </ModalHeader>
            <ModalBody className="rp-console-mobile-menu">{nav}</ModalBody>
          </ModalContent>
        </Modal>
      )}
    </div>
  );
}
