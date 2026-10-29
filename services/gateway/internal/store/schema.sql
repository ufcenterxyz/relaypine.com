CREATE TABLE IF NOT EXISTS tenants (
 id VARCHAR(64) PRIMARY KEY, name VARCHAR(200) NOT NULL, status VARCHAR(32) NOT NULL DEFAULT 'active', created_at DATETIME(6) NOT NULL
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS users (
 id VARCHAR(64) PRIMARY KEY, tenant_id VARCHAR(64) NOT NULL, email VARCHAR(254) NOT NULL UNIQUE, name VARCHAR(200) NOT NULL,
 password_hash VARCHAR(255) NOT NULL, operator BOOLEAN NOT NULL DEFAULT FALSE, created_at DATETIME(6) NOT NULL,
 FOREIGN KEY (tenant_id) REFERENCES tenants(id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS projects (
 id VARCHAR(64) PRIMARY KEY, tenant_id VARCHAR(64) NOT NULL, name VARCHAR(200) NOT NULL, created_at DATETIME(6) NOT NULL,
 FOREIGN KEY (tenant_id) REFERENCES tenants(id), INDEX(tenant_id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS wallets (
 tenant_id VARCHAR(64) PRIMARY KEY, balance_micros BIGINT NOT NULL DEFAULT 0, held_micros BIGINT NOT NULL DEFAULT 0,
 currency VARCHAR(8) NOT NULL DEFAULT 'usd', FOREIGN KEY (tenant_id) REFERENCES tenants(id),
 CHECK(balance_micros >= 0), CHECK(held_micros >= 0), CHECK(balance_micros >= held_micros)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS sessions (
 token_hash CHAR(64) PRIMARY KEY, user_id VARCHAR(64) NOT NULL, expires_at DATETIME(6) NOT NULL,
 auth_method VARCHAR(32) NOT NULL DEFAULT 'legacy',
 FOREIGN KEY (user_id) REFERENCES users(id), INDEX(expires_at)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS access_keys (
 id VARCHAR(64) PRIMARY KEY, tenant_id VARCHAR(64) NOT NULL, project_id VARCHAR(64) NOT NULL, name VARCHAR(200) NOT NULL,
 prefix VARCHAR(32) NOT NULL, token_hash CHAR(64) NOT NULL UNIQUE, enabled BOOLEAN NOT NULL DEFAULT TRUE,
 rpm INT NOT NULL, models JSON NOT NULL, created_at DATETIME(6) NOT NULL,
 FOREIGN KEY (tenant_id) REFERENCES tenants(id), FOREIGN KEY (project_id) REFERENCES projects(id), INDEX(tenant_id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS endpoints (
 id VARCHAR(64) PRIMARY KEY, config JSON NOT NULL
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS proxies (
 id VARCHAR(64) PRIMARY KEY, config JSON NOT NULL, encrypted_url BLOB NOT NULL
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS backup_management_keys (
 vendor VARCHAR(32) PRIMARY KEY, encrypted_key BLOB NOT NULL, updated_at DATETIME(6) NOT NULL
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS credentials (
 id VARCHAR(64) PRIMARY KEY, endpoint_id VARCHAR(64) NOT NULL, config JSON NOT NULL, encrypted_secret BLOB NOT NULL,
 budget_micros BIGINT NOT NULL DEFAULT 0, spent_micros BIGINT NOT NULL DEFAULT 0, held_micros BIGINT NOT NULL DEFAULT 0,
 FOREIGN KEY (endpoint_id) REFERENCES endpoints(id), CHECK(budget_micros >= 0), CHECK(spent_micros >= 0), CHECK(held_micros >= 0)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS models (
 id VARCHAR(64) PRIMARY KEY, config JSON NOT NULL, version INT NOT NULL DEFAULT 1
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS settings (
 id VARCHAR(64) PRIMARY KEY, config JSON NOT NULL
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS requests (
 id VARCHAR(64) PRIMARY KEY, tenant_id VARCHAR(64) NOT NULL, project_id VARCHAR(64) NOT NULL, access_key_id VARCHAR(64) NOT NULL,
 credential_id VARCHAR(64) NOT NULL, provider VARCHAR(32) NOT NULL, model VARCHAR(200) NOT NULL,
 status VARCHAR(32) NOT NULL, phase VARCHAR(32) NOT NULL, usage_json JSON NOT NULL, price_snapshot JSON NOT NULL,
 charge_micros BIGINT NOT NULL DEFAULT 0, cost_micros BIGINT NOT NULL DEFAULT 0, reserved_micros BIGINT NOT NULL,
 cost_reserved_micros BIGINT NOT NULL, customer_hold_active BOOLEAN NOT NULL DEFAULT TRUE,
 cost_hold_active BOOLEAN NOT NULL DEFAULT TRUE, latency_ms BIGINT NOT NULL DEFAULT 0, error_code VARCHAR(128) NOT NULL DEFAULT '',
 created_at DATETIME(6) NOT NULL, updated_at DATETIME(6) NOT NULL,
 FOREIGN KEY (tenant_id) REFERENCES tenants(id), FOREIGN KEY (credential_id) REFERENCES credentials(id),
 INDEX(tenant_id,created_at), INDEX(phase,created_at)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS attempts (
 id VARCHAR(80) PRIMARY KEY, request_id VARCHAR(64) NOT NULL, credential_id VARCHAR(64) NOT NULL,
 status VARCHAR(32) NOT NULL, reserved_micros BIGINT NOT NULL, cost_micros BIGINT NOT NULL DEFAULT 0,
 error_code VARCHAR(128) NOT NULL DEFAULT '',
 created_at DATETIME(6) NOT NULL, updated_at DATETIME(6) NOT NULL,
 FOREIGN KEY (request_id) REFERENCES requests(id), INDEX(request_id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS request_payloads (
 request_id VARCHAR(64) PRIMARY KEY, created_at DATETIME(6) NOT NULL, payload MEDIUMBLOB NOT NULL,
 FOREIGN KEY (request_id) REFERENCES requests(id) ON DELETE CASCADE, INDEX(created_at)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS ledger_entries (
 id VARCHAR(64) PRIMARY KEY, tenant_id VARCHAR(64) NOT NULL, kind VARCHAR(32) NOT NULL, amount_micros BIGINT NOT NULL,
 reference VARCHAR(191) NOT NULL, reason VARCHAR(512) NOT NULL, created_at DATETIME(6) NOT NULL,
 FOREIGN KEY (tenant_id) REFERENCES tenants(id), UNIQUE KEY ledger_business_key(tenant_id,reference), INDEX(tenant_id,created_at)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS resources (
 id VARCHAR(255) NOT NULL, tenant_id VARCHAR(64) NOT NULL, project_id VARCHAR(64) NOT NULL,
 credential_id VARCHAR(64) NOT NULL, endpoint_id VARCHAR(64) NOT NULL, model VARCHAR(200) NOT NULL, input_tokens BIGINT NOT NULL, created_at DATETIME(6) NOT NULL,
 PRIMARY KEY(tenant_id,project_id,id), FOREIGN KEY (tenant_id) REFERENCES tenants(id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS topup_orders (
 id VARCHAR(64) PRIMARY KEY, tenant_id VARCHAR(64) NOT NULL, amount_micros BIGINT NOT NULL,
 currency VARCHAR(8) NOT NULL DEFAULT 'usd', status VARCHAR(32) NOT NULL, session_id VARCHAR(255) NULL UNIQUE,
 payment_id VARCHAR(255) NULL UNIQUE, created_at DATETIME(6) NOT NULL, updated_at DATETIME(6) NOT NULL,
 FOREIGN KEY (tenant_id) REFERENCES tenants(id), INDEX(tenant_id,created_at)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS payment_events (
 id VARCHAR(255) PRIMARY KEY, event_type VARCHAR(128) NOT NULL, payload JSON NOT NULL,
 status VARCHAR(32) NOT NULL DEFAULT 'pending', last_error TEXT NULL, attempts INT NOT NULL DEFAULT 0,
 created_at DATETIME(6) NOT NULL, updated_at DATETIME(6) NOT NULL, INDEX(status,created_at)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS waitlist_entries (
 id VARCHAR(64) PRIMARY KEY, email VARCHAR(254) NOT NULL UNIQUE, name VARCHAR(200) NOT NULL DEFAULT '', created_at DATETIME(6) NOT NULL,
 INDEX(created_at)
) ENGINE=InnoDB;
