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
import { motion, useReducedMotion } from "framer-motion";
import {
  ArrowRight,
  ArrowUpRight,
  Braces,
  Check,
  KeyRound,
  Layers3,
  Route,
  Activity,
} from "lucide-react";
import { en as t } from "@/i18n/en";
import { marketing as m } from "@/i18n/marketing-en";
import { RoutingDemo } from "./routing-demo";
import { AuthModal, type AuthMode } from "./auth-modal";
import { waitlistEn as w } from "@/i18n/waitlist-en";
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
      classNames={{
        base: "rp-modal max-h-[85dvh]",
        backdrop: "bg-[#0c2117]/25 backdrop-blur-sm",
      }}
    >
      <ModalContent>
        <ModalHeader className="pt-7 text-xl font-medium tracking-tight">
          {t.integrationTitle}
        </ModalHeader>
        <ModalBody className="gap-4 pb-6">
          <p className="text-sm leading-6 text-default-500">
            {t.integrationBody}
          </p>
          <Tabs
            role="group"
            aria-label={t.provider}
            selectedKey={provider}
            onSelectionChange={(k) => setProvider(String(k))}
            variant="underlined"
            color="primary"
          >
            <Tab key="openai" title={t.openai} />
            <Tab key="anthropic" title={t.anthropic} />
          </Tabs>
          <CodeBlock label={m.codeFile} code={sdkCode(provider)} tone="ink" />
        </ModalBody>
      </ModalContent>
    </Modal>
  );
}
export function Marketing() {
  const [auth, setAuth] = useState<AuthMode | null>(null);
  const [docs, setDocs] = useState(false);
  const [provider, setProvider] = useState("openai");
  const reducedMotion = useReducedMotion();
  useEffect(() => {
    const mode = new URLSearchParams(window.location.search).get("auth");
    if (mode === "login" || mode === "waitlist") {
      const timer = setTimeout(() => setAuth(mode), 0);
      return () => clearTimeout(timer);
    }
  }, []);
  const entrance = {
    initial: { opacity: 1, y: reducedMotion ? 0 : 14 },
    animate: { opacity: 1, y: 0 },
    transition: {
      duration: reducedMotion ? 0 : 0.65,
      ease: "easeOut" as const,
    },
  };
  return (
    <div className="rp-marketing">
      <header className="rp-site-header">
        <div className="rp-wrap rp-header-inner">
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
              {w.adminSignIn}
            </Button>
            <Button
              onPress={() => setAuth("waitlist")}
              color="primary"
              className="hidden sm:inline-flex"
              endContent={<ArrowUpRight size={15} />}
            >
              {w.join}
            </Button>
          </nav>
        </div>
      </header>
      <main>
        <section className="rp-hero">
          <div className="rp-wrap rp-hero-grid">
            <motion.div {...entrance} className="rp-hero-copy">
              <p className="rp-eyebrow">
                <span className="rp-status-dot" />
                {m.eyebrow}
              </p>
              <h1>
                {m.title.split("\n").map((line, i) => (
                  <span key={line} className={i ? "rp-hero-second" : undefined}>
                    {line}
                  </span>
                ))}
              </h1>
              <p className="rp-hero-description">{m.description}</p>
              <div className="rp-hero-actions">
                <Button
                  size="lg"
                  color="primary"
                  onPress={() => setAuth("waitlist")}
                  endContent={<ArrowUpRight size={17} />}
                >
                  {w.join}
                </Button>
                <Button
                  size="lg"
                  variant="light"
                  onPress={() => setDocs(true)}
                  endContent={<ArrowRight size={16} />}
                >
                  {t.integration}
                </Button>
              </div>
              <p className="rp-hero-note">
                <Braces size={14} />
                {m.heroNote}
              </p>
            </motion.div>
            <div className="rp-hero-visual">
              <RoutingDemo />
            </div>
          </div>
          <div className="rp-wrap rp-providers">
            <p className="rp-eyebrow">{m.supported}</p>
            <div className="rp-provider">
              <span className="rp-provider-name">{t.openai}</span>
              <span className="rp-provider-detail">{m.openaiDetail}</span>
            </div>
            <div className="rp-provider">
              <span className="rp-provider-name rp-provider-anthropic">
                {t.anthropic}
              </span>
              <span className="rp-provider-detail">{m.anthropicDetail}</span>
            </div>
          </div>
        </section>
        <section className="rp-wrap rp-native-section">
          <div className="rp-section-copy">
            <p className="rp-eyebrow">{m.nativeLabel}</p>
            <h2>{m.nativeTitle}</h2>
            <p className="rp-body-copy">{m.nativeDescription}</p>
            <ul className="rp-native-points">
              <li>
                <Check size={16} />
                {m.nativeProtocol}
              </li>
              <li>
                <Check size={16} />
                {m.streamProtocol}
              </li>
            </ul>
            <Button
              onPress={() => setDocs(true)}
              variant="light"
              className="rp-text-link"
              endContent={<ArrowUpRight size={16} />}
            >
              {m.modelLink}
            </Button>
          </div>
          <div className="rp-sdk-panel">
            <Tabs
              role="group"
              aria-label={t.provider}
              selectedKey={provider}
              onSelectionChange={(k) => setProvider(String(k))}
              variant="underlined"
              color="primary"
              classNames={{
                tabList: "gap-7 p-0 mb-5",
                tab: "px-0",
                cursor: "w-full",
              }}
            >
              <Tab key="openai" title={t.openai} />
              <Tab key="anthropic" title={t.anthropic} />
            </Tabs>
            <CodeBlock label={m.codeFile} code={sdkCode(provider)} tone="ink" />
            <p className="rp-code-hint">{t.integrationBody}</p>
          </div>
        </section>
        <section className="rp-capabilities">
          <div className="rp-wrap">
            <div className="rp-capabilities-heading">
              <div>
                <p className="rp-eyebrow">{m.capabilitiesLabel}</p>
                <h2>{m.capabilitiesTitle}</h2>
              </div>
              <p className="rp-body-copy">{m.capabilitiesDescription}</p>
            </div>
            <div className="rp-capability-grid">
              <article className="rp-routing-feature">
                <Route size={23} strokeWidth={1.4} />
                <h3>{m.routingTitle}</h3>
                <p>{m.routingDescription}</p>
                <div
                  className="rp-routing-path"
                  role="group"
                  aria-label={m.routingCaption}
                >
                  <div>
                    <Braces size={20} />
                    <span>{m.routingFrom}</span>
                  </div>
                  <span className="rp-path-line">
                    <ArrowRight size={15} />
                  </span>
                  <div className="rp-path-core">
                    <Layers3 size={24} strokeWidth={1.4} />
                    <span>{t.brand}</span>
                  </div>
                  <span className="rp-path-line">
                    <ArrowRight size={15} />
                  </span>
                  <div>
                    <Route size={20} />
                    <span>{m.routingTo}</span>
                  </div>
                </div>
                <p className="rp-feature-detail">{m.routingCaption}</p>
              </article>
              <div className="rp-side-features">
                <article>
                  <Activity size={21} strokeWidth={1.4} />
                  <div>
                    <h3>{m.walletTitle}</h3>
                    <p>{m.walletDescription}</p>
                    <p className="rp-feature-detail">{m.walletDetail}</p>
                  </div>
                </article>
                <article>
                  <KeyRound size={21} strokeWidth={1.4} />
                  <div>
                    <h3>{m.keysTitle}</h3>
                    <p>{m.keysDescription}</p>
                    <p className="rp-feature-detail">{m.keysDetail}</p>
                  </div>
                </article>
              </div>
            </div>
          </div>
        </section>
        <section className="rp-wrap rp-final-cta">
          <div>
            <h2>{m.startTitle}</h2>
            <p>{m.startDescription}</p>
          </div>
          <Button
            size="lg"
            color="primary"
            onPress={() => setAuth("waitlist")}
            endContent={<ArrowUpRight size={18} />}
          >
            {w.join}
          </Button>
        </section>
      </main>
      <footer className="rp-site-footer">
        <div className="rp-wrap">
          <div className="rp-footer-main">
            <Brand />
            <p>{m.footerTagline}</p>
          </div>
          <div className="rp-footer-bottom">
            <p>{t.copyright}</p>
            <p>{t.providerNote}</p>
          </div>
        </div>
      </footer>
      {auth && (
        <AuthModal mode={auth} onMode={setAuth} onClose={() => setAuth(null)} />
      )}
      {docs && <Integration onClose={() => setDocs(false)} />}
    </div>
  );
}
