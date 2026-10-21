import type { Metadata, Viewport } from "next";
import "@/styles/globals.css";
import { GeistSans } from "geist/font/sans";
import { GeistMono } from "geist/font/mono";
import { Providers } from "./providers";
export const metadata: Metadata = {
  title: {
    default: "RelayPine | One gateway for your AI",
    template: "%s | RelayPine",
  },
  description:
    "Connect to Claude and OpenAI through one API gateway. Native APIs, capacity-aware routing, and clear usage. Join the waitlist for access.",
};
export const viewport: Viewport = {
  themeColor: [
    { media: "(prefers-color-scheme: light)", color: "#ffffff" },
    { media: "(prefers-color-scheme: dark)", color: "#101713" },
  ],
};
export default function RootLayout({
  children,
}: Readonly<{ children: React.ReactNode }>) {
  return (
    <html
      lang="en"
      className={`${GeistSans.variable} ${GeistMono.variable}`}
      suppressHydrationWarning
    >
      <body className="bg-background text-foreground font-sans antialiased">
        <Providers>{children}</Providers>
      </body>
    </html>
  );
}
