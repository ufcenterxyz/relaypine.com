"use client";

import { useEffect, useRef, useState } from "react";
import Link from "next/link";
import {
  Button,
  Input,
  Modal,
  ModalBody,
  ModalContent,
  ModalFooter,
  ModalHeader,
  Select,
  SelectItem,
  addToast,
} from "@heroui/react";
import { en as t } from "@/i18n/en";
import { api, count, errorMessage } from "@/lib/api";
import type { Credential, Endpoint, Model } from "@/lib/types";
import { ErrorBox } from "./shared";
import { ModelImportSelection, type ModelImportOption } from "./model-import-selection";

interface DiscoveredModel {
  id: string;
  name: string;
  max_output_tokens?: number;
}
interface Discovery {
  status: "verified" | "workspace_required";
  models: DiscoveredModel[];
  workspace_id: string;
  workspaces: { id: string; name: string }[];
}
function validDiscovery(value: unknown): value is Discovery {
  if (!value || typeof value !== "object") return false;
  const data = value as Record<string, unknown>;
  if (
    !["verified", "workspace_required"].includes(String(data.status)) ||
    typeof data.workspace_id !== "string" ||
    (data.workspace_id !== "" &&
      !/^wrkspc_[A-Za-z0-9]+$/.test(data.workspace_id))
  )
    return false;
  if (!Array.isArray(data.models) || !Array.isArray(data.workspaces))
    return false;
  const validModels = data.models.every((model: unknown) => {
    if (!model || typeof model !== "object") return false;
    const item = model as Record<string, unknown>;
    return (
      typeof item.id === "string" &&
      item.id.trim().length > 0 &&
      typeof item.name === "string" &&
      (item.max_output_tokens === undefined ||
        (typeof item.max_output_tokens === "number" &&
          Number.isFinite(item.max_output_tokens)))
    );
  });
  const validWorkspaces = data.workspaces.every((workspace: unknown) => {
    if (!workspace || typeof workspace !== "object") return false;
    const item = workspace as Record<string, unknown>;
    return (
      typeof item.id === "string" &&
      /^wrkspc_[A-Za-z0-9]+$/.test(item.id) &&
      typeof item.name === "string"
    );
  });
  return (
    validModels &&
    validWorkspaces &&
    new Set(data.models.map((model) => model.id)).size === data.models.length &&
    new Set(data.workspaces.map((workspace) => workspace.id)).size ===
      data.workspaces.length
  );
}

interface ImportReceipt {
  created_count: number;
  skipped_count: number;
  results: { id: string; status: "created" | "skipped"; reason?: string }[];
}
function validReceipt(value: unknown, ids: string[]): value is ImportReceipt {
  if (!value || typeof value !== "object") return false;
  const data = value as ImportReceipt;
  if (!Number.isInteger(data.created_count) || !Number.isInteger(data.skipped_count) ||
      data.created_count < 0 || data.skipped_count < 0 || !Array.isArray(data.results)) return false;
  const requested = new Set(ids);
  return data.results.length === requested.size &&
    data.results.every((item) => item && requested.has(item.id) &&
      (item.status === "created" || item.status === "skipped")) &&
    new Set(data.results.map((item) => item.id)).size === requested.size &&
    data.results.filter((item) => item.status === "created").length === data.created_count &&
    data.results.filter((item) => item.status === "skipped").length === data.skipped_count;
}
const byteLength = (value: string) => new TextEncoder().encode(value).length;
const importSummary = (receipt: ImportReceipt) => t.modelImportSummary
  .replace("{created}", count(receipt.created_count))
  .replace("{skipped}", count(receipt.skipped_count));

