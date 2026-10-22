"use client";
import { useEffect, useId, useRef, useState } from "react";
import {
  Button,
  Input,
  InputOtp,
  Modal,
  ModalContent,
  ModalHeader,
  ModalBody,
  ModalFooter,
} from "@heroui/react";
import { useRouter } from "next/navigation";
import { api, ApiError } from "@/lib/api";
import { en as t } from "@/i18n/en";
import { authEn as w } from "@/i18n/auth-en";
import { BrandMark, ErrorBox } from "./shared";
import { ArrowRight, Mail } from "lucide-react";

type Step = "email" | "code";
type CodeSent = {
  status: "code_sent";
  expires_in: number;
  resend_after: number;
};
type Verified = { status: "authenticated" };

function requestError(error: unknown) {
  if (!(error instanceof ApiError)) return w.requestFailed;
  switch (error.code) {
    case "rate_limited":
      return error.retryAfter
        ? w.rateLimitedFor.replace("{seconds}", String(error.retryAfter))
        : w.rateLimited;
    case "invalid_code":
      return w.invalidCode;
    case "expired_code":
      return w.expiredCode;
    case "email_unavailable":
      return w.emailUnavailable;
    case "forbidden":
      return w.forbidden;
    case "auth_unavailable":
      return w.authUnavailable;
    default:
      return w.requestFailed;
  }
}

