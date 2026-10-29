import type { BackupVendor, Credential, Endpoint, RequestLog } from "./types";

export const backupVendors: { id: BackupVendor; name: string }[] = [
  { id: "openrouter", name: "OpenRouter" },
  { id: "zenmux", name: "ZenMux" },
];

export interface BackupRoute {
  vendor: BackupVendor;
  name: string;
  endpoint: Endpoint;
  credential?: Credential;
}

export const isBackup = (endpoint?: Endpoint) => endpoint?.role === "backup";

// Backups in the order the gateway tries them: higher priority first.
export function backupRoutes(
  endpoints: Endpoint[] = [],
  credentials: Credential[] = [],
): BackupRoute[] {
  return endpoints
    .filter(isBackup)
    .flatMap((endpoint) => {
      const vendor = backupVendors.find((item) => item.id === endpoint.vendor);
      if (!vendor) return [];
      const credential = credentials.find(
        (item) => item.endpoint_id === endpoint.id,
      );
      return [{ vendor: vendor.id, name: vendor.name, endpoint, credential }];
    })
    .sort(
      (a, b) =>
        (b.credential?.priority ?? 0) - (a.credential?.priority ?? 0) ||
        a.vendor.localeCompare(b.vendor),
    );
}

// The backup that served a request, if any. Only administrator snapshots
// carry the credential behind a request.
export function servedByBackup(
  request: RequestLog,
  endpoints: Endpoint[] = [],
  credentials: Credential[] = [],
) {
  const credential = credentials.find((item) => item.id === request.credential_id);
  const endpoint = endpoints.find((item) => item.id === credential?.endpoint_id);
  return isBackup(endpoint)
    ? backupVendors.find((item) => item.id === endpoint?.vendor)
    : undefined;
}
