import { ConsoleProvider } from "@/components/console-provider";
import { ConsoleShell } from "@/components/console-shell";
export default function AppLayout({ children }: { children: React.ReactNode }) {
  return (
    <ConsoleProvider>
      <ConsoleShell>{children}</ConsoleShell>
    </ConsoleProvider>
  );
}
