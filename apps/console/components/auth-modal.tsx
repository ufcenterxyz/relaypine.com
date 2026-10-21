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
      classNames={{ base: "max-h-[85dvh]" }}
    >
      <ModalContent>
        <form noValidate onSubmit={submit}>
          <ModalHeader className="flex-col gap-2 pt-7">
            <h2 className="text-xl">
              {register ? t.authCreate : t.authWelcome}
            </h2>
            <p className="text-sm font-normal leading-6 text-default-500">
              {register ? t.authCreateDesc : t.authWelcomeDesc}
            </p>
          </ModalHeader>
          <ModalBody className="gap-4">
            {error && <ErrorBox message={error} />}{" "}
            {register && (
              <Input
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
          <ModalFooter className="flex-col pb-6">
            <Button
              className="w-full"
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
