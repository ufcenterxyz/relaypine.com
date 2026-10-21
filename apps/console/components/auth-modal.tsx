"use client";
import { useState } from "react";
import {
  Button,
  Input,
  Modal,
  ModalContent,
  ModalHeader,
  ModalBody,
  ModalFooter,
} from "@heroui/react";
import { useRouter } from "next/navigation";
import { api, ApiError, errorMessage } from "@/lib/api";
import { en as t } from "@/i18n/en";
import { waitlistEn as w } from "@/i18n/waitlist-en";
import { ErrorBox } from "./shared";
import { TreePine, ArrowRight, Check } from "lucide-react";

export type AuthMode = "login" | "waitlist";
export function AuthModal({
  mode,
  onMode,
  onClose,
}: {
  mode: AuthMode;
  onMode: (mode: AuthMode) => void;
  onClose: () => void;
}) {
  const router = useRouter();
  const [values, setValues] = useState({ name: "", email: "", password: "" });
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [joined, setJoined] = useState(false);
  const [accessRequired, setAccessRequired] = useState(false);
  const waitlist = mode === "waitlist";
  function switchMode(next: AuthMode) {
    onMode(next);
    setError("");
    setErrors({});
    setAccessRequired(false);
    setJoined(false);
    setValues((current) => ({ ...current, password: "" }));
  }
  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (busy || joined) return;
    const next: Record<string, string> = {};
    const email = values.email.trim();
    const name = values.name.trim();
    if (waitlist && !name) next.name = t.required;
    else if (waitlist && new TextEncoder().encode(name).length > 100)
      next.name = w.nameTooLong;
    if (!/^\S+@\S+\.\S+$/.test(email)) next.email = t.invalidEmail;
    if (!waitlist && !values.password) next.password = t.required;
    setErrors(next);
    if (Object.keys(next).length) return;
    setBusy(true);
    setError("");
    setAccessRequired(false);
    try {
      if (waitlist) {
        await api("/api/waitlist", { name, email });
        setJoined(true);
      } else {
        await api("/api/auth/login", { email, password: values.password });
        router.push("/app");
      }
    } catch (e) {
      if (e instanceof ApiError && e.code === "waitlist_required") {
        setAccessRequired(true);
      } else {
        setError(errorMessage(e));
      }
    } finally {
      setBusy(false);
    }
  }
  const fieldClasses = {
    inputWrapper: "h-12 border border-default-200 shadow-none",
    label: "font-medium text-xs",
  };
  return (
    <Modal
      isOpen
      onClose={onClose}
      size="md"
      scrollBehavior="inside"
      classNames={{
        base: "rp-modal max-h-[85dvh]",
        backdrop: "bg-[#0c2117]/25 backdrop-blur-sm",
      }}
    >
      <ModalContent>
        <form
          key={mode}
          noValidate
          onSubmit={submit}
          className="flex min-h-0 flex-col overflow-hidden"
        >
          <ModalHeader className="flex-col gap-2 px-6 pt-6 sm:gap-3 sm:px-8 sm:pt-8">
            <span className="mb-1 flex size-9 items-center justify-center rounded-xl bg-primary text-primary-foreground sm:mb-3 sm:size-10">
              {joined ? (
                <Check size={23} strokeWidth={1.8} />
              ) : (
                <TreePine size={24} strokeWidth={1.5} />
              )}
            </span>
            <h2 className="text-[25px] font-medium leading-tight tracking-[-.04em] sm:text-[27px]">
              {joined ? w.successTitle : waitlist ? w.title : w.adminTitle}
            </h2>
            <p className="text-[13px] font-normal leading-[1.6] text-default-500 sm:text-sm">
              {joined
                ? w.successBody
                : waitlist
                  ? w.description
                  : w.adminDescription}
            </p>
          </ModalHeader>
          <ModalBody className="min-h-0 gap-4 overflow-y-auto px-6 pt-3 sm:gap-5 sm:px-8 sm:pt-4">
            {joined ? (
              <div
                role="status"
                className="rounded-xl border border-default-200 px-4 py-3"
              >
                <p className="mb-1 text-xs text-default-500">
                  {w.successEmail}
                </p>
                <p className="break-all text-sm">{values.email.trim()}</p>
              </div>
            ) : (
              <>
                {error && <ErrorBox message={error} />}
                {accessRequired && (
                  <div
                    role="status"
                    className="rounded-xl border border-default-200 p-4 text-sm leading-6"
                  >
                    <p className="text-default-600">{w.accessRequired}</p>
                    <Button
                      className="mt-3"
                      color="primary"
                      variant="flat"
                      onPress={() => switchMode("waitlist")}
                    >
                      {w.join}
                    </Button>
                  </div>
                )}
                {waitlist && (
                  <Input
                    variant="bordered"
                    labelPlacement="outside"
                    placeholder=" "
                    classNames={fieldClasses}
                    autoFocus
                    label={t.fullName}
                    autoComplete="name"
                    maxLength={100}
                    value={values.name}
                    onValueChange={(name) => setValues({ ...values, name })}
                    isInvalid={!!errors.name}
                    errorMessage={errors.name}
                  />
                )}
                <Input
                  variant="bordered"
                  labelPlacement="outside"
                  placeholder=" "
                  classNames={fieldClasses}
                  autoFocus={!waitlist}
                  label={t.email}
                  type="email"
                  autoComplete="email"
                  maxLength={254}
                  value={values.email}
                  onValueChange={(email) => setValues({ ...values, email })}
                  isInvalid={!!errors.email}
                  errorMessage={errors.email}
                />
                {!waitlist && (
                  <Input
                    variant="bordered"
                    labelPlacement="outside"
                    placeholder=" "
                    classNames={fieldClasses}
                    label={t.password}
                    type="password"
                    autoComplete="current-password"
                    value={values.password}
                    onValueChange={(password) =>
                      setValues({ ...values, password })
                    }
                    isInvalid={!!errors.password}
                    errorMessage={errors.password}
                  />
                )}
                {waitlist && (
                  <p className="text-xs leading-5 text-default-500">
                    {w.privacy}
                  </p>
                )}
              </>
            )}
          </ModalBody>
          <ModalFooter className="flex-col gap-3 px-6 pb-6 pt-5 sm:gap-4 sm:px-8 sm:pb-7 sm:pt-6">
            {joined ? (
              <Button className="h-12 w-full" color="primary" onPress={onClose}>
                {t.close}
              </Button>
            ) : (
              <>
                <Button
                  className="h-12 w-full"
                  endContent={<ArrowRight size={16} />}
                  color="primary"
                  type="submit"
                  isLoading={busy}
                >
                  {waitlist ? w.join : t.signIn}
                </Button>
                {waitlist && (
                  <p className="text-center text-[11px] leading-5 text-default-400">
                    {w.note}
                  </p>
                )}
                <div className="flex flex-wrap items-center justify-center text-xs text-default-500">
                  {waitlist ? w.adminPrompt : w.invitePrompt}
                  <Button
                    variant="light"
                    size="sm"
                    isDisabled={busy}
                    onPress={() => switchMode(waitlist ? "login" : "waitlist")}
                  >
                    {waitlist ? w.adminSignIn : w.join}
                  </Button>
                </div>
              </>
            )}
          </ModalFooter>
        </form>
      </ModalContent>
    </Modal>
  );
}
