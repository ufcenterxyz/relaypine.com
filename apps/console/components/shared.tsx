"use client";
import type { ReactNode } from "react";
import { useState, useSyncExternalStore } from "react";
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
import { Copy, Check, Inbox, RefreshCw, Moon, Sun } from "lucide-react";
import Link from "next/link";
import { useTheme } from "next-themes";
import { en as t } from "@/i18n/en";
import { errorMessage } from "@/lib/api";
export function BrandMark({ className = "" }: { className?: string }) {
  return (
    <span className={`rp-brand-mark ${className}`} aria-hidden="true">
      <svg viewBox="0 0 28 28" fill="none">
        <path d="M5 20h5L18 6h5M5 14h6l4 6h8M5 8h5" stroke="currentColor" strokeWidth="2.3" strokeLinecap="square" strokeLinejoin="miter" />
      </svg>
    </span>
  );
}
export function Brand() {
  return (
    <Link href="/" className="rp-brand" aria-label="RelayPine home">
      <BrandMark />
      <span>{t.brand}</span>
    </Link>
  );
}
const subscribeToHydration = () => () => {};

export function ThemeToggle() {
  const { resolvedTheme, setTheme } = useTheme();
  const hydrated = useSyncExternalStore(
    subscribeToHydration,
    () => true,
    () => false,
  );
  const label = hydrated && resolvedTheme === "dark" ? t.lightMode : t.darkMode;
  return (
    <Tooltip content={label}>
      <Button
        isIconOnly
        variant="light"
        aria-label={label}
        onPress={() => setTheme(resolvedTheme === "dark" ? "light" : "dark")}
      >
        <Sun size={17} className="hidden dark:block" />
        <Moon size={17} className="dark:hidden" />
      </Button>
    </Tooltip>
  );
}
export function CopyButton({ value }: { value: string }) {
  const [copied, setCopied] = useState(false);
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
            setCopied(true);
            setTimeout(() => setCopied(false), 1800);
            addToast({ title: t.copied, color: "success" });
          } catch {
            addToast({ title: t.copyFailed, color: "danger" });
          }
        }}
      >
        {copied ? <Check size={16} /> : <Copy size={16} />}
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
    <Chip radius="sm" className={`rp-status ${enabled ? "is-enabled" : ""}`} variant="flat" size="sm" startContent={<span className="rp-status-indicator" aria-hidden="true" />}>
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
    <div className="rp-empty flex min-h-60 flex-col items-center justify-center px-5 py-10 text-center">
      <div className="rp-empty-icon">
        <Inbox className="size-7" strokeWidth={1.25} />
      </div>
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
      className="rounded-[3px] border border-danger-200 bg-danger-50 p-4 text-sm text-danger-700 dark:text-danger-300"
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
    <div className="rp-page-header">
      <div>
        <h1>
          {title}
        </h1>
        <p className="mt-2 max-w-2xl text-sm leading-6 text-default-500">
          {description}
        </p>
      </div>
      {action && <div className="rp-page-actions">{action}</div>}
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
    <div className="rp-data-table">
      {rows.length ? (
        <div
          className="rp-table-scroll"
          tabIndex={0}
          role="region"
          aria-label={caption}
        >
          <table className="route-table w-full text-sm">
            <caption className="sr-only">{caption}</caption>
            <thead className="text-xs text-default-500">
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
      classNames={{
        base: "rp-modal max-h-[85dvh]",
        backdrop: "bg-[#080d16]/55",
      }}
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
function HighlightLine({ line }: { line: string }) {
  const pieces = line.split(
    /("[^"\n]*"|'[^'\n]*'|\b(?:import|from|const|await|new|process|env)\b|\b\d+\b)/g,
  );
  return (
    <>
      {pieces.map((piece, i) => (
        <span
          key={i}
          className={
            /^["']/.test(piece)
              ? "rp-syntax-string"
              : /^(import|from|const|await|new)$/.test(piece)
                ? "rp-syntax-keyword"
                : /^(process|env)$/.test(piece)
                  ? "rp-syntax-variable"
                  : /^\d+$/.test(piece)
                    ? "rp-syntax-number"
                    : undefined
          }
        >
          {piece}
        </span>
      ))}
    </>
  );
}
export function CodeBlock({
  code,
  label,
  tone = "default",
}: {
  code: string;
  label: string;
  tone?: "default" | "ink";
}) {
  return (
    <div className={`rp-code ${tone === "ink" ? "rp-code-ink" : ""}`}>
      <div className="rp-code-toolbar">
        <span className="rp-code-file">
          <span aria-hidden="true">{"{ }"}</span>
          {label}
        </span>
        <CopyButton value={code} />
      </div>
      <pre
        className="rp-code-body"
        tabIndex={0}
        role="region"
        aria-label={label}
      >
        <code>
          {code.split("\n").map((line, i) => (
            <span className="rp-code-line" key={i}>
              <span className="rp-line-number" aria-hidden="true">
                {i + 1}
              </span>
              <span>
                <HighlightLine line={line} />
                {i < code.split("\n").length - 1 ? "\n" : ""}
              </span>
            </span>
          ))}
        </code>
      </pre>
    </div>
  );
}
