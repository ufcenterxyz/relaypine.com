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
  Select,
  SelectItem,
  Switch,
} from "@heroui/react";
import { Check, RefreshCw, KeyRound } from "lucide-react";
import { en as t } from "@/i18n/en";
import { pricingEn as p } from "@/i18n/pricing-en";
import { api, count, dollars, errorMessage } from "@/lib/api";
import type { Credential, Endpoint, Proxy } from "@/lib/types";
import { ErrorBox } from "./shared";

interface CredentialDefaults {
  name: string;
  quota_group: string;
  rpm: number;
  tpm: number;
  max_concurrency: number;
  budget_micros: number;
  priority: number;
  enabled: boolean;
}
interface Discovery {
  status: "verified" | "workspace_required";
  models: { id: string; name: string; max_output_tokens?: number }[];
  workspaces: { id: string; name: string }[];
  workspace_id: string;
  organization_id: string;
  defaults: CredentialDefaults;
  limits_source: "provider" | "default";
  warnings: string[];
  checked_at: string;
}
interface Values {
  endpoint_id: string;
  secret: string;
  workspace_id: string;
  proxy_id: string;
  name: string;
  quota_group: string;
  rpm: string;
  tpm: string;
  max_concurrency: string;
  budget: string;
  priority: string;
  enabled: boolean;
}
type AdvancedField = Exclude<
  keyof Values,
  "endpoint_id" | "secret" | "workspace_id" | "proxy_id"
>;
const advancedFields: AdvancedField[] = [
  "name",
  "quota_group",
  "rpm",
  "tpm",
  "max_concurrency",
  "budget",
  "priority",
  "enabled",
];
const numericFields = [
  { key: "rpm", label: t.rpm, min: 1, max: 100000 },
  { key: "tpm", label: t.tpm, min: 1, max: 100000000 },
  { key: "max_concurrency", label: t.concurrency, min: 1, max: 1000 },
  { key: "budget", label: t.budget, min: 0, max: 1000000 },
  { key: "priority", label: t.priority, min: 0, max: 1000 },
] as const;

function fallback(endpoint?: Endpoint): CredentialDefaults {
  const name = Array.from(
    (endpoint?.name || endpoint?.provider || t.credential).trim(),
  );
  const suffix = ` ${t.credentialNameSuffix}`;
  while (new TextEncoder().encode(name.join("") + suffix).length > 120) name.pop();
  return {
    name: name.join("") + suffix,
    quota_group: `auto:${endpoint?.id || ""}`,
    rpm: 60,
    tpm: 100000,
    max_concurrency: 10,
    budget_micros: 0,
    priority: 0,
    enabled: true,
  };
}
function fromDefaults(
  defaults: CredentialDefaults,
): Pick<Values, AdvancedField> {
  return {
    name: defaults.name,
    quota_group: defaults.quota_group,
    rpm: String(defaults.rpm),
    tpm: String(defaults.tpm),
    max_concurrency: String(defaults.max_concurrency),
    budget: String(defaults.budget_micros / 1e6),
    priority: String(defaults.priority),
    enabled: defaults.enabled,
  };
}
function official(endpoint: Endpoint) {
  try {
    const host = new URL(endpoint.base_url).hostname;
    return endpoint.provider === "openai"
      ? host === "api.openai.com"
      : endpoint.provider === "anthropic" && host === "api.anthropic.com";
  } catch {
    return false;
  }
}

