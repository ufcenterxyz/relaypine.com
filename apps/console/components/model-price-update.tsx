"use client";

import { useEffect, useRef, useState } from "react";
import { Button, Modal, ModalBody, ModalContent, ModalFooter, ModalHeader, Skeleton, addToast } from "@heroui/react";
import { pricingEn as p } from "@/i18n/pricing-en";
import { en as t } from "@/i18n/en";
import { api, count, errorMessage } from "@/lib/api";
import { validPricePreview, type PricePreview } from "@/lib/pricing";
import { ModelPricePreview } from "./model-price-preview";
import { ErrorBox } from "./shared";

export function ModelPriceUpdate({ onClose, onUpdated }: {
  onClose: () => void;
  onUpdated: () => Promise<void>;
}) {
  const [preview, setPreview] = useState<PricePreview | null>(null);
  const [selected, setSelected] = useState<string[]>([]);
  const [loading, setLoading] = useState(true);
  const [applying, setApplying] = useState(false);
  const [receipt, setReceipt] = useState<number | null>(null);
  const [error, setError] = useState("");
  const pending = useRef<AbortController | null>(null);
  const busy = useRef(false);
  const errorBlock = useRef<HTMLDivElement | null>(null);
  useEffect(() => {
    if (error) errorBlock.current?.scrollIntoView({ block: "nearest", behavior: "instant" });
  }, [error]);

  useEffect(() => {
    const controller = new AbortController();
    pending.current = controller;
    void api<unknown>("/api/admin/models/prices/preview", {}, "POST", controller.signal)
      .then((value) => {
        if (controller.signal.aborted) return;
        if (!validPricePreview(value) || value.kind !== "update") throw new Error(p.invalid);
        setPreview(value);
        setSelected(value.items.filter((item) => item.status === "matched").map((item) => item.id));
      })
      .catch((failure) => { if (!controller.signal.aborted) setError(errorMessage(failure)); })
      .finally(() => { if (!controller.signal.aborted) { setLoading(false); pending.current = null; } });
    return () => { controller.abort(); pending.current?.abort(); };
  }, []);

  async function recheck() {
    if (busy.current) return;
    busy.current = true;
    setLoading(true);
    setError("");
    const controller = new AbortController();
    pending.current = controller;
    try {
      const value = await api<unknown>("/api/admin/models/prices/preview", {}, "POST", controller.signal);
      if (!validPricePreview(value) || value.kind !== "update") throw new Error(p.invalid);
      setPreview(value);
      setSelected(value.items.filter((item) => item.status === "matched").map((item) => item.id));
    } catch (failure) {
      if (!controller.signal.aborted) setError(errorMessage(failure));
    } finally {
      busy.current = false;
      if (!controller.signal.aborted) setLoading(false);
      pending.current = null;
    }
  }
  async function apply() {
    if (busy.current) return;
    if (receipt === null && (!preview || !selected.length)) { setError(p.noSelection); return; }
    if (receipt === null && preview && Date.parse(preview.expires_at) <= Date.now()) { setError(p.expired); return; }
    busy.current = true;
    setApplying(true);
    setError("");
    let updated = receipt;
    try {
      if (updated === null) {
        const result = await api<{ updated_count: number }>("/api/admin/models/prices/apply", {
          preview_id: preview!.preview_id, model_ids: selected,
        });
        if (!Number.isInteger(result.updated_count) || result.updated_count < 0) throw new Error(p.invalid);
        updated = result.updated_count;
        setReceipt(updated);
      }
      await onUpdated();
      addToast({ title: p.success.replace("{count}", count(updated)), color: "success" });
      onClose();
    } catch (failure) {
      setError(`${updated !== null ? p.refreshFailed + " " : ""}${errorMessage(failure)}`);
    } finally { busy.current = false; setApplying(false); }
  }
  function close() {
    if (applying) return;
    pending.current?.abort();
    onClose();
  }
  return (
    <Modal isOpen onClose={close} size="2xl" scrollBehavior="inside" isDismissable={!applying}
      isKeyboardDismissDisabled={applying} hideCloseButton={applying} classNames={{ base: "max-h-[85dvh]" }}>
      <ModalContent>
        <ModalHeader className="flex-col gap-2">
          <h2>{p.updateTitle}</h2>
          <p className="text-sm font-normal leading-6 text-default-500">{p.updateIntro}</p>
        </ModalHeader>
        <ModalBody className="gap-4 overflow-y-auto">
          {error && <div ref={errorBlock}><ErrorBox message={error} /></div>}
          {receipt !== null && <p role="status" className="text-sm">{p.success.replace("{count}", count(receipt))}</p>}
          {loading ? <div aria-label={t.loading} className="space-y-3">{[0, 1, 2].map((n) => <Skeleton key={n} className="h-28 rounded-xl" />)}</div> : preview?.items.length ? (
            <>
              {preview.items.some((item) => item.status === "manual") && <p className="rounded-lg border border-default-200 p-3 text-sm leading-6">{p.manualNote}</p>}
              <ModelPricePreview preview={preview} selected={selected} onSelection={setSelected} disabled={applying || receipt !== null} />
            </>
          ) : !error && <p className="py-6 text-sm text-default-500">{p.none}</p>}
        </ModalBody>
        <ModalFooter className="flex-wrap">
          <Button variant="light" isDisabled={applying} onPress={close}>{t.close}</Button>
          {receipt === null && <Button variant="bordered" isLoading={loading} isDisabled={applying} onPress={() => void recheck()}>{p.retry}</Button>}
          <Button color="primary" isLoading={applying} isDisabled={loading || (receipt === null && !selected.length)} onPress={() => void apply()}>
            {receipt !== null ? p.refresh : p.apply.replace("{count}", count(selected.length))}
          </Button>
        </ModalFooter>
      </ModalContent>
    </Modal>
  );
}
