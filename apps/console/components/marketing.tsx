"use client";
import { useEffect, useState } from "react";
import {
  Button,
  Modal,
  ModalContent,
  ModalHeader,
  ModalBody,
  Tabs,
  Tab,
} from "@heroui/react";
import Link from "next/link";
import {
  ArrowRight,
  Braces,
  Cable,
  Coins,
  Network,
  Terminal,
} from "lucide-react";
import { en as t } from "@/i18n/en";
import { AuthModal } from "./auth-modal";
import { Brand, ThemeToggle, CodeBlock } from "./shared";
export function sdkCode(provider: string) {
  return provider === "openai"
    ? `import OpenAI from "openai";\n\nconst client = new OpenAI({\n  apiKey: process.env.RELAYPINE_API_KEY,\n  baseURL: "https://relaypine.com/openai/v1",\n});\n\nconst response = await client.responses.create({\n  model: "MODEL_ID",\n  input: "Hello from RelayPine",\n});`
    : `import Anthropic from "@anthropic-ai/sdk";\n\nconst client = new Anthropic({\n  apiKey: process.env.RELAYPINE_API_KEY,\n  baseURL: "https://relaypine.com/anthropic",\n});\n\nconst response = await client.messages.create({\n  model: "MODEL_ID",\n  max_tokens: 1024,\n  messages: [{role: "user", content: "Hello"}],\n});`;
}
export function Integration({ onClose }: { onClose: () => void }) {
  const [provider, setProvider] = useState("openai");
  return (
    <Modal
      isOpen
      onClose={onClose}
      size="2xl"
      scrollBehavior="inside"
      classNames={{ base: "max-h-[85dvh]" }}
    >
      <ModalContent>
        <ModalHeader>{t.integrationTitle}</ModalHeader>
        <ModalBody className="pb-6">
          <p className="mb-3 text-sm leading-6 text-default-500">
            {t.integrationBody}
          </p>
          <Tabs
            selectedKey={provider}
            onSelectionChange={(k) => setProvider(String(k))}
          >
            <Tab key="openai" title={t.openai} />
            <Tab key="anthropic" title={t.anthropic} />
          </Tabs>
          <CodeBlock label={t.integrationCode} code={sdkCode(provider)} />
        </ModalBody>
      </ModalContent>
    </Modal>
  );
}
export function Marketing() {
  const [auth, setAuth] = useState<"login" | "register" | null>(null);
  const [docs, setDocs] = useState(false);
  useEffect(() => {
    if (new URLSearchParams(window.location.search).get("auth") === "login") {
      const timer = setTimeout(() => setAuth("login"), 0);
      return () => clearTimeout(timer);
    }
  }, []);
  return (
    <div className="flex min-h-dvh flex-col">
      <header className="sticky top-0 z-20 border-b border-default-100 bg-background/95 backdrop-blur">
        <div className="mx-auto flex h-20 max-w-6xl items-center justify-between gap-3 px-5 sm:px-8">
          <Brand />
          <nav aria-label={t.menu} className="flex items-center gap-1 sm:gap-3">
            <Button
              variant="light"
              className="hidden sm:inline-flex"
              onPress={() => setDocs(true)}
            >
              {t.integration}
            </Button>
            <ThemeToggle />
            <Button variant="light" onPress={() => setAuth("login")}>
              {t.signIn}
            </Button>
            <Button
              as={Link}
              href="/app"
              color="primary"
              className="hidden sm:inline-flex"
            >
              {t.console}
            </Button>
          </nav>
        </div>
      </header>
      <main className="flex-1">
        <section className="mx-auto grid max-w-6xl items-center gap-14 px-5 py-16 sm:px-8 sm:py-24 lg:grid-cols-[1.1fr_1fr] lg:gap-12">
          <div>
            <p className="mb-6 text-[11px] font-semibold tracking-[.16em] text-default-500">
              {t.landingEyebrow}
            </p>
            <h1 className="whitespace-pre-line text-[clamp(2.6rem,5vw,4.4rem)] font-semibold leading-[1.07] tracking-[-.055em]">
              {t.landingTitle}
            </h1>
            <p className="mt-7 max-w-md text-lg leading-8 text-default-500">
              {t.landingDescription}
            </p>
            <div className="mt-9 flex flex-wrap gap-3">
              <Button
                size="lg"
                color="primary"
                onPress={() => setAuth("register")}
                endContent={<ArrowRight size={17} />}
              >
                {t.getStarted}
              </Button>
              <Button
                size="lg"
                variant="bordered"
                onPress={() => setDocs(true)}
              >
                {t.integration}
              </Button>
            </div>
          </div>
          <div className="rounded-2xl border border-default-200 p-6 sm:p-8">
            <div className="flex items-center gap-3 text-sm">
              <Terminal size={20} />
              <span className="font-medium">{t.apiRequest}</span>
            </div>
            <div className="ml-2.5 my-3 h-8 border-l border-dashed border-default-300" />
            <div className="rounded-xl border border-default-200 bg-default-50 p-5">
              <div className="flex items-center justify-between">
                <span className="font-semibold tracking-tight">
                  {t.relayGateway}
                </span>
                <Network size={20} strokeWidth={1.5} />
              </div>
              <div className="mt-4 flex flex-wrap gap-x-4 gap-y-2 font-mono text-[11px] text-default-500">
                <span>{t.flowAuth}</span>
                <span>{t.flowRoute}</span>
                <span>{t.flowStream}</span>
                <span>{t.flowMeter}</span>
              </div>
            </div>
            <div className="ml-2.5 my-3 h-8 border-l border-dashed border-default-300" />
            <div className="grid grid-cols-2 gap-3">
              <div className="rounded-lg border border-default-200 px-4 py-4">
                <p className="font-medium">{t.openai}</p>
                <p className="mt-1 font-mono text-[11px] text-default-500">
                  /responses
                </p>
              </div>
              <div className="rounded-lg border border-default-200 px-4 py-4">
                <p className="font-medium">{t.anthropic}</p>
                <p className="mt-1 font-mono text-[11px] text-default-500">
                  /messages
                </p>
              </div>
            </div>
          </div>
        </section>
        <section className="border-y border-default-200">
          <div className="mx-auto max-w-6xl px-5 py-16 sm:px-8">
            <div className="max-w-xl">
              <h2 className="text-3xl font-semibold tracking-tight">
                {t.nativeTitle}
              </h2>
              <p className="mt-4 text-default-500 leading-7">
                {t.nativeDescription}
              </p>
            </div>
            <div className="mt-10 grid gap-10 sm:grid-cols-3">
              {[
                { icon: Braces, title: t.nativeFeature, body: t.nativeBody },
                { icon: Coins, title: t.billingFeature, body: t.billingBody },
                { icon: Cable, title: t.routingFeature, body: t.routingBody },
              ].map((item) => (
                <article key={item.title}>
                  <item.icon size={22} strokeWidth={1.4} />
                  <h3 className="mt-4 font-medium">{item.title}</h3>
                  <p className="mt-2 text-sm leading-7 text-default-500">
                    {item.body}
                  </p>
                </article>
              ))}
            </div>
          </div>
        </section>
        <section className="mx-auto max-w-6xl px-5 py-16 sm:px-8">
          <h2 className="text-3xl font-semibold tracking-tight">
            {t.howTitle}
          </h2>
          <div className="mt-10 grid gap-9 lg:grid-cols-[.7fr_1.3fr]">
            <ol className="space-y-8">
              {[
                { title: t.stepAccount, body: t.stepAccountBody },
                { title: t.stepFund, body: t.stepFundBody },
                { title: t.stepKey, body: t.stepKeyBody },
              ].map((step, i) => (
                <li key={step.title} className="flex gap-4">
                  <span className="flex size-7 shrink-0 items-center justify-center rounded-full border border-default-300 font-mono text-xs text-default-500">
                    {i + 1}
                  </span>
                  <div>
                    <h3 className="font-medium">{step.title}</h3>
                    <p className="mt-2 max-w-xs text-sm leading-6 text-default-500">
                      {step.body}
                    </p>
                  </div>
                </li>
              ))}
            </ol>
            <CodeBlock label={t.openai} code={sdkCode("openai")} />
          </div>
        </section>
      </main>
      <footer className="border-t border-default-200">
        <div className="mx-auto flex max-w-6xl flex-wrap items-center justify-between gap-4 px-5 py-6 text-xs text-default-500 sm:px-8">
          <p>{t.copyright}</p>
          <p>{t.providerNote}</p>
        </div>
      </footer>
      {auth && (
        <AuthModal mode={auth} onMode={setAuth} onClose={() => setAuth(null)} />
      )}{" "}
      {docs && <Integration onClose={() => setDocs(false)} />}
    </div>
  );
}