export function CredentialForm({
  credential,
  endpoints,
  proxies,
  onSave,
  onClose,
  onAddHost,
}: {
  credential?: Credential;
  endpoints: Endpoint[];
  proxies: Proxy[];
  onSave: (body: unknown) => Promise<void>;
  onClose: () => void;
  onAddHost: () => void;
}) {
  const initialEndpoint =
    endpoints.find((endpoint) => endpoint.id === credential?.endpoint_id) ||
    endpoints.find((endpoint) => endpoint.enabled && official(endpoint)) ||
    endpoints.find((endpoint) => endpoint.enabled) ||
    endpoints[0];
  const [values, setValues] = useState<Values>(() => ({
    ...fromDefaults(credential || fallback(initialEndpoint)),
    endpoint_id: credential?.endpoint_id || initialEndpoint?.id || "",
    secret: "",
    workspace_id: credential?.workspace_id || "",
    proxy_id: credential?.proxy_id || "direct",
  }));
  const [result, setResult] = useState<Discovery | null>(null);
  const [workspaces, setWorkspaces] = useState<Discovery["workspaces"]>([]);
  const [workspaceRequired, setWorkspaceRequired] = useState(false);
  const [phase, setPhase] = useState<"idle" | "verify" | "save">("idle");
  const [error, setError] = useState("");
  const errorElement = useRef<HTMLDivElement | null>(null);
  const [errors, setErrors] = useState<Partial<Record<keyof Values, string>>>(
    {},
  );
  const [advanced, setAdvanced] = useState(false);
  const [imported, setImported] = useState(false);
  const [slow, setSlow] = useState(false);
  const touched = useRef(new Set<keyof Values>());
  const pending = useRef<AbortController | null>(null);
  const revision = useRef(0);
  const busy = useRef(false);
  const autoWorkspace = useRef(false);
  const endpoint = endpoints.find((item) => item.id === values.endpoint_id);
  const anthropic = endpoint?.provider === "anthropic";
  const verified = result?.status === "verified";
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
  function mergeUntouched(current: Values, defaults: CredentialDefaults) {
    const next = { ...current };
    const suggested = fromDefaults(defaults);
    for (const field of advancedFields) {
      if (!touched.current.has(field))
        Object.assign(next, { [field]: suggested[field] });
    }
    return next;
  }
  function update<K extends keyof Values>(key: K, value: Values[K]) {
    touched.current.add(key);
    const clearDiscoveredWorkspace = key === "secret" && autoWorkspace.current;
    if (
      key === "workspace_id" ||
      key === "endpoint_id" ||
      clearDiscoveredWorkspace
    )
      autoWorkspace.current = false;
    const identityChanged = [
      "endpoint_id",
      "secret",
      "workspace_id",
      "proxy_id",
    ].includes(key);
    if (identityChanged) {
      revision.current += 1;
      pending.current?.abort();
      pending.current = null;
      busy.current = false;
      setPhase("idle");
      setSlow(false);
      setResult(null);
      setImported(false);
      if (key !== "workspace_id") {
        setWorkspaces([]);
        setWorkspaceRequired(false);
      }
    }
    setError("");
    setErrors((current) => ({ ...current, [key]: undefined }));
    setValues((current) => {
      let next = { ...current, [key]: value };
      if (key === "endpoint_id" || clearDiscoveredWorkspace)
        next.workspace_id = "";
      if (identityChanged && !credential)
        next = mergeUntouched(
          next,
          fallback(endpoints.find((item) => item.id === next.endpoint_id)),
        );
      return next;
    });
  }
  function validate(manual: boolean) {
    const next: Partial<Record<keyof Values, string>> = {};
    if (!endpoint) next.endpoint_id = t.required;
    if (
      (!credential || credential.endpoint_id !== values.endpoint_id) &&
      !values.secret.trim()
    )
      next.secret = credential
        ? t.credentialChangedHostSecret
        : t.secretRequired;
    const workspace = values.workspace_id.trim();
    if (
      anthropic &&
      workspace &&
      (workspace.length > 128 || !/^wrkspc_[A-Za-z0-9]+$/.test(workspace))
    )
      next.workspace_id = t.invalidAnthropicWorkspace;
    if (anthropic && workspaceRequired && !workspace)
      next.workspace_id = t.credentialWorkspaceSelect;
    if (manual) {
      if (!values.name.trim()) next.name = t.required;
      if (!values.quota_group.trim()) next.quota_group = t.required;
      for (const field of numericFields) {
        const value = Number(values[field.key]);
        if (
          !values[field.key].trim() ||
          !Number.isFinite(value) ||
          value < field.min ||
          value > field.max ||
          (field.key !== "budget" && !Number.isInteger(value))
        )
          next[field.key] = t.credentialInvalidLimit;
      }
    }
    setErrors(next);
    if (
      Object.keys(next).some(
        (key) =>
          advancedFields.includes(key as AdvancedField) ||
          (key === "workspace_id" && !workspaceRequired),
      )
    )
      setAdvanced(true);
    return !Object.keys(next).length;
  }
  async function verify() {
    if (busy.current || !validate(false)) return;
    const version = ++revision.current;
    const controller = new AbortController();
    pending.current = controller;
    busy.current = true;
    setPhase("verify");
    setSlow(false);
    setError("");
    setResult(null);
    setImported(false);
    try {
      const data = await api<Discovery>(
        "/api/admin/credentials/discover",
        {
          ...(credential ? { id: credential.id } : {}),
          endpoint_id: values.endpoint_id,
          secret: values.secret.trim(),
          workspace_id: anthropic ? values.workspace_id.trim() : "",
          proxy_id: values.proxy_id === "direct" ? "" : values.proxy_id,
        },
        "POST",
        controller.signal,
      );
      if (version !== revision.current) return;
      if (data.status !== "verified" && data.status !== "workspace_required")
        throw new Error(t.credentialDiscoveryInvalid);
      setWorkspaces(data.workspaces || []);
      setWorkspaceRequired(data.status === "workspace_required");
      setResult(data);
      if (data.status === "verified") {
        autoWorkspace.current = Boolean(
          anthropic &&
            data.workspace_id &&
            (autoWorkspace.current ||
              !values.workspace_id.trim() ||
              data.workspace_id !== values.workspace_id.trim()),
        );
        setValues((current) => ({
          ...(credential ? current : mergeUntouched(current, data.defaults)),
          workspace_id: anthropic ? data.workspace_id || "" : "",
        }));
      }
    } catch (failure) {
      if (version === revision.current)
        setError(
          controller.signal.aborted
            ? t.credentialDiscoveryInterrupted
            : errorMessage(failure),
        );
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
        ...(credential || {}),
        name: values.name.trim(),
        endpoint_id: values.endpoint_id,
        secret: values.secret.trim(),
        workspace_id: anthropic ? values.workspace_id.trim() : "",
        proxy_id: values.proxy_id === "direct" ? "" : values.proxy_id,
        quota_group: values.quota_group.trim(),
        rpm: Number(values.rpm),
        tpm: Number(values.tpm),
        max_concurrency: Number(values.max_concurrency),
        budget_micros: dollars(values.budget),
        priority: Number(values.priority),
        enabled: values.enabled,
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
  function importLimits() {
    if (!result || !verified) return;
    const fields = ["quota_group", "rpm", "tpm", "max_concurrency"] as const;
    const suggested = fromDefaults(result.defaults);
    fields.forEach((field) => touched.current.add(field));
    if (!result.defaults.enabled) touched.current.add("enabled");
    setValues((current) => ({
      ...current,
      ...Object.fromEntries(fields.map((field) => [field, suggested[field]])),
      enabled: result.defaults.enabled ? current.enabled : false,
    }));
    setImported(true);
  }
  const workspaceControl = workspaces.length ? (
    <Select
      label={t.anthropicWorkspaceID}
      selectedKeys={values.workspace_id ? [values.workspace_id] : []}
      onChange={(event) => update("workspace_id", event.target.value)}
      description={t.credentialWorkspaceSelect}
      isDisabled={saving}
      isInvalid={!!errors.workspace_id}
      errorMessage={errors.workspace_id}
    >
      {workspaces.map((workspace) => (
        <SelectItem
          key={workspace.id}
          textValue={workspace.name || workspace.id}
          description={workspace.id}
        >
          {workspace.name || workspace.id}
        </SelectItem>
      ))}
    </Select>
  ) : (
    <Input
      label={t.anthropicWorkspaceID}
      value={values.workspace_id}
      onValueChange={(value) => update("workspace_id", value)}
      placeholder="wrkspc_…"
      description={t.anthropicWorkspaceHelp}
      isDisabled={saving}
      isInvalid={!!errors.workspace_id}
      errorMessage={errors.workspace_id}
    />
  );

  return (
    <Modal
      isOpen
      onClose={close}
      isDismissable={phase === "idle"}
      isKeyboardDismissDisabled={phase !== "idle"}
      hideCloseButton={phase !== "idle"}
      size="lg"
      scrollBehavior="inside"
      classNames={{ base: "rp-management-modal rp-management-credential-modal max-h-[88dvh]" }}
    >
      <ModalContent>
        <form
          noValidate
          className="flex min-h-0 flex-col overflow-hidden"
          onSubmit={(event) => {
            event.preventDefault();
          void (credential || verified ? save() : verify());
          }}
        >
          <ModalHeader className="rp-management-modal-header flex-col gap-2">
            <span className="rp-management-modal-eyebrow"><KeyRound size={14} />{p.credentialConnection}</span>
            <h2>{credential ? t.editCredential : t.addCredential}</h2>
            <p className="text-sm font-normal leading-6 text-default-500">
              {credential ? t.credentialEditIntro : t.credentialAddIntro}
            </p>
          </ModalHeader>
          <ModalBody className="rp-management-modal-body gap-4 overflow-y-auto">
            {error && (
              <div ref={errorElement}>
                <ErrorBox message={error} />
              </div>
            )}
            {!endpoints.length ? (
              <div className="rounded-[3px] border border-default-200 p-4">
                <p className="text-sm leading-6 text-default-500">
                  {t.noHostsBody}
                </p>
                <Button className="mt-3" variant="flat" onPress={onAddHost}>
                  {t.addHost}
                </Button>
              </div>
            ) : (
              <>
                <Select
                  autoFocus
                  label={t.credentialPlatformHost}
                  variant="bordered"
                  selectedKeys={values.endpoint_id ? [values.endpoint_id] : []}
                  onChange={(event) =>
                    update("endpoint_id", event.target.value)
                  }
                  description={endpoint?.base_url}
                  isDisabled={saving}
                  isInvalid={!!errors.endpoint_id}
                  errorMessage={errors.endpoint_id}
                >
                  {endpoints.map((item) => (
                    <SelectItem
                      key={item.id}
                      textValue={`${item.provider === "anthropic" ? t.anthropic : t.openai} · ${item.name}`}
                      description={item.base_url}
                    >
                      {item.provider === "anthropic" ? t.anthropic : t.openai} ·{" "}
                      {item.name}
                    </SelectItem>
                  ))}
                </Select>
                <Input
                  label={credential ? t.rotateSecret : t.secret}
                  variant="bordered"
                  type="password"
                  autoComplete="new-password"
                  value={values.secret}
                  onValueChange={(value) => update("secret", value)}
                  description={
                    credential
                      ? credential.endpoint_id !== values.endpoint_id
                        ? t.credentialChangedHostSecret
                        : t.preserveSecret
                      : t.protectedSecret
                  }
                  isDisabled={saving}
                  isInvalid={!!errors.secret}
                  errorMessage={errors.secret}
                />
                {anthropic && workspaceRequired && (
                  <div className="space-y-3 rounded-[3px] border border-warning-200 bg-warning-50 p-4">
                    <p className="text-sm leading-6">
                      {t.credentialWorkspaceRequired}
                    </p>
                    {workspaceControl}
                  </div>
                )}
                {verified && result && (
                  <section
                    aria-live="polite"
                    className="rp-management-verified space-y-4"
                  >
                    <div className="flex flex-wrap items-center justify-between gap-2">
                      <Chip radius="sm"
                        color="success"
                        variant="flat"
                        size="sm"
                        startContent={<Check size={14} />}
                      >
                        {t.credentialMetadataVerified}
                      </Chip>
                      <Button
                        type="button"
                        variant="light"
                        size="sm"
                        onPress={() => void verify()}
                        isLoading={phase === "verify"}
                        isDisabled={saving}
                        startContent={<RefreshCw size={14} />}
                      >
                        {t.credentialRecheck}
                      </Button>
                    </div>
                    <div className="rp-management-discovery-count">
                      <strong>{count(result.models.length)}</strong><span>{t.credentialModelsFound}</span>
                      <p className="mt-1 text-xs leading-5 text-default-500">
                        {t.credentialModelsMetadataOnly}
                      </p>
                    </div>
                    {!!result.models.length && (
                      <ul
                        aria-label={t.credentialAvailableModels}
                        className="rp-management-discovered-list"
                      >
                        {result.models.map((model) => (
                          <li
                            key={model.id}
                            className="flex flex-wrap justify-between gap-x-3 gap-y-1 text-xs"
                          >
                            <span>{model.name || model.id}</span>
                            <code className="break-all text-default-500">
                              {model.id}
                            </code>
                          </li>
                        ))}
                      </ul>
                    )}
                    <div className="rp-management-discovered-limits">
                      <p className="text-xs font-medium">
                        {result.limits_source === "provider"
                          ? t.credentialProviderLimits
                          : t.credentialDefaultLimits}
                      </p>
                      <div className="rp-management-limit-values"><div><strong>{count(result.defaults.rpm)}</strong><span>RPM</span></div><div><strong>{count(result.defaults.tpm)}</strong><span>TPM</span></div></div>
                      <p className="mt-2 text-xs leading-5 text-default-500">
                        {credential
                          ? t.credentialEditPreserved
                          : t.credentialUntouchedDefaults}
                      </p>
                      {credential && (
                        <>
                          <Button
                            type="button"
                            className="mt-2"
                            variant="bordered"
                            size="sm"
                            onPress={importLimits}
                            isDisabled={saving}
                          >
                            {imported
                              ? t.credentialLimitsImported
                              : t.credentialImportLimits}
                          </Button>
                          <p className="mt-2 text-xs leading-5 text-default-500">
                            {t.credentialImportLimitsHelp}
                          </p>
                        </>
                      )}
                    </div>
                    {!!result.warnings?.length && (
                      <ul className="space-y-1 text-xs leading-5 text-default-500">
                        {result.warnings.map((warning, index) => (
                          <li key={index}>{warning}</li>
                        ))}
                      </ul>
                    )}
                    {!result.defaults.enabled && (
                      <p className="text-xs leading-5 text-warning-700 dark:text-warning-400">
                        {t.credentialCapacityDisabled}
                      </p>
                    )}
                    <p className="text-xs leading-5 text-default-500">
                      {t.credentialVerificationScope}
                    </p>
                  </section>
                )}
                <Accordion
                  variant="light"
                  selectedKeys={advanced ? ["advanced"] : []}
                  onSelectionChange={(keys) =>
                    setAdvanced(keys === "all" || keys.has("advanced"))
                  }
                  className="rp-management-advanced px-0"
                  itemClasses={{
                    trigger: "py-3",
                    title: "text-sm font-medium",
                    content: "pb-2",
                  }}
                >
                  <AccordionItem
                    key="advanced"
                    aria-label={t.credentialAdvanced}
                    title={t.credentialAdvanced}
                  >
                    <div className="space-y-4">
                      {anthropic && !workspaceRequired && workspaceControl}
                      <Input
                        label={t.name}
                        value={values.name}
                        onValueChange={(value) => update("name", value)}
                        description={t.credentialAutoName}
                        isDisabled={saving}
                        isInvalid={!!errors.name}
                        errorMessage={errors.name}
                      />
                      <Select
                        label={t.proxy}
                        selectedKeys={[values.proxy_id]}
                        onChange={(event) =>
                          update("proxy_id", event.target.value || "direct")
                        }
                        isDisabled={saving}
                      >
                        {[{ id: "direct", name: t.direct }, ...proxies].map(
                          (proxy) => (
                            <SelectItem key={proxy.id}>{proxy.name}</SelectItem>
                          ),
                        )}
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
                            step={field.key === "budget" ? "any" : 1}
                            description={
                              field.key === "budget" ? t.budgetHelp : undefined
                            }
                            isDisabled={saving}
                            isInvalid={!!errors[field.key]}
                            errorMessage={errors[field.key]}
                          />
                        ))}
                      </div>
                      <Input
                        label={t.quotaGroup}
                        value={values.quota_group}
                        onValueChange={(value) => update("quota_group", value)}
                        description={t.credentialAutoQuota}
                        isDisabled={saving}
                        isInvalid={!!errors.quota_group}
                        errorMessage={errors.quota_group}
                      />
                      <Switch
                        isSelected={values.enabled}
                        onValueChange={(value) => update("enabled", value)}
                        isDisabled={saving}
                      >
                        {t.enabled}
                      </Switch>
                      <p className="text-xs leading-5 text-default-500">
                        {t.credentialLimitsCaveat}
                      </p>
                      {!credential && !verified && (
                        <div className="border-t border-default-200 pt-4">
                          <p className="text-xs leading-5 text-default-500">
                            {t.credentialManualHelp}
                          </p>
                          <Button
                            type="button"
                            variant="bordered"
                            size="sm"
                            className="mt-3"
                            isLoading={saving}
                            isDisabled={phase === "verify"}
                            onPress={() => void save()}
                          >
                            {t.credentialSaveUnverified}
                          </Button>
                        </div>
                      )}
                    </div>
                  </AccordionItem>
                </Accordion>
              </>
            )}
            {slow && (
              <p role="status" className="text-xs text-default-500">
                {t.stillWorking}
              </p>
            )}
          </ModalBody>
          <ModalFooter className="rp-management-modal-footer shrink-0">
            <Button
              variant="light"
              isDisabled={phase !== "idle"}
              onPress={close}
            >
              {t.cancel}
            </Button>
            {credential && !verified && !!endpoints.length && (
              <Button
                type="button"
                variant="bordered"
                isLoading={phase === "verify"}
                isDisabled={saving}
                onPress={() => void verify()}
              >
                {t.credentialVerifyKey}
              </Button>
            )}
            {!!endpoints.length && (
              <Button
                type="submit"
                color="primary"
                isLoading={credential ? saving : phase !== "idle"}
                isDisabled={!!credential && phase === "verify"}
              >
                {credential ? t.save : verified ? t.addCredential : t.credentialVerifyKey}
              </Button>
            )}
          </ModalFooter>
        </form>
      </ModalContent>
    </Modal>
  );
}