export function AuthModal({ onClose }: { onClose: () => void }) {
  const router = useRouter();
  const [values, setValues] = useState({ name: "", email: "" });
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [error, setError] = useState("");
  const [busy, setBusy] = useState<"send" | "resend" | "verify" | null>(null);
  const [step, setStep] = useState<Step>("email");
  const [code, setCode] = useState("");
  const [sentEmail, setSentEmail] = useState("");
  const [resent, setResent] = useState(false);
  const [resendAt, setResendAt] = useState(0);
  const [expiresAt, setExpiresAt] = useState(0);
  const [now, setNow] = useState(0);
  const request = useRef<AbortController | null>(null);
  const codeInput = useRef<HTMLInputElement>(null);
  const codeHintId = useId();
  const codeErrorId = useId();
  const checking = step === "code";
  const resendSeconds = Math.max(0, Math.ceil((resendAt - now) / 1000));
  const expirySeconds = Math.max(0, Math.ceil((expiresAt - now) / 1000));
  const expiryTime = `${Math.floor(expirySeconds / 60)}:${String(expirySeconds % 60).padStart(2, "0")}`;

  useEffect(() => () => request.current?.abort(), []);
  useEffect(() => {
    if (!busy) return;
    // Disabled loading buttons lose focus; keep Escape available during a request.
    const dismiss = (event: KeyboardEvent) => {
      if (event.key !== "Escape") return;
      event.preventDefault();
      event.stopPropagation();
      request.current?.abort();
      request.current = null;
      setBusy(null);
      onClose();
    };
    document.addEventListener("keydown", dismiss, true);
    return () => document.removeEventListener("keydown", dismiss, true);
  }, [busy, onClose]);
  useEffect(() => {
    if (step !== "code") return;
    const timer = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(timer);
  }, [step]);

  function cancelRequest() {
    request.current?.abort();
    request.current = null;
    setBusy(null);
  }
  function close() {
    cancelRequest();
    onClose();
  }
  function changeEmail() {
    cancelRequest();
    setStep("email");
    setCode("");
    setErrors({});
    setError("");
    setResent(false);
  }
  function isCurrent(controller: AbortController) {
    return request.current === controller && !controller.signal.aborted;
  }
  function finish(controller: AbortController) {
    if (!isCurrent(controller)) return;
    request.current = null;
    setBusy(null);
  }
  async function sendCode(resend = false) {
    if (request.current) return;
    if (resend && resendSeconds > 0) return;
    const next: Record<string, string> = {};
    const email = (resend ? sentEmail : values.email).trim().toLowerCase();
    if (!/^\S+@\S+\.\S+$/.test(email)) next.email = t.invalidEmail;
    setErrors(next);
    if (Object.keys(next).length) return;
    const controller = new AbortController();
    request.current = controller;
    setBusy(resend ? "resend" : "send");
    setError("");
    setResent(false);
    try {
      const result = await api<CodeSent>(
        "/api/auth/code",
        { email },
        "POST",
        controller.signal,
      );
      if (!isCurrent(controller)) return;
      if (result.status !== "code_sent") throw new Error("Unexpected response");
      const time = Date.now();
      setNow(time);
      setResendAt(time + (result.resend_after || 60) * 1000);
      setExpiresAt(time + (result.expires_in || 600) * 1000);
      setSentEmail(email);
      setCode("");
      setStep("code");
      setResent(resend);
      if (resend) codeInput.current?.focus();
    } catch (error) {
      if (!isCurrent(controller)) return;
      if (error instanceof ApiError && error.code === "invalid_email") {
        setErrors({ email: t.invalidEmail });
      } else {
        setError(requestError(error));
      }
      if (
        error instanceof ApiError &&
        error.code === "rate_limited" &&
        error.retryAfter
      ) {
        const time = Date.now();
        setNow(time);
        setResendAt(time + error.retryAfter * 1000);
      }
    } finally {
      finish(controller);
    }
  }
  async function verifyCode() {
    if (request.current || !checking) return;
    if (!/^\d{6}$/.test(code)) {
      setErrors({ code: w.incompleteCode });
      codeInput.current?.focus();
      return;
    }
    const controller = new AbortController();
    request.current = controller;
    setBusy("verify");
    setError("");
    setErrors({});
    setResent(false);
    let navigating = false;
    try {
      const result = await api<Verified>(
        "/api/auth/verify",
        { email: sentEmail, code },
        "POST",
        controller.signal,
      );
      if (!isCurrent(controller)) return;
      if (result.status === "authenticated") {
        router.push("/app");
        navigating = true;
        return;
      }
      throw new Error("Unexpected response");
    } catch (error) {
      if (!isCurrent(controller)) return;
      if (
        error instanceof ApiError &&
        ["invalid_code", "expired_code"].includes(error.code)
      ) {
        setErrors({ code: requestError(error) });
        if (error.code === "expired_code") setExpiresAt(0);
        codeInput.current?.focus();
      } else {
        setError(requestError(error));
      }
    } finally {
      if (!navigating) finish(controller);
    }
  }
  function submit(event: React.FormEvent) {
    event.preventDefault();
    if (checking) void verifyCode();
    else void sendCode();
  }
  const fieldClasses = {
    inputWrapper: "h-12 border border-default-200 shadow-none",
    label: "font-medium text-xs",
  };
  return (
    <Modal
      isOpen
      onClose={close}
      size="md"
      scrollBehavior="inside"
      classNames={{
        base: "rp-modal rp-auth-modal max-h-[85dvh]",
        backdrop: "bg-[#080d16]/55",
      }}
    >
      <ModalContent>
        <form
          noValidate
          onSubmit={submit}
          className="flex min-h-0 flex-col overflow-hidden"
          aria-busy={!!busy}
        >
          <ModalHeader className="rp-auth-header">
            <div className="rp-auth-identity">
              {checking ? <span className="rp-brand-mark rp-auth-mark"><Mail size={20} /></span> : <BrandMark className="rp-auth-mark" />}
              <span>RelayPine</span>
            </div>
            <h2>{checking ? w.verifyTitle : w.title}</h2>
            <p>{checking ? w.verifyDescription : w.description}</p>
          </ModalHeader>
          <ModalBody className="rp-auth-body min-h-0 overflow-y-auto">
            <>
                {error && <ErrorBox message={error} />}
                {checking ? (
                  <>
                    <div className="rp-auth-recipient">
                      <div className="min-w-0">
                        <p className="mb-0.5 text-[11px] text-default-500">
                          {w.codeSentTo}
                        </p>
                        <p className="break-all text-sm">{sentEmail}</p>
                      </div>
                      <Button
                        className="min-h-11 shrink-0 px-2"
                        size="sm"
                        variant="light"
                        onPress={changeEmail}
                      >
                        {w.changeEmail}
                      </Button>
                    </div>
                    <div>
                      <InputOtp
                        ref={(input: HTMLInputElement | null) => {
                          codeInput.current = input;
                          // HeroUI 2.8 labels the wrapper, so label its native OTP input too.
                          input?.setAttribute("aria-label", w.codeLabel);
                          input?.setAttribute(
                            "aria-invalid",
                            String(!!errors.code),
                          );
                          input?.setAttribute(
                            "aria-describedby",
                            errors.code ? codeErrorId : codeHintId,
                          );
                        }}
                        autoFocus
                        aria-label={w.codeLabel}
                        length={6}
                        variant="bordered"
                        color="primary"
                        inputMode="numeric"
                        autoComplete="one-time-code"
                        allowedKeys="^[0-9]*$"
                        pasteTransformer={(value) =>
                          value.replace(/[\s-]/g, "")
                        }
                        value={code}
                        onValueChange={(value) => {
                          setCode(value);
                          setErrors({});
                        }}
                        isReadOnly={busy === "verify"}
                        isInvalid={!!errors.code}
                        classNames={{
                          base: "w-full items-center",
                          segmentWrapper: "w-full justify-between gap-1.5",
                          segment:
                            "h-12 w-11 border border-default-200 bg-background text-xl font-medium shadow-none data-[active=true]:scale-100 sm:h-14 sm:w-12",
                          helperWrapper: "w-full px-0",
                          errorMessage: "text-sm leading-5",
                        }}
                      />
                      {errors.code && (
                        <p
                          id={codeErrorId}
                          role="alert"
                          className="mt-2 text-sm leading-5 text-danger"
                        >
                          {errors.code}
                        </p>
                      )}
                      {!errors.code && (
                        <p
                          id={codeHintId}
                          className="mt-2 text-center text-xs tabular-nums text-default-500"
                        >
                          {expirySeconds > 0
                            ? w.expiresIn.replace("{time}", expiryTime)
                            : w.expiredCode}
                        </p>
                      )}
                    </div>
                    {resent && (
                      <p
                        role="status"
                        className="text-xs leading-5 text-default-600"
                      >
                        {w.resent}
                      </p>
                    )}
                    <div className="text-center">
                      <p className="text-xs leading-5 text-default-500">
                        {w.inboxHelp}
                      </p>
                      <Button
                        variant="light"
                        size="sm"
                        className="mt-1 min-h-11 tabular-nums"
                        isDisabled={resendSeconds > 0 || busy === "verify"}
                        isLoading={busy === "resend"}
                        onPress={() => void sendCode(true)}
                      >
                        {resendSeconds > 0
                          ? w.resendIn.replace(
                              "{seconds}",
                              String(resendSeconds),
                            )
                          : w.resend}
                      </Button>
                    </div>
                  </>
                ) : (
                  <>
                    <Input
                      variant="bordered"
                      labelPlacement="outside"
                      placeholder="you@company.com"
                      classNames={fieldClasses}
                      autoFocus
                      label={t.email}
                      type="email"
                      autoComplete="email"
                      maxLength={254}
                      value={values.email}
                      onValueChange={(email) => setValues({ ...values, email })}
                      isReadOnly={!!busy}
                      isInvalid={!!errors.email}
                      errorMessage={errors.email}
                    />
                    <p className="rp-auth-email-note">
                      {w.loginNote}
                    </p>
                  </>
                )}
            </>
          </ModalBody>
          <ModalFooter className="rp-auth-footer">
                <Button
                  className="h-12 w-full"
                  endContent={<ArrowRight size={16} />}
                  color="primary"
                  type="submit"
                  isLoading={busy === "send" || busy === "verify"}
                  isDisabled={busy === "resend"}
                >
                  {checking ? w.verify : w.sendCode}
                </Button>
          </ModalFooter>
        </form>
      </ModalContent>
    </Modal>
  );
}
