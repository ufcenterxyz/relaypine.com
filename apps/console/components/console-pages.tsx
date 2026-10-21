"use client";
import Link from "next/link";
import { useEffect } from "react";
import { useRouter } from "next/navigation";
import { Button } from "@heroui/react";
import { useConsole } from "./console-provider";
import {
  Overview,
  Keys,
  Usage,
  Billing,
  Customers,
  Waitlist,
} from "./customer-pages";
import { Models, Upstreams, Proxies, Routing } from "./operator-pages";
import { Empty } from "./shared";
import { en as t } from "@/i18n/en";
export function ConsolePage({ section }: { section: string }) {
  const { state } = useConsole();
  const router = useRouter();
  const forbidden =
    ["upstreams", "proxies", "routing", "customers", "waitlist"].includes(
      section,
    ) && !state.user.operator;
  useEffect(() => {
    if (forbidden) router.replace("/app");
  }, [forbidden, router]);
  if (forbidden)
    return (
      <Empty
        title={t.inaccessible}
        body={t.returnOverview}
        action={
          <Button as={Link} href="/app" color="primary">
            {t.returnOverview}
          </Button>
        }
      />
    );
  switch (section) {
    case "":
      return <Overview />;
    case "keys":
      return <Keys />;
    case "models":
      return <Models />;
    case "usage":
      return <Usage />;
    case "billing":
      return <Billing />;
    case "upstreams":
      return <Upstreams />;
    case "proxies":
      return <Proxies />;
    case "routing":
      return <Routing />;
    case "customers":
      return <Customers />;
    case "waitlist":
      return <Waitlist />;
    default:
      return (
        <Empty
          title={t.notFound}
          body={t.notFoundBody}
          action={
            <Button as={Link} href="/app" color="primary">
              {t.returnOverview}
            </Button>
          }
        />
      );
  }
}
