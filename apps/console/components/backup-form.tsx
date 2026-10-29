"use client";

import { useEffect, useRef, useState } from "react";
import {
  Accordion,
  AccordionItem,
  Button,
  Chip,
  Input,
  Modal,
  ModalBody,
  ModalContent,
  ModalFooter,
  ModalHeader,
  Radio,
  RadioGroup,
  Select,
  SelectItem,
  Switch,
} from "@heroui/react";
import { Check, LifeBuoy, RefreshCw } from "lucide-react";
import { en as t } from "@/i18n/en";
import { backupEn as b } from "@/i18n/backup-en";
import { api, count, errorMessage, money } from "@/lib/api";
import { backupVendors, type BackupRoute } from "@/lib/backups";
import type { BackupCheck, BackupVendor, Proxy } from "@/lib/types";
import { ErrorBox } from "./shared";

interface Values {
  vendor: BackupVendor;
  secret: string;
  management_key: string;
  remove_management_key: boolean;
  proxy_id: string;
  priority: string;
  rpm: string;
  tpm: string;
  max_concurrency: string;
  enabled: boolean;
}
type NumericField = "priority" | "rpm" | "tpm" | "max_concurrency";
const numericFields: { key: NumericField; label: string; min: number; max: number }[] = [
  { key: "priority", label: b.order, min: 0, max: 1000 },
  { key: "rpm", label: t.rpm, min: 1, max: 100000 },
  { key: "tpm", label: t.tpm, min: 1, max: 100000000 },
  { key: "max_concurrency", label: t.concurrency, min: 1, max: 1000 },
];

