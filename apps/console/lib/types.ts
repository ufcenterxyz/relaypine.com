export interface User {
  id: string;
  tenant_id: string;
  email: string;
  name: string;
  operator: boolean;
}
export interface Tenant {
  email?: string;
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
export type BackupVendor = "openrouter" | "zenmux";
export interface Endpoint {
  id: string;
  name: string;
  provider: string;
  base_url: string;
  auth_mode: string;
  enabled: boolean;
  models: string[];
  // Backup endpoints serve both protocols through a fixed vendor preset.
  role?: "backup";
  vendor?: BackupVendor;
  // A backup has a saved management key for reading the account balance.
  management_key?: boolean;
}
export interface BackupBalance {
  vendor: BackupVendor;
  balance_micros?: number;
  key_limit_remaining_micros?: number;
  management_key: boolean;
  checked_at: string;
  error?: string;
}
export interface BackupCoverage {
  id: string;
  name: string;
  provider: string;
  enabled: boolean;
  backup_model?: string;
  available: boolean;
}
export interface BackupCheck {
  vendor: BackupVendor;
  key_label?: string;
  balance: BackupBalance;
  models: BackupCoverage[];
  checked_at: string;
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
// The price of requests whose prompt, cache reads and writes included, is
// longer than above_input_tokens. The whole request then pays these rates.
export interface LongContext {
  above_input_tokens: number;
  input_rate: number;
  output_rate: number;
  cached_rate: number;
  cache_write_rate: number;
  cost_input_rate: number;
  cost_output_rate: number;
  cost_cached_rate: number;
  cost_cache_write_rate: number;
}
export interface Model {
  pricing_reference?: { provider: string; model: string; source_url: string; source_name: string; checked_at: string; hash: string; scope: "standard_text" | "standard_image"; max_input_tokens?: number; all_prompt_lengths?: boolean };
  pricing_note?: string;
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
  // Image generation models. Text rates then price text input and output.
  kind?: "image";
  image_input_rate?: number;
  image_output_rate?: number;
  cost_image_input_rate?: number;
  cost_image_output_rate?: number;
  long_context?: LongContext | null;
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
    image_input_tokens?: number;
    image_output_tokens?: number;
    image_text_tokens?: number;
    known: boolean;
  };
  charge_micros: number;
  cost_micros?: number;
  reserved_micros: number;
  latency_ms: number;
  error_code?: string;
  created_at: string;
  // Set when the prompt was long enough to pay the model's long context rates.
  long_context?: boolean;
  // Administrator only: every upstream tried when the request was rerouted.
  attempts?: { credential_id: string; status: string; error_code?: string }[];
}
export interface CapturedRequest {
  method: string;
  path: string;
  headers?: Record<string, string>;
  body: string;
  bytes: number;
  truncated?: boolean;
}
export interface CapturedResponse {
  status: number;
  content_type?: string;
  body: string;
  bytes: number;
  truncated?: boolean;
}
export interface RequestPayload {
  request: CapturedRequest;
  response: CapturedResponse;
  /** The provider's reply when the client did not receive it. Administrator only. */
  upstream?: CapturedResponse;
}
/** Why a request detail has no payload; "stored" when it has one. */
export type PayloadStatus =
  | "stored"
  | "pending"
  | "not_recorded"
  | "disabled"
  | "expired"
  | "archived"
  | "unavailable";
export interface RequestDetail {
  request: RequestLog;
  payload?: RequestPayload;
  payload_status: PayloadStatus;
  /** 0 when bodies are kept indefinitely. */
  retention_days: number;
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
// The per-minute rate limit an official upstream stated for a key and model
// in its latest response.
export interface ReportedLimit {
  model: string;
  requests?: number;
  tokens?: number;
  input_tokens?: number;
  output_tokens?: number;
  observed_at: string;
}
export interface Snapshot {
  surface: "admin" | "customer";
  app_url: string;
  admin_app_url: string;
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
  // Administrator only: reported limits by credential ID.
  reported_limits?: Record<string, ReportedLimit[]>;
}
