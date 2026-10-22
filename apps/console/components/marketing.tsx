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
import {
  ArrowRight,
  ArrowUpRight,
  ArrowUp,
  Github,
  Route,
  KeyRound,
  ReceiptText,
  Braces,
  Check,
} from "lucide-react";
import { en as t } from "@/i18n/en";
import { marketing as m } from "@/i18n/marketing-en";
import { authEn as w } from "@/i18n/auth-en";
import { GITHUB_REPOSITORY_URL } from "@/config/links";
import { RoutingDemo } from "./routing-demo";
import { AuthModal } from "./auth-modal";
import { Brand, ThemeToggle, CodeBlock } from "./shared";

export function sdkCode(provider: string) {
  return provider === "openai"
    ? `import OpenAI from "openai";\n\nconst client = new OpenAI({\n  apiKey: process.env.RELAYPINE_API_KEY,\n  baseURL: "https://relaypine.com/openai/v1",\n});\n\nconst response = await client.responses.create({\n  model: "MODEL_ID",\n  input: "Hello from RelayPine",\n});`
    : `import Anthropic from "@anthropic-ai/sdk";\n\nconst client = new Anthropic({\n  apiKey: process.env.RELAYPINE_API_KEY,\n  baseURL: "https://relaypine.com/anthropic",\n});\n\nconst response = await client.messages.create({\n  model: "MODEL_ID",\n  max_tokens: 1024,\n  messages: [{ role: "user", content: "Hello" }],\n});`;
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
        backdrop: "bg-black/30 backdrop-blur-sm",
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
            onSelectionChange={(key) => setProvider(String(key))}
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
  const [auth, setAuth] = useState(false);
  const [docs, setDocs] = useState(false);
  const [provider, setProvider] = useState("openai");
  useEffect(() => {
    const mode = new URLSearchParams(window.location.search).get("auth");
    if (mode === "login" || mode === "waitlist") {
      const timer = setTimeout(() => setAuth(true), 0);
      return () => clearTimeout(timer);
    }
  }, []);

  const capabilities = [
    {
      number: "01",
      icon: Route,
      title: m.routingTitle,
      description: m.routingDescription,
      detail: m.routingDetail,
    },
    {
      number: "02",
      icon: ReceiptText,
      title: m.walletTitle,
      description: m.walletDescription,
      detail: m.walletDetail,
    },
    {
      number: "03",
      icon: KeyRound,
      title: m.keysTitle,
      description: m.keysDescription,
      detail: m.keysDetail,
    },
  ];

  return (
    <div className="rp-marketing" id="top">
      <a className="rp-skip-link" href="#main">
        Skip to content
      </a>
      <header className="rp-site-header">
        <div className="rp-wrap rp-header-inner">
          <Brand />
          <nav aria-label="Main navigation" className="rp-marketing-nav">
            <a href="#gateway">{m.productNav}</a>
            <a href="#quickstart">{m.integrationNav}</a>
            <a
              href={GITHUB_REPOSITORY_URL}
              target="_blank"
              rel="noopener noreferrer"
              aria-label={t.githubSourceNewTab}
            >
              <Github size={15} aria-hidden="true" />
              <span>{m.sourceNav}</span>
            </a>
          </nav>
          <div className="rp-header-actions">
            <ThemeToggle />
            <Button
              onPress={() => setAuth(true)}
              className="rp-header-signin"
              endContent={<ArrowUpRight size={15} />}
            >
              {w.signIn}
            </Button>
          </div>
        </div>
      </header>
      <main id="main">
        <section className="rp-hero rp-wrap" aria-labelledby="hero-title">
          <div className="rp-hero-overline">
            <p className="rp-eyebrow">
              <span className="rp-signal-square" />
              {m.eyebrow}
            </p>
            <span className="rp-hero-coordinate" aria-hidden="true">
              RP / 001
            </span>
          </div>
          <div className="rp-hero-intro">
            <h1 id="hero-title">
              {m.title.split("\n").map((line, index) => (
                <span
                  key={line}
                  className={index ? "rp-hero-second" : undefined}
                >
                  {line}
                </span>
              ))}
            </h1>
            <div className="rp-hero-aside">
              <p className="rp-hero-description">{m.description}</p>
              <div className="rp-hero-actions">
                <Button
                  color="primary"
                  size="lg"
                  onPress={() => setAuth(true)}
                  endContent={<ArrowUpRight size={18} />}
                >
                  {w.getStarted}
                </Button>
                <a href="#quickstart">
                  {m.integrationNav}
                  <ArrowRight size={16} />
                </a>
              </div>
              <p className="rp-hero-note">
                <Github size={13} aria-hidden="true" />
                {m.heroNote}
              </p>
            </div>
          </div>
          <div className="rp-providers">
            <p className="rp-eyebrow">{m.supported}</p>
            <div className="rp-provider">
              <span className="rp-provider-name">{t.openai}</span>
              <span className="rp-provider-detail">{m.openaiDetail}</span>
            </div>
            <div className="rp-provider">
              <span className="rp-provider-name">{t.anthropic}</span>
              <span className="rp-provider-detail">{m.anthropicDetail}</span>
            </div>
          </div>
        </section>

        <section
          className="rp-wrap rp-workbench-section"
          id="gateway"
          aria-label={m.productNav}
        >
          <div className="rp-section-index">
            <span>{m.workbenchLabel}</span>
            <span>{m.workbenchHint}</span>
          </div>
          <RoutingDemo />
        </section>

        <section
          className="rp-wrap rp-native-section"
          id="quickstart"
          aria-labelledby="native-title"
        >
          <div className="rp-section-copy">
            <p className="rp-eyebrow">{m.nativeLabel}</p>
            <h2 id="native-title">{m.nativeTitle}</h2>
            <p className="rp-body-copy">{m.nativeDescription}</p>
            <ol className="rp-connection-steps">
              {[
                [m.stepOne, m.stepOneDetail],
                [m.stepTwo, m.stepTwoDetail],
                [m.stepThree, m.stepThreeDetail],
              ].map(([title, description], index) => (
                <li key={title}>
                  <span className="rp-step-number">0{index + 1}</span>
                  <div>
                    <h3>{title}</h3>
                    <p>{description}</p>
                  </div>
                </li>
              ))}
            </ol>
          </div>
          <div className="rp-sdk-panel">
            <div className="rp-sdk-heading">
              <Braces size={18} />
              <span>{m.integrationNav}</span>
              <span className="rp-sdk-language">TypeScript</span>
            </div>
            <Tabs
              role="group"
              aria-label={t.provider}
              selectedKey={provider}
              onSelectionChange={(key) => setProvider(String(key))}
              variant="underlined"
              color="primary"
              classNames={{
                base: "rp-sdk-tabs",
                tabList: "gap-6 p-0",
                tab: "h-12 px-0",
                cursor: "w-full",
              }}
            >
              <Tab key="openai" title={t.openai} />
              <Tab key="anthropic" title={t.anthropic} />
            </Tabs>
            <CodeBlock label={m.codeFile} code={sdkCode(provider)} tone="ink" />
            <p className="rp-code-hint">
              <Check size={14} aria-hidden="true" />
              {t.integrationBody}
            </p>
          </div>
        </section>

        <section
          className="rp-capabilities"
          aria-labelledby="capabilities-title"
        >
          <div className="rp-wrap">
            <div className="rp-capabilities-heading">
              <div>
                <p className="rp-eyebrow">{m.capabilitiesLabel}</p>
                <h2 id="capabilities-title">{m.capabilitiesTitle}</h2>
              </div>
              <p className="rp-body-copy">{m.capabilitiesDescription}</p>
            </div>
            <div className="rp-capability-grid">
              {capabilities.map(
                ({ number, icon: Icon, title, description, detail }) => (
                  <article key={number}>
                    <div className="rp-capability-top">
                      <span>{number}</span>
                      <Icon size={24} strokeWidth={1.4} />
                    </div>
                    <h3>{title}</h3>
                    <p>{description}</p>
                    <span className="rp-feature-detail">{detail}</span>
                  </article>
                ),
              )}
            </div>
          </div>
        </section>

        <section className="rp-source-section" aria-labelledby="source-title">
          <div className="rp-wrap rp-source-grid">
            <div>
              <p className="rp-eyebrow">
                <Github size={15} aria-hidden="true" />
                {m.sourceLabel}
              </p>
              <h2 id="source-title">{m.sourceTitle}</h2>
            </div>
            <div className="rp-source-aside">
              <p>{m.sourceDescription}</p>
              <a
                href={GITHUB_REPOSITORY_URL}
                target="_blank"
                rel="noopener noreferrer"
                aria-label={t.githubSourceNewTab}
              >
                {m.sourceCta}
                <ArrowUpRight size={19} />
              </a>
              <span className="rp-source-license">
                MIT LICENSE <span aria-hidden="true">/</span> GITHUB
              </span>
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
            onPress={() => setAuth(true)}
            endContent={<ArrowUpRight size={18} />}
          >
            {w.getStarted}
          </Button>
        </section>
      </main>
      <footer className="rp-site-footer">
        <div className="rp-wrap">
          <div className="rp-footer-main">
            <Brand />
            <p>{m.footerTagline}</p>
            <a href="#top">
              {m.backTop}
              <ArrowUp size={15} />
            </a>
          </div>
          <div className="rp-footer-bottom">
            <p>{t.copyright}</p>
            <div>
              <button type="button" onClick={() => setDocs(true)}>
                {t.integration}
              </button>
              <a
                href={GITHUB_REPOSITORY_URL}
                target="_blank"
                rel="noopener noreferrer"
                aria-label={t.githubSourceNewTab}
              >
                GitHub
                <ArrowUpRight size={12} />
              </a>
              <span>{t.providerNote}</span>
            </div>
          </div>
        </div>
      </footer>
      {auth && <AuthModal onClose={() => setAuth(false)} />}
      {docs && <Integration onClose={() => setDocs(false)} />}
    </div>
  );
}
