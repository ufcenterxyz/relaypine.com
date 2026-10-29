"use client";
import { Chip } from "@heroui/react";
import { backupEn as b } from "@/i18n/backup-en";
import { backupVendors, servedByBackup } from "@/lib/backups";
import type { RequestLog } from "@/lib/types";
import { useConsole } from "./console-provider";

// One line under a request in the administrator's table: the backup that
// served it, or how many routes were tried. Customers never see routing.
export function RouteHint({ request }: { request: RequestLog }) {
  const { state } = useConsole();
  if (state.surface !== "admin" || !state.user.operator) return null;
  const backup = servedByBackup(request, state.endpoints, state.credentials);
  const tried = request.attempts?.length ?? 1;
  if (!backup && tried < 2) return null;
  return (
    <p className="mt-1 text-xs text-default-500">
      {backup
        ? `${b.requestBackup} · ${backup.name}`
        : b.requestAttempts.replace("{count}", String(tried))}
    </p>
  );
}

// Every route tried for a rerouted request, oldest first.
export function RouteTrail({ request }: { request: RequestLog }) {
  const { state } = useConsole();
  if (state.surface !== "admin" || !state.user.operator || (request.attempts?.length ?? 0) < 2)
    return null;
  const label = (credentialID: string) => {
    const credential = state.credentials?.find((item) => item.id === credentialID);
    const endpoint = state.endpoints?.find((item) => item.id === credential?.endpoint_id);
    const vendor = backupVendors.find((item) => item.id === endpoint?.vendor);
    if (vendor) return `${b.requestBackup} · ${vendor.name}`;
    return credential?.name || credentialID;
  };
  return (
    <section className="mt-6">
      <h3 className="rp-request-label">{b.routesTried}</h3>
      <ol className="mt-2">
        {request.attempts!.map((attempt, index) => (
          <li
            key={`${attempt.credential_id}-${index}`}
            className="flex flex-wrap items-center justify-between gap-x-3 gap-y-1 border-b border-default-100 py-2.5 text-xs last:border-b-0"
          >
            <span>
              <span className="mr-2 tabular-nums text-default-400">{index + 1}</span>
              {label(attempt.credential_id)}
            </span>
            <span className="flex items-center gap-2">
              {attempt.error_code && (
                <code className="text-default-500">{attempt.error_code}</code>
              )}
              <Chip radius="sm" size="sm" variant="flat">
                {attempt.status === "failed_over" ? b.attemptRerouted : attempt.status}
              </Chip>
            </span>
          </li>
        ))}
      </ol>
      <p className="mt-2 text-xs leading-5 text-default-500">{b.routeHelp}</p>
    </section>
  );
}
