"use client";

import { useEffect, useRef, useState } from "react";
import Link from "next/link";
import {
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
} from "@heroui/react";
import { Search } from "lucide-react";
import { en as t } from "@/i18n/en";
import { api, count, errorMessage } from "@/lib/api";
import type { Credential, Endpoint, Model } from "@/lib/types";
import { ErrorBox } from "./shared";

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

export interface ImportedModelDraft {
  id: string;
  name: string;
  provider: string;
  upstream_model: string;
  max_output_tokens: number;
}

export function ModelImport({
  credentials,
  endpoints,
  models,
  onSelect,
  onClose,
  onCredentialSaved,
}: {
  credentials: Credential[];
  endpoints: Endpoint[];
  models: Model[];
  onSelect: (model: ImportedModelDraft) => void;
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
  const [selected, setSelected] = useState("");
  const [search, setSearch] = useState("");
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
  const visible = (result?.models || []).filter((model) =>
    `${model.id} ${model.name}`.toLowerCase().includes(search.toLowerCase()),
  );
  const selectedModel = result?.models.find((model) => model.id === selected);

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
      (busy.current && savingWorkspace)
    )
      return;
    revision.current += 1;
    pending.current?.abort();
    pending.current = null;
    busy.current = false;
    setCredentialID(value);
    setResult(null);
    setSelected("");
    setSearch("");
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
    setSelected("");
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
  function choose() {
    if (busy.current) return;
    if (
      !selectedModel ||
      !endpoint ||
      !credential ||
      result?.status !== "verified" ||
      scopeChanged
    ) {
      setError(t.modelImportChooseModel);
      return;
    }
    if (!allowed(selectedModel)) {
      setError(
        existing.has(selectedModel.id)
          ? t.modelIDExists
          : t.modelImportHostRestriction,
      );
      return;
    }
    const cap = selectedModel.max_output_tokens;
    onSelect({
      id: selectedModel.id,
      name: selectedModel.name || selectedModel.id,
      provider: endpoint.provider,
      upstream_model: selectedModel.id,
      max_output_tokens:
        typeof cap === "number" && Number.isFinite(cap) && cap > 0
          ? Math.min(65536, Math.max(1, Math.floor(cap)))
          : 4096,
    });
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
            if (result?.status === "verified" && !scopeChanged) choose();
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
                  isDisabled={savingWorkspace}
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
                {scopeChanged && (
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
                {result?.status === "verified" && !scopeChanged && (
                  <section className="space-y-3">
                    <div className="flex items-center justify-between gap-3">
                      <p className="text-sm font-medium">
                        {count(result.models.length)} {t.credentialModelsFound}
                      </p>
                      <Button
                        size="sm"
                        variant="light"
                        onPress={() => void discover()}
                      >
                        {t.credentialRecheck}
                      </Button>
                    </div>
                    <Input
                      aria-label={t.modelImportSearch}
                      placeholder={t.modelImportSearch}
                      value={search}
                      onValueChange={setSearch}
                      startContent={<Search size={16} />}
                    />
                    {visible.length ? (
                      <RadioGroup
                        aria-label={t.modelImportChooseModel}
                        value={selected}
                        onValueChange={setSelected}
                        classNames={{
                          wrapper:
                            "max-h-64 flex-nowrap gap-1 overflow-x-hidden overflow-y-auto rounded-lg border border-default-200 p-3",
                        }}
                      >
                        {visible.map((model) => (
                          <Radio
                            key={model.id}
                            value={model.id}
                            isDisabled={!allowed(model)}
                            classNames={{
                              base: "m-0 w-full max-w-full shrink-0 p-3",
                              labelWrapper: "min-w-0",
                              label: "break-words text-sm",
                              description: "break-all text-xs leading-5",
                            }}
                            description={model.id}
                          >
                            <span className="mr-2">
                              {model.name || model.id}
                            </span>
                            {existing.has(model.id) ? (
                              <Chip size="sm" variant="flat">
                                {t.modelImportExists}
                              </Chip>
                            ) : !allowed(model) ? (
                              <Chip size="sm" variant="flat">
                                {t.modelImportRestricted}
                              </Chip>
                            ) : null}
                          </Radio>
                        ))}
                      </RadioGroup>
                    ) : (
                      <p className="py-5 text-sm text-default-500">
                        {search ? t.noResults : t.modelImportEmpty}
                      </p>
                    )}
                    <p className="text-xs leading-5 text-default-500">
                      {t.modelImportPricingNote}
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
              {t.cancel}
            </Button>
            {!!eligible.length && !scopeChanged && (
              <Button color="primary" type="submit" isLoading={loading}>
                {result?.status === "verified"
                  ? t.modelImportSetRates
                  : t.modelImportDiscover}
              </Button>
            )}
          </ModalFooter>
        </form>
      </ModalContent>
    </Modal>
  );
}
