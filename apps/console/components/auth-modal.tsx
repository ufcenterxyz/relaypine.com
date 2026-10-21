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
import { api, errorMessage } from "@/lib/api";
import { en as t } from "@/i18n/en";
import { ErrorBox } from "./shared";
import { TreePine, ArrowRight } from "lucide-react";
export function AuthModal({
  mode,
  onMode,
  onClose,
}: {
  mode: "login" | "register";
  onMode: (mode: "login" | "register") => void;
  onClose: () => void;
}) {
  const router = useRouter();
  const [values, setValues] = useState({
    name: "",
    email: "",
    password: "",
    confirmation: "",
  });
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const register = mode === "register";
  async function submit(e: React.FormEvent) {
    e.preventDefault();
    const next: Record<string, string> = {};
    if (register && !values.name.trim()) next.name = t.required;
    if (!/^\S+@\S+\.\S+$/.test(values.email)) next.email = t.invalidEmail;
    if (values.password.length < (register ? 12 : 1))
      next.password = register ? t.passwordHelp : t.required;
    if (register && values.password !== values.confirmation)
      next.confirmation = t.passwordMismatch;
    setErrors(next);
    if (Object.keys(next).length) return;
    setBusy(true);
    setError("");
    try {
      await api(`/api/auth/${mode}`, {
        name: values.name,
        email: values.email,
        password: values.password,
      });
      router.push("/app");
    } catch (e) {
      setError(errorMessage(e));
      setBusy(false);
    }
  }
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
          noValidate
          onSubmit={submit}
          className="flex min-h-0 flex-col overflow-hidden"
        >
          <ModalHeader className="flex-col gap-2 px-6 pt-6 sm:gap-3 sm:px-8 sm:pt-8">
            <span className="mb-1 flex size-9 sm:mb-3 sm:size-10 items-center justify-center rounded-xl bg-primary text-primary-foreground">
              <TreePine size={24} strokeWidth={1.5} />
            </span>
            <h2 className="text-[25px] sm:text-[27px] font-medium leading-tight tracking-[-.04em]">
              {register ? t.authCreate : t.authWelcome}
            </h2>
            <p className="text-[13px] sm:text-sm font-normal leading-[1.6] text-default-500">
              {register ? t.authCreateDesc : t.authWelcomeDesc}
            </p>
          </ModalHeader>
          <ModalBody className="min-h-0 gap-4 overflow-y-auto px-6 pt-3 sm:gap-5 sm:px-8 sm:pt-4">
            {error && <ErrorBox message={error} />}{" "}
            {register && (
              <Input
                variant="bordered"
                labelPlacement="outside"
                placeholder=" "
                classNames={{
                  inputWrapper: "h-12 border border-default-200 shadow-none",
                  label: "font-medium text-xs",
                }}
                autoFocus
                label={t.fullName}
                autoComplete="name"
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
              classNames={{
                inputWrapper: "h-12 border border-default-200 shadow-none",
                label: "font-medium text-xs",
              }}
              autoFocus={!register}
              label={t.email}
              type="email"
              autoComplete="email"
              value={values.email}
              onValueChange={(email) => setValues({ ...values, email })}
              isInvalid={!!errors.email}
              errorMessage={errors.email}
            />
            <Input
              variant="bordered"
              labelPlacement="outside"
              placeholder=" "
              classNames={{
                inputWrapper: "h-12 border border-default-200 shadow-none",
                label: "font-medium text-xs",
              }}
              label={t.password}
              type="password"
              autoComplete={register ? "new-password" : "current-password"}
              value={values.password}
              onValueChange={(password) => setValues({ ...values, password })}
              description={register ? t.passwordHelp : undefined}
              isInvalid={!!errors.password}
              errorMessage={errors.password}
            />
            {register && (
              <Input
                variant="bordered"
                labelPlacement="outside"
                placeholder=" "
                classNames={{
                  inputWrapper: "h-12 border border-default-200 shadow-none",
                  label: "font-medium text-xs",
                }}
                label={t.confirmPassword}
                type="password"
                autoComplete="new-password"
                value={values.confirmation}
                onValueChange={(confirmation) =>
                  setValues({ ...values, confirmation })
                }
                isInvalid={!!errors.confirmation}
                errorMessage={errors.confirmation}
              />
            )}
          </ModalBody>
          <ModalFooter className="flex-col gap-3 px-6 pb-6 pt-5 sm:gap-4 sm:px-8 sm:pb-7 sm:pt-6">
            <Button
              className="h-12 w-full"
              endContent={<ArrowRight size={16} />}
              color="primary"
              type="submit"
              isLoading={busy}
            >
              {register ? t.signUp : t.signIn}
            </Button>
            <div className="flex items-center justify-center text-sm text-default-500">
              {register ? t.haveAccount : t.needAccount}
              <Button
                variant="light"
                size="sm"
                onPress={() => {
                  onMode(register ? "login" : "register");
                  setError("");
                  setErrors({});
                }}
              >
                {register ? t.signIn : t.signUp}
              </Button>
            </div>
          </ModalFooter>
        </form>
      </ModalContent>
    </Modal>
  );
}
