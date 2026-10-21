"use client";
import { Button } from "@heroui/react";
import { en as t } from "@/i18n/en";
export default function ErrorPage({ reset }: { reset: () => void }) {
  return (
    <main className="flex min-h-dvh flex-col items-center justify-center gap-4 p-6">
      <h1 className="text-xl font-semibold">{t.error}</h1>
      <Button color="primary" onPress={reset}>
        {t.retry}
      </Button>
    </main>
  );
}
