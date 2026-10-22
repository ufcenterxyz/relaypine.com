"use client";
import { Fragment, useEffect, useRef, useState } from "react";
import {
  Button,
  Input,
  Modal,
  ModalContent,
  ModalHeader,
  ModalBody,
  ModalFooter,
  Select,
  SelectItem,
  Switch,
} from "@heroui/react";
import { en as t } from "@/i18n/en";
import { errorMessage } from "@/lib/api";
import { ErrorBox } from "./shared";
import { SlidersHorizontal } from "lucide-react";
export interface Field {
  key: string;
  label: string;
  type?: "text" | "number" | "password" | "select" | "switch";
  description?: string;
  placeholder?: string;
  required?: boolean;
  readOnly?: boolean;
  min?: number;
  options?: { key: string; label: string }[];
  section?: string;
  visibleWhen?: (values: FormValues) => boolean;
  validate?: (value: string, values: FormValues) => string | undefined;
}
export type FormValues = Record<string, string | number | boolean>;
export function ResourceForm({
  title,
  description,
  fields,
  initial,
  onSubmit,
  onClose,
  submitLabel = t.save,
  onChange,
}: {
  title: string;
  description?: string;
  fields: Field[];
  initial: FormValues;
  onSubmit: (values: FormValues) => Promise<void>;
  onClose: () => void;
  submitLabel?: string;
  onChange?: (key: string, value: string, values: FormValues) => FormValues;
}) {
  const [values, setValues] = useState(initial);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const errorBlock = useRef<HTMLDivElement | null>(null);
  const formElement = useRef<HTMLFormElement | null>(null);
  useEffect(() => {
    if (!Object.keys(errors).length) return;
    const invalid = formElement.current?.querySelector<HTMLElement>(
      '[aria-invalid="true"]',
    );
    invalid?.focus({ preventScroll: true });
    invalid?.scrollIntoView({ block: "center", behavior: "instant" });
  }, [errors]);
  useEffect(() => {
    if (error)
      errorBlock.current?.scrollIntoView({
        block: "nearest",
        behavior: "instant",
      });
  }, [error]);
  const visibleFields = fields.filter(
    (field) => !field.visibleWhen || field.visibleWhen(values),
  );
  function update(key: string, value: string | boolean) {
    setValues((v) =>
      typeof value === "string" && onChange
        ? onChange(key, value, { ...v, [key]: value })
        : { ...v, [key]: value },
    );
  }
  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (busy) return;
    const next: Record<string, string> = {};
    for (const field of visibleFields) {
      const value = values[field.key];
      if (field.required && !String(value ?? "").trim())
        next[field.key] = t.required;
      if (
        field.type === "number" &&
        (!Number.isFinite(Number(value)) || Number(value) < (field.min ?? 0))
      )
        next[field.key] = t.invalidNumber;
      const message = field.validate?.(String(value ?? ""), values);
      if (message) next[field.key] = message;
    }
    setErrors(next);
    if (Object.keys(next).length) return;
    setBusy(true);
    setError("");
    try {
      await onSubmit(values);
      onClose();
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <Modal
      isOpen
      onClose={() => { if (!busy) onClose(); }}
      isDismissable={!busy}
      isKeyboardDismissDisabled={busy}
      hideCloseButton={busy}
      size={fields.length > 5 ? "lg" : "md"}
      scrollBehavior="inside"
      classNames={{ base: "rp-management-modal max-h-[88dvh]" }}
    >
      <ModalContent>
        <form
          ref={formElement}
          className="flex min-h-0 flex-col overflow-hidden"
          noValidate
          onSubmit={submit}
        >
          <ModalHeader className="rp-management-modal-header flex-col gap-2">
            <span className="rp-management-modal-icon"><SlidersHorizontal size={18} /></span>
            <h2>{title}</h2>
            {description && (
              <p className="text-sm font-normal leading-6 text-default-500">
                {description}
              </p>
            )}
          </ModalHeader>
          <ModalBody className="rp-management-modal-body gap-4 overflow-y-auto">
            {error && (
              <div ref={errorBlock}>
                <ErrorBox message={error} />
              </div>
            )}{" "}
            <div className="rp-management-fields">
            {visibleFields.map((field, index) => (
              <Fragment key={field.key}>
                {field.section && (
                  <h3 className="rp-management-field-section">
                    {field.section}
                  </h3>
                )}
              <div className={field.key.endsWith("_rate") ? "rp-management-rate-field" : "rp-management-full-field"}>
                {field.type === "switch" ? (
                  <div>
                  <Switch
                    isDisabled={busy}
                    isSelected={Boolean(values[field.key])}
                    onValueChange={(v) => update(field.key, v)}
                  >
                    {field.label}
                  </Switch>
                  {field.description && <p className="mt-2 text-xs leading-5 text-default-500">{field.description}</p>}
                  </div>
                ) : field.type === "select" ? (
                  <Select
                    isDisabled={busy}
                    variant="bordered"
                    autoFocus={index === 0}
                    label={field.label}
                    selectedKeys={
                      values[field.key] !== undefined
                        ? [String(values[field.key])]
                        : []
                    }
                    onChange={(e) => update(field.key, e.target.value)}
                    description={field.description}
                    isInvalid={!!errors[field.key]}
                    errorMessage={errors[field.key]}
                  >
                    {(field.options ?? []).map((option) => (
                      <SelectItem key={option.key}>{option.label}</SelectItem>
                    ))}
                  </Select>
                ) : (
                  <Input
                    isDisabled={busy}
                    variant="bordered"
                    isReadOnly={field.readOnly}
                    autoFocus={index === 0}
                    label={field.label}
                    type={field.type ?? "text"}
                    autoComplete={
                      field.type === "password" ? "new-password" : "off"
                    }
                    value={String(values[field.key] ?? "")}
                    onValueChange={(v) => update(field.key, v)}
                    description={field.description}
                    placeholder={field.placeholder}
                    isInvalid={!!errors[field.key]}
                    errorMessage={errors[field.key]}
                    min={field.min ?? 0}
                    step={field.type === "number" ? "any" : undefined}
                  />
                )}
              </div>
              </Fragment>
            ))}
            </div>
          </ModalBody>
          <ModalFooter className="rp-management-modal-footer">
            <Button variant="light" isDisabled={busy} onPress={onClose}>
              {t.cancel}
            </Button>
            <Button type="submit" color="primary" isLoading={busy}>
              {submitLabel}
            </Button>
          </ModalFooter>
        </form>
      </ModalContent>
    </Modal>
  );
}
