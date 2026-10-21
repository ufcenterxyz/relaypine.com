export interface User {
  id: string;
  tenant_id: string;
  email: string;
  name: string;
  operator: boolean;
}
export interface Tenant {
  id: string;
  name: string;
  status: string;
  created_at: string;
}
export interface Wallet {
  tenant_id: string;
  balance_micros: number;
  held_micros: number;
  currency: string;
}
export interface AccessKey {
  id: string;
  tenant_id: string;
  project_id: string;
  name: string;
  prefix: string;
  enabled: boolean;
  rpm: number;
  models: string[];
  created_at: string;
}
export interface Endpoint {
  id: string;
  name: string;
  provider: string;
  base_url: string;
  auth_mode: string;
  enabled: boolean;
  models: string[];
}
export interface Credential {
  id: string;
  name: string;
  endpoint_id: string;
  workspace_id: string;
  masked_key: string;
  quota_group: string;
  proxy_id: string;
  enabled: boolean;
  rpm: number;
  tpm: number;
  max_concurrency: number;
  budget_micros: number;
  spent_micros: number;
  held_micros: number;
  priority: number;
}
export interface Proxy {
  id: string;
  name: string;
  masked_url: string;
  enabled: boolean;
}
export interface Model {
  id: string;
  name: string;
  provider: string;
  upstream_model: string;
  enabled: boolean;
  billing_mode?: "metered" | "usage_only";
  input_rate: number;
  output_rate: number;
  cached_rate: number;
  cache_write_rate: number;
  cost_input_rate: number;
  cost_output_rate: number;
  cost_cached_rate: number;
  cost_cache_write_rate: number;
  max_output_tokens: number;
  version: number;
}
export interface RequestLog {
  id: string;
  tenant_id: string;
  project_id: string;
  access_key_id: string;
  credential_id?: string;
  provider: string;
  model: string;
  status: string;
  billing_mode?: "metered" | "usage_only";
  cost_status?: "untracked" | "recorded" | "pending";
  usage: {
    input_tokens: number;
    output_tokens: number;
    cached_tokens: number;
    cache_write_tokens: number;
    known: boolean;
  };
  charge_micros: number;
  cost_micros?: number;
  reserved_micros: number;
  latency_ms: number;
  error_code?: string;
  created_at: string;
}
export interface LedgerEntry {
  id: string;
  tenant_id: string;
  kind: string;
  amount_micros: number;
  reference: string;
  reason: string;
  created_at: string;
}
export interface WaitlistEntry {
  id: string;
  name: string;
  email: string;
  created_at: string;
}
export interface RouteConfig {
  strategy: string;
  tenant_rpm: number;
  tenant_concurrency: number;
}
export interface Snapshot {
  user: User;
  tenant: Tenant;
  project: { id: string; tenant_id: string; name: string };
  wallet: Wallet;
  keys: AccessKey[];
  models: Model[];
  requests: RequestLog[];
  ledger: LedgerEntry[];
  endpoints?: Endpoint[];
  credentials?: Credential[];
  proxies?: Proxy[];
  customers?: Tenant[];
  routing: RouteConfig;
  waitlist?: WaitlistEntry[];
}
