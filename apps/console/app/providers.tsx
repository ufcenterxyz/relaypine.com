"use client";
import { HeroUIProvider, ToastProvider } from "@heroui/react";
import { ThemeProvider } from "next-themes";
import { useRouter } from "next/navigation";
export function Providers({ children }: { children: React.ReactNode }) {
  const router = useRouter();
  return (
    <HeroUIProvider navigate={router.push}>
      <ThemeProvider attribute="class" defaultTheme="light" enableSystem>
        <ToastProvider placement="bottom-right" toastProps={{ radius: "md" }} />
        {children}
      </ThemeProvider>
    </HeroUIProvider>
  );
}