export function BackupForm({
  backup,
  configured,
  proxies,
  onSave,
  onClose,
}: {
  backup?: BackupRoute;
  configured: BackupVendor[];
  proxies: Proxy[];
  onSave: (body: unknown) => Promise<void>;
  onClose: () => void;
}) {
  const choices = backupVendors.filter((vendor) => !configured.includes(vendor.id));
  const [values, setValues] = useState<Values>(() => ({
    vendor: backup?.vendor ?? choices[0]?.id ?? "openrouter",
    secret: "",
    management_key: "",
    remove_management_key: false,
    proxy_id: backup?.credential?.proxy_id || "direct",
    // Added first, tried first: a later backup starts below the existing one.
    priority: String(backup?.credential?.priority ?? (configured.length ? 0 : 1)),
    rpm: String(backup?.credential?.rpm ?? 60),
    tpm: String(backup?.credential?.tpm ?? 100000),
    max_concurrency: String(backup?.credential?.max_concurrency ?? 10),
    enabled: backup ? !!backup.credential?.enabled && backup.endpoint.enabled : true,
  }));
  const [result, setResult] = useState<BackupCheck | null>(null);
  const [phase, setPhase] = useState<"idle" | "check" | "save">("idle");
  const [error, setError] = useState("");
  const [errors, setErrors] = useState<Partial<Record<keyof Values, string>>>({});
  const [advanced, setAdvanced] = useState(false);
  const [slow, setSlow] = useState(false);
  const busy = useRef(false);
  const revision = useRef(0);
  const pending = useRef<AbortController | null>(null);
  const errorElement = useRef<HTMLDivElement | null>(null);
  const vendor = backupVendors.find((item) => item.id === values.vendor);
  const checked = !!result;
  const saving = phase === "save";

  useEffect(() => {
    if (error) errorElement.current?.scrollIntoView({ block: "nearest" });
  }, [error]);
  useEffect(
    () => () => {
      revision.current += 1;
      pending.current?.abort();
    },
    [],
  );
  useEffect(() => {
    if (phase === "idle") return;
    const timer = setTimeout(() => setSlow(true), 10000);
    return () => clearTimeout(timer);
  }, [phase]);

  function close() {
    if (!busy.current) onClose();
  }
  function update<K extends keyof Values>(key: K, value: Values[K]) {
    // A different key, provider or exit invalidates the previous check.
    if (["vendor", "secret", "proxy_id", "management_key", "remove_management_key"].includes(key)) {
      revision.current += 1;
      pending.current?.abort();
      pending.current = null;
      busy.current = false;
      setPhase("idle");
      setSlow(false);
      setResult(null);
    }
    setError("");
    setErrors((current) => ({ ...current, [key]: undefined }));
    setValues((current) => ({ ...current, [key]: value }));
  }
  function validate(full: boolean) {
    const next: Partial<Record<keyof Values, string>> = {};
    if (!backup && !values.secret.trim()) next.secret = b.keyRequired;
    if (full) {
      for (const field of numericFields) {
        const value = Number(values[field.key]);
        if (
          !values[field.key].trim() ||
          !Number.isInteger(value) ||
          value < field.min ||
          value > field.max
        )
          next[field.key] = b.invalidLimit;
      }
    }
    setErrors(next);
    if (numericFields.some((field) => next[field.key])) setAdvanced(true);
    return !Object.keys(next).length;
  }
  async function check() {
    if (busy.current || !validate(false)) return;
    const version = ++revision.current;
    const controller = new AbortController();
    pending.current = controller;
    busy.current = true;
    setPhase("check");
    setSlow(false);
    setError("");
    setResult(null);
    try {
      const data = await api<BackupCheck>(
        "/api/admin/backups/check",
        {
          vendor: values.vendor,
          secret: values.secret.trim(),
          management_key: values.remove_management_key ? "" : values.management_key.trim(),
          proxy_id: values.proxy_id === "direct" ? "" : values.proxy_id,
        },
        "POST",
        controller.signal,
      );
      if (version === revision.current) setResult(data);
    } catch (failure) {
      if (version === revision.current && !controller.signal.aborted)
        setError(errorMessage(failure));
    } finally {
      if (version === revision.current) {
        busy.current = false;
        pending.current = null;
        setPhase("idle");
        setSlow(false);
      }
    }
  }
  async function save() {
    if (busy.current || !validate(true)) return;
    busy.current = true;
    setPhase("save");
    setSlow(false);
    setError("");
    try {
      await onSave({
        vendor: values.vendor,
        secret: values.secret.trim(),
        management_key: values.remove_management_key ? "" : values.management_key.trim(),
        remove_management_key: values.remove_management_key,
        proxy_id: values.proxy_id === "direct" ? "" : values.proxy_id,
        enabled: values.enabled,
        priority: Number(values.priority),
        rpm: Number(values.rpm),
        tpm: Number(values.tpm),
        max_concurrency: Number(values.max_concurrency),
      });
      onClose();
    } catch (failure) {
      setError(errorMessage(failure));
    } finally {
      busy.current = false;
      setPhase("idle");
      setSlow(false);
    }
  }

  const enabledModels = result?.models.filter((model) => model.enabled) ?? [];
  const accountBalance = values.remove_management_key ? undefined : result?.balance.balance_micros;
  const managementSaved = !!backup?.endpoint.management_key;
  const covered = enabledModels.filter((model) => model.available).length;

  return (
    <Modal
      isOpen
      onClose={close}
      isDismissable={phase === "idle"}
      isKeyboardDismissDisabled={phase !== "idle"}
      hideCloseButton={phase !== "idle"}
      size="lg"
      scrollBehavior="inside"
      classNames={{ base: "rp-management-modal max-h-[88dvh]" }}
    >
      <ModalContent>
        <form
          noValidate
          className="flex min-h-0 flex-col overflow-hidden"
          onSubmit={(event) => {
            event.preventDefault();
            void (backup || checked ? save() : check());
          }}
        >
          <ModalHeader className="rp-management-modal-header flex-col gap-2">
            <span className="rp-management-modal-eyebrow">
              <LifeBuoy size={14} />
              {backup ? `${b.eyebrow} · ${backup.name}` : b.eyebrow}
            </span>
            <h2>{backup ? b.edit : b.add}</h2>
            <p className="text-sm font-normal leading-6 text-default-500">
              {backup ? b.editIntro : b.addIntro}
            </p>
          </ModalHeader>
          <ModalBody className="rp-management-modal-body gap-4 overflow-y-auto">
            {error && (
              <div ref={errorElement}>
                <ErrorBox message={error} />
              </div>
            )}
            {!backup && (
              <RadioGroup
                label={b.vendor}
                value={values.vendor}
                onValueChange={(value) => update("vendor", value as BackupVendor)}
                isDisabled={saving}
                classNames={{ wrapper: "rp-console-strategy-options", label: "text-xs" }}
              >
                {backupVendors.map((item) => (
                  <Radio
                    key={item.id}
                    value={item.id}
                    isDisabled={configured.includes(item.id)}
                    description={
                      configured.includes(item.id) ? b.alreadyAdded : b.vendorHelp[item.id]
                    }
                    classNames={{
                      base: "rp-console-strategy-option",
                      label: "text-sm font-medium",
                      description: "text-xs leading-6",
                      labelWrapper: "ml-2",
                    }}
                  >
                    {item.name}
                  </Radio>
                ))}
              </RadioGroup>
            )}
            <Input
              autoFocus
              label={backup ? b.replaceKey : `${vendor?.name ?? ""} ${b.apiKey}`.trim()}
              variant="bordered"
              type="password"
              autoComplete="new-password"
              value={values.secret}
              onValueChange={(value) => update("secret", value)}
              description={backup ? b.keepKey : b.keyHelp}
              isDisabled={saving}
              isInvalid={!!errors.secret}
              errorMessage={errors.secret}
            />
            {result && (
              <section aria-live="polite" className="rp-management-verified space-y-4">
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <Chip
                    radius="sm"
                    color="success"
                    variant="flat"
                    size="sm"
                    startContent={<Check size={14} />}
                  >
                    {b.keyAccepted}
                  </Chip>
                  <Button
                    type="button"
                    variant="light"
                    size="sm"
                    onPress={() => void check()}
                    isLoading={phase === "check"}
                    isDisabled={saving}
                    startContent={phase === "check" ? undefined : <RefreshCw size={14} />}
                  >
                    {b.recheck}
                  </Button>
                </div>
                {(result.key_label || accountBalance !== undefined || result.balance.key_limit_remaining_micros !== undefined) && (
                  <dl className="grid grid-cols-2 gap-3 text-xs">
                    {accountBalance !== undefined && (
                      <div>
                        <dt className="text-default-500">{b.accountBalance}</dt>
                        <dd className="mt-1 text-sm font-medium tabular-nums">{money(accountBalance)}</dd>
                      </div>
                    )}
                    {result.balance.key_limit_remaining_micros !== undefined && (
                      <div>
                        <dt className="text-default-500">{b.keyLimit}</dt>
                        <dd className="mt-1 tabular-nums">{money(result.balance.key_limit_remaining_micros)}</dd>
                      </div>
                    )}
                    {result.key_label && (
                      <div>
                        <dt className="text-default-500">{b.keyLabel}</dt>
                        <dd className="mt-1 break-all">{result.key_label}</dd>
                      </div>
                    )}
                  </dl>
                )}
                {!values.remove_management_key && result.balance.error && (
                  <p role="alert" className="text-xs leading-5 text-warning-700 dark:text-warning-400">
                    {result.balance.error}
                  </p>
                )}
                {enabledModels.length ? (
                  <>
                    <div className="rp-management-discovery-count">
                      <strong>{count(covered)}</strong>
                      <span>{b.coverage.replace("{total}", count(enabledModels.length))}</span>
                      <p className="mt-1 text-xs leading-5 text-default-500">{b.coverageHelp}</p>
                    </div>
                    <ul aria-label={b.coverage.replace("{total}", count(enabledModels.length))} className="rp-management-discovered-list">
                      {enabledModels.map((model) => (
                        <li key={model.id} className="flex flex-wrap justify-between gap-x-3 gap-y-1 text-xs">
                          <span>{model.name || model.id}</span>
                          <code className={`break-all ${model.available ? "text-default-500" : "text-default-400"}`}>
                            {model.available
                              ? model.backup_model
                              : model.backup_model
                                ? b.notOffered
                                : b.noMatch}
                          </code>
                        </li>
                      ))}
                    </ul>
                  </>
                ) : (
                  <p className="text-xs leading-5 text-default-500">{b.coverageEmpty}</p>
                )}
                <p className="text-xs leading-5 text-default-500">{b.checkScope}</p>
              </section>
            )}
            <Accordion
              variant="light"
              selectedKeys={advanced ? ["advanced"] : []}
              onSelectionChange={(keys) => setAdvanced(keys === "all" || keys.has("advanced"))}
              className="rp-management-advanced px-0"
              itemClasses={{ trigger: "py-3", title: "text-sm font-medium", content: "pb-2" }}
            >
              <AccordionItem key="advanced" aria-label={b.advanced} title={b.advanced}>
                <div className="space-y-4">
                  <Select
                    label={t.proxy}
                    selectedKeys={[values.proxy_id]}
                    onChange={(event) => update("proxy_id", event.target.value || "direct")}
                    isDisabled={saving}
                  >
                    {[{ id: "direct", name: t.direct }, ...proxies].map((proxy) => (
                      <SelectItem key={proxy.id}>{proxy.name}</SelectItem>
                    ))}
                  </Select>
                  <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
                    {numericFields.map((field) => (
                      <Input
                        key={field.key}
                        type="number"
                        label={field.label}
                        value={values[field.key]}
                        onValueChange={(value) => update(field.key, value)}
                        min={field.min}
                        max={field.max}
                        step={1}
                        description={field.key === "priority" ? b.orderHelp : undefined}
                        isDisabled={saving}
                        isInvalid={!!errors[field.key]}
                        errorMessage={errors[field.key]}
                      />
                    ))}
                  </div>
                  <p className="text-xs leading-5 text-default-500">{b.limitsHelp}</p>
                  <div>
                    <Switch
                      isSelected={values.enabled}
                      onValueChange={(value) => update("enabled", value)}
                      isDisabled={saving}
                    >
                      {t.enabled}
                    </Switch>
                  </div>
                  <Input
                    label={b.managementKey}
                    type="password"
                    autoComplete="new-password"
                    value={values.management_key}
                    onValueChange={(value) => update("management_key", value)}
                    description={
                      managementSaved
                        ? `${b.managementKeySaved} ${b.managementKeyHelp[values.vendor]}`
                        : b.managementKeyHelp[values.vendor]
                    }
                    isDisabled={saving || values.remove_management_key}
                  />
                  {managementSaved && (
                    <div>
                      <Switch
                        size="sm"
                        isSelected={values.remove_management_key}
                        onValueChange={(value) => update("remove_management_key", value)}
                        isDisabled={saving}
                      >
                        <span className="text-sm">{b.removeManagementKey}</span>
                      </Switch>
                    </div>
                  )}
                  {!backup && !checked && (
                    <div className="border-t border-default-200 pt-4">
                      <p className="text-xs leading-5 text-default-500">{b.saveUncheckedHelp}</p>
                      <Button
                        type="button"
                        variant="bordered"
                        size="sm"
                        className="mt-3"
                        isLoading={saving}
                        isDisabled={phase === "check"}
                        onPress={() => void save()}
                      >
                        {b.saveUnchecked}
                      </Button>
                    </div>
                  )}
                </div>
              </AccordionItem>
            </Accordion>
            {slow && (
              <p role="status" className="text-xs text-default-500">
                {t.stillWorking}
              </p>
            )}
          </ModalBody>
          <ModalFooter className="rp-management-modal-footer shrink-0">
            <Button variant="light" isDisabled={phase !== "idle"} onPress={close}>
              {t.cancel}
            </Button>
            {backup && (
              <Button
                type="button"
                variant="bordered"
                isLoading={phase === "check"}
                isDisabled={saving}
                onPress={() => void check()}
              >
                {checked ? b.recheck : b.check}
              </Button>
            )}
            <Button
              type="submit"
              color="primary"
              isLoading={backup || checked ? saving : phase === "check"}
              isDisabled={backup ? phase === "check" : saving}
            >
              {backup ? t.save : checked ? b.add : b.check}
            </Button>
          </ModalFooter>
        </form>
      </ModalContent>
    </Modal>
  );
}