export function ModelImport({
  credentials,
  endpoints,
  models,
  onImported,
  onClose,
  onCredentialSaved,
}: {
  credentials: Credential[];
  endpoints: Endpoint[];
  models: Model[];
  onImported: () => Promise<void>;
  onClose: () => void;
  onCredentialSaved: () => Promise<void>;
}) {
  const eligible = credentials.filter(
    (credential) =>
      credential.enabled &&
      endpoints.some(
        (endpoint) =>
          endpoint.id === credential.endpoint_id && endpoint.enabled,
      ),
  );
  const [credentialID, setCredentialID] = useState(eligible[0]?.id || "");
  const [result, setResult] = useState<Discovery | null>(null);
  const [selected, setSelected] = useState<string[]>([]);
  const [receipt, setReceipt] = useState<ImportReceipt | null>(null);
  const [importing, setImporting] = useState(false);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [slow, setSlow] = useState(false);
  const [workspace, setWorkspace] = useState(eligible[0]?.workspace_id || "");
  const [savedWorkspace, setSavedWorkspace] = useState<string | null>(null);
  const [savingWorkspace, setSavingWorkspace] = useState(false);
  const pending = useRef<AbortController | null>(null);
  const revision = useRef(0);
  const busy = useRef(false);
  const errorBlock = useRef<HTMLDivElement | null>(null);
  const credential = eligible.find((item) => item.id === credentialID);
  const endpoint = endpoints.find(
    (item) => item.id === credential?.endpoint_id,
  );
  const existing = new Set(models.map((model) => model.id));
  const storedWorkspace = savedWorkspace ?? credential?.workspace_id ?? "";
  const scopeChanged =
    !!result &&
    (result.status === "workspace_required" ||
      (endpoint?.provider === "anthropic" &&
        (result.workspace_id || "") !== storedWorkspace));
  const canSaveScope =
    result?.status === "verified" && !!result.workspace_id && !storedWorkspace;
  const allowed = (model: DiscoveredModel) =>
    !existing.has(model.id) &&
    (!endpoint?.models.length ||
      endpoint.models.includes("*") ||
      endpoint.models.includes(model.id));
  const options: ModelImportOption[] = (result?.models || []).map((model) => ({
    id: model.id,
    name: model.name.trim() || model.id,
    unavailable: existing.has(model.id) ? "configured" : !allowed(model) ? "restricted" :
      byteLength(model.id) > 64 ||
      byteLength(model.name.trim() || model.id) > 120 ? "alias_required" : undefined,
  }));
  const selectedModels = (result?.models || []).filter((model) => selected.includes(model.id));
  const budgetBlocked = !!credential && credential.budget_micros > 0;


  useEffect(
    () => () => {
      revision.current += 1;
      pending.current?.abort();
    },
    [],
  );
  useEffect(() => {
    if (error)
      errorBlock.current?.scrollIntoView({
        block: "nearest",
        behavior: "instant",
      });
  }, [error]);
  useEffect(() => {
    if (!loading) return;
    const timer = setTimeout(() => setSlow(true), 10000);
    return () => clearTimeout(timer);
  }, [loading]);
  function changeCredential(value: string) {
    if (
      !eligible.some((item) => item.id === value) ||
      (busy.current && !pending.current) || !!receipt
    )
      return;
    revision.current += 1;
    pending.current?.abort();
    pending.current = null;
    busy.current = false;
    setCredentialID(value);
    setResult(null);
    setSelected([]);
    setError("");
    setLoading(false);
    setSlow(false);
    setWorkspace(
      eligible.find((item) => item.id === value)?.workspace_id || "",
    );
    setSavedWorkspace(null);
  }
  function close() {
    if (!busy.current) onClose();
  }
  async function discover() {
    if (busy.current) return;
    if (!credential || !endpoint) {
      setError(t.modelImportChooseCredential);
      return;
    }
    if (result?.status === "workspace_required" && !workspace) {
      setError(t.credentialWorkspaceSelect);
      return;
    }
    const version = ++revision.current;
    const controller = new AbortController();
    pending.current = controller;
    busy.current = true;
    setLoading(true);
    setSlow(false);
    setError("");
    setResult(null);
    setSelected([]);
    try {
      const data = await api<unknown>(
        "/api/admin/credentials/discover",
        {
          id: credential.id,
          endpoint_id: credential.endpoint_id,
          secret: "",
          workspace_id: workspace,
          proxy_id: credential.proxy_id || "",
        },
        "POST",
        controller.signal,
      );
      if (version !== revision.current) return;
      if (!validDiscovery(data)) throw new Error(t.modelImportDiscoveryInvalid);
      setResult(data);
    } catch (failure) {
      if (version === revision.current) setError(errorMessage(failure));
    } finally {
      if (version === revision.current) {
        busy.current = false;
        pending.current = null;
        setLoading(false);
        setSlow(false);
      }
    }
  }
  async function saveScope() {
    if (busy.current || !credential || !canSaveScope || !result) return;
    busy.current = true;
    setLoading(true);
    setSavingWorkspace(true);
    setError("");
    try {
      await api("/api/admin/credentials", {
        ...credential,
        workspace_id: result.workspace_id,
        secret: "",
      });
      setSavedWorkspace(result.workspace_id);
      setWorkspace(result.workspace_id);
      await onCredentialSaved();
    } catch (failure) {
      setError(errorMessage(failure));
    } finally {
      busy.current = false;
      setLoading(false);
      setSavingWorkspace(false);
      setSlow(false);
    }
  }
  async function reloadImported(data: ImportReceipt) {
    await onImported();
    addToast({ title: importSummary(data), color: "success" });
    onClose();
  }
  async function retryRefresh() {
    if (busy.current || !receipt) return;
    busy.current = true;
    setLoading(true);
    setError("");
    try {
      await reloadImported(receipt);
    } catch (failure) {
      setError(`${t.modelImportRefreshFailed} ${errorMessage(failure)}`);
    } finally {
      busy.current = false;
      setLoading(false);
    }
  }
  async function importModels() {
    if (busy.current) return;
    if (!selectedModels.length || !endpoint || !credential || result?.status !== "verified" || scopeChanged) {
      setError(t.modelImportChooseModels);
      return;
    }
    if (budgetBlocked) {
      setError(t.modelImportBudgetBlocked);
      return;
    }
    if (selectedModels.length > 400) {
      setError(t.modelImportBatchLimit);
      return;
    }
    if (selectedModels.some((model) => options.find((option) => option.id === model.id)?.unavailable)) {
      setError(t.modelImportUnavailable);
      return;
    }
    const payload = selectedModels.map((model) => {
      const cap = model.max_output_tokens;
      return {
        id: model.id, name: model.name.trim() || model.id, provider: endpoint.provider,
        upstream_model: model.id, enabled: true, billing_mode: "usage_only",
        input_rate: 0, output_rate: 0, cached_rate: 0, cache_write_rate: 0,
        cost_input_rate: 0, cost_output_rate: 0, cost_cached_rate: 0, cost_cache_write_rate: 0,
        max_output_tokens: typeof cap === "number" && Number.isFinite(cap) && cap > 0
          ? Math.min(65536, Math.max(1, Math.floor(cap))) : 4096,
      };
    });
    busy.current = true;
    setImporting(true);
    setLoading(true);
    setSlow(false);
    setError("");
    let imported: ImportReceipt | null = null;
    try {
      const data = await api<unknown>("/api/admin/models/import", { models: payload });
      if (!validReceipt(data, payload.map((model) => model.id))) throw new Error(t.modelImportResultInvalid);
      imported = data;
      setReceipt(data);
      await reloadImported(data);
    } catch (failure) {
      setError(`${imported ? t.modelImportRefreshFailed + " " : ""}${errorMessage(failure)}`);
    } finally {
      busy.current = false;
      setImporting(false);
      setLoading(false);
      setSlow(false);
    }
  }

  return (
    <Modal
      isOpen
      size="lg"
      scrollBehavior="inside"
      onClose={close}
      isDismissable={!loading}
      isKeyboardDismissDisabled={loading}
      hideCloseButton={loading}
      classNames={{ base: "max-h-[85dvh]" }}
    >
      <ModalContent>
        <form
          noValidate
          className="flex min-h-0 flex-col overflow-hidden"
          onSubmit={(event) => {
            event.preventDefault();
            if (receipt) void retryRefresh();
            else if (result?.status === "verified" && !scopeChanged) void importModels();
            else void discover();
          }}
        >
          <ModalHeader className="flex-col gap-2">
            <h2>{t.modelImportTitle}</h2>
            <p className="text-sm font-normal leading-6 text-default-500">
              {t.modelImportIntro}
            </p>
          </ModalHeader>
          <ModalBody className="gap-4 overflow-y-auto">
            {error && (
              <div ref={errorBlock}>
                <ErrorBox message={error} />
              </div>
            )}
            {!eligible.length ? (
              <div className="space-y-3 rounded-lg border border-default-200 p-4">
                <p className="text-sm leading-6 text-default-500">
                  {t.modelImportNoCredentials}
                </p>
                <Button as={Link} href="/app/upstreams" variant="flat">
                  {t.upstreams}
                </Button>
              </div>
            ) : (
              <>
                <Select
                  autoFocus
                  label={t.credential}
                  selectedKeys={credentialID ? [credentialID] : []}
                  onChange={(event) => changeCredential(event.target.value)}
                  description={endpoint?.base_url}
                  isDisabled={savingWorkspace || importing || !!receipt}
                >
                  {eligible.map((item) => (
                    <SelectItem
                      key={item.id}
                      textValue={item.name}
                      description={
                        endpoints.find((host) => host.id === item.endpoint_id)
                          ?.name
                      }
                    >
                      {item.name}
                    </SelectItem>
                  ))}
                </Select>
                {budgetBlocked && !receipt && (
                  <div className="space-y-2 rounded-lg border border-warning-200 bg-warning-50 p-4 text-sm leading-6">
                    <p>{t.modelImportBudgetBlocked}</p>
                    <Button as={Link} href="/app/upstreams" variant="bordered" isDisabled={loading}>
                      {t.modelImportConfigureBudget}
                    </Button>
                  </div>
                )}
                {receipt && (
                  <div role="status" className="rounded-lg border border-success-200 bg-success-50 p-4 text-sm leading-6">
                    {importSummary(receipt)}
                  </div>
                )}
                {scopeChanged && !receipt && (
                  <div className="space-y-3 rounded-lg border border-warning-200 bg-warning-50 p-4">
                    <p className="text-sm font-medium">
                      {t.modelImportScopeTitle}
                    </p>
                    <p className="text-sm leading-6">
                      {canSaveScope
                        ? t.modelImportScopeSaveBody
                        : result?.status === "workspace_required"
                          ? t.modelImportSelectWorkspace
                          : t.modelImportScopeBody}
                    </p>
                    {result?.workspace_id && (
                      <code className="block break-all text-xs">
                        {result.workspace_id}
                      </code>
                    )}
                    {result?.status === "workspace_required" && (
                      <>
                        {result.workspaces?.length ? (
                          <Select
                            label={t.anthropicWorkspaceID}
                            selectedKeys={workspace ? [workspace] : []}
                            onChange={(event) => {
                              if (event.target.value)
                                setWorkspace(event.target.value);
                              setError("");
                            }}
                          >
                            {result.workspaces.map((item) => (
                              <SelectItem key={item.id} description={item.id}>
                                {item.name || item.id}
                              </SelectItem>
                            ))}
                          </Select>
                        ) : (
                          <Input
                            label={t.anthropicWorkspaceID}
                            value={workspace}
                            onValueChange={setWorkspace}
                            placeholder="wrkspc_…"
                          />
                        )}
                        <Button
                          variant="bordered"
                          onPress={() => void discover()}
                        >
                          {t.credentialVerifyKey}
                        </Button>
                      </>
                    )}
                    {canSaveScope ? (
                      <Button
                        color="primary"
                        isLoading={savingWorkspace}
                        onPress={() => void saveScope()}
                      >
                        {t.modelImportSaveWorkspace}
                      </Button>
                    ) : result?.status !== "workspace_required" ? (
                      <Button
                        as={Link}
                        href="/app/upstreams"
                        variant="bordered"
                      >
                        {t.modelImportConfigureWorkspace}
                      </Button>
                    ) : null}
                  </div>
                )}
                {result?.status === "verified" && !scopeChanged && !receipt && (
                  <section className="space-y-3">
                    <div className="flex items-center justify-between gap-3">
                      <p className="text-sm font-medium">
                        {count(result.models.length)} {t.credentialModelsFound}
                      </p>
                      <Button
                        size="sm"
                        variant="light"
                        onPress={() => void discover()}
                        isDisabled={loading}
                      >
                        {t.credentialRecheck}
                      </Button>
                    </div>
                    <ModelImportSelection
                      models={options}
                      value={selected}
                      onValueChange={setSelected}
                      isDisabled={loading}
                    />
                    {selected.length > 400 && (
                      <p role="alert" className="text-sm text-danger">{t.modelImportBatchLimit}</p>
                    )}
                    {options.some((option) => option.unavailable === "alias_required") && (
                      <p className="text-xs leading-5 text-default-500">{t.modelImportAliasHelp}</p>
                    )}
                    <p className="text-xs leading-5 text-default-500">
                      {t.modelUsageOnlyHelp}
                    </p>
                  </section>
                )}
                {!result && (
                  <p className="text-xs leading-5 text-default-500">
                    {t.modelImportDiscoveryNote}
                  </p>
                )}
              </>
            )}
            {slow && (
              <p role="status" className="text-xs text-default-500">
                {t.stillWorking}
              </p>
            )}
          </ModalBody>
          <ModalFooter className="shrink-0">
            <Button variant="light" isDisabled={loading} onPress={close}>
              {receipt ? t.close : t.cancel}
            </Button>
            {!!eligible.length && !scopeChanged && (
              <Button color="primary" type="submit" isLoading={loading}
                isDisabled={!receipt && result?.status === "verified" && (!selected.length || selected.length > 400 || budgetBlocked)}>
                {receipt ? t.modelImportRefresh : result?.status === "verified"
                  ? t.modelImportBatchAction.replace("{count}", count(selected.length))
                  : t.modelImportDiscover}
              </Button>
            )}
          </ModalFooter>
        </form>
      </ModalContent>
    </Modal>
  );
}
