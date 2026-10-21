"use client";
import type { ReactNode } from "react";
import { useState } from "react";
import {
  Button,
  Chip,
  Tooltip,
  Modal,
  ModalContent,
  ModalHeader,
  ModalBody,
  ModalFooter,
  addToast,
} from "@heroui/react";
import { Copy, Inbox, RefreshCw, TreePine, Moon, Sun } from "lucide-react";
import Link from "next/link";
import { useTheme } from "next-themes";
import { en as t } from "@/i18n/en";
import { errorMessage } from "@/lib/api";
export function Brand() {
  return (
    <Link
      href="/"
      className="inline-flex items-center gap-2.5 text-lg font-semibold tracking-tight"
    >
      <span className="flex size-8 items-center justify-center rounded-lg bg-foreground text-background">
        <TreePine size={20} strokeWidth={1.7} />
      </span>
      {t.brand}
    </Link>
  );
}
export function ThemeToggle() {
  const { resolvedTheme, setTheme } = useTheme();
  return (
    <Tooltip content={resolvedTheme === "dark" ? t.lightMode : t.darkMode}>
      <Button
        isIconOnly
        variant="light"
        aria-label={resolvedTheme === "dark" ? t.lightMode : t.darkMode}
        onPress={() => setTheme(resolvedTheme === "dark" ? "light" : "dark")}
      >
        <Sun size={17} className="hidden dark:block" />
        <Moon size={17} className="dark:hidden" />
      </Button>
    </Tooltip>
  );
}
export function CopyButton({ value }: { value: string }) {
  return (
    <Tooltip content={t.copy}>
      <Button
        aria-label={t.copy}
        isIconOnly
        variant="light"
        size="sm"
        className="min-h-11 min-w-11"
        onPress={async () => {
          try {
            await navigator.clipboard.writeText(value);
            addToast({ title: t.copied, color: "success" });
          } catch {
            addToast({ title: t.copyFailed, color: "danger" });
          }
        }}
      >
        <Copy size={16} />
      </Button>
    </Tooltip>
  );
}
export function IconButton({
  label,
  children,
  onPress,
  isLoading = false,
  color = "default",
}: {
  label: string;
  children: ReactNode;
  onPress: () => void;
  isLoading?: boolean;
  color?: "default" | "danger" | "primary";
}) {
  return (
    <Tooltip content={label}>
      <Button
        aria-label={label}
        isIconOnly
        variant="light"
        size="sm"
        className="min-h-11 min-w-11"
        onPress={onPress}
        isLoading={isLoading}
        color={color}
      >
        {children}
      </Button>
    </Tooltip>
  );
}
export function Status({
  enabled,
  label,
}: {
  enabled: boolean;
  label?: string;
}) {
  return (
    <Chip variant="flat" size="sm" color={enabled ? "success" : "default"}>
      {label || (enabled ? t.enabled : t.disabled)}
    </Chip>
  );
}
export function Empty({
  title,
  body,
  action,
}: {
  title: string;
  body: string;
  action?: ReactNode;
}) {
  return (
    <div className="flex min-h-60 flex-col items-center justify-center px-5 py-10 text-center">
      <Inbox className="mb-4 size-10 text-default-400" strokeWidth={1.3} />
      <h3 className="font-medium">{title}</h3>
      <p className="mt-2 max-w-md text-sm leading-6 text-default-500">{body}</p>
      {action && <div className="mt-5">{action}</div>}
    </div>
  );
}
export function ErrorBox({
  message,
  retry,
}: {
  message: string;
  retry?: () => void;
}) {
  return (
    <div
      role="alert"
      className="rounded-xl border border-danger-200 bg-danger-50 p-4 text-sm text-danger-700 dark:text-danger-300"
    >
      <p>{message}</p>
      {retry && (
        <Button
          className="mt-3"
          size="sm"
          variant="flat"
          onPress={retry}
          startContent={<RefreshCw size={14} />}
        >
          {t.retry}
        </Button>
      )}
    </div>
  );
}
export function PageHeader({
  title,
  description,
  action,
}: {
  title: string;
  description: string;
  action?: ReactNode;
}) {
  return (
    <div className="mb-7 flex flex-wrap items-start justify-between gap-4">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">{title}</h1>
        <p className="mt-2 max-w-2xl text-sm leading-6 text-default-500">
          {description}
        </p>
      </div>
      {action}
    </div>
  );
}
export function DataTable({
  columns,
  rows,
  empty,
  caption,
}: {
  columns: string[];
  rows: ReactNode[][];
  empty: ReactNode;
  caption: string;
}) {
  return (
    <div className="overflow-hidden rounded-xl border border-default-200">
      {rows.length ? (
        <div className="max-h-[62vh] overflow-auto">
          <table className="route-table w-full text-sm">
            <caption className="sr-only">{caption}</caption>
            <thead className="bg-default-50 text-xs text-default-500">
              <tr>
                {columns.map((c, i) => (
                  <th scope="col" key={`${c}-${i}`}>
                    {c}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {rows.map((row, i) => (
                <tr key={i} className="hover:bg-default-50/60">
                  {row.map((cell, j) => (
                    <td className="border-b border-default-100" key={j}>
                      {cell}
                    </td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        empty
      )}
    </div>
  );
}
export function Confirm({
  title,
  body,
  action,
  onConfirm,
  onClose,
}: {
  title: string;
  body: string;
  action: string;
  onConfirm: () => Promise<void>;
  onClose: () => void;
}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  return (
    <Modal
      isOpen
      onClose={onClose}
      isDismissable={!busy}
      size="md"
      scrollBehavior="inside"
      classNames={{ base: "max-h-[85dvh]" }}
    >
      <ModalContent>
        <ModalHeader>{title}</ModalHeader>
        <ModalBody>
          <p className="text-sm leading-6 text-default-600">{body}</p>
          {error && <ErrorBox message={error} />}
        </ModalBody>
        <ModalFooter>
          <Button variant="light" onPress={onClose} isDisabled={busy}>
            {t.cancel}
          </Button>
          <Button
            color="danger"
            isLoading={busy}
            onPress={async () => {
              setBusy(true);
              setError("");
              try {
                await onConfirm();
                onClose();
              } catch (e) {
                setError(errorMessage(e));
              } finally {
                setBusy(false);
              }
            }}
          >
            {action}
          </Button>
        </ModalFooter>
      </ModalContent>
    </Modal>
  );
}
export function CodeBlock({ code, label }: { code: string; label: string }) {
  return (
    <div className="overflow-hidden rounded-xl border border-default-200 bg-default-50">
      <div className="flex items-center justify-between border-b border-default-200 px-4 text-xs text-default-500">
        <span>{label}</span>
        <CopyButton value={code} />
      </div>
      <pre className="overflow-x-auto p-5 text-xs leading-7 text-default-700">
        <code>{code}</code>
      </pre>
    </div>
  );
}
