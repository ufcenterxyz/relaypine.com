# RelayPine live preview

The live private preview uses namespace `relaypine-dev` and `https://relaypine.com`.
The namespace name does not imply disposable data. MySQL is a dedicated database
on managed CDB; Redis uses a 10 GiB CBS PVC with AOF persistence and authentication.
Deleting the PVC can delete the underlying disk because the `cbs` StorageClass has
the `Delete` reclaim policy.

## Bootstrap and releases

`namespace.yaml`, `bootstrap-ci.yaml`, and secrets are managed separately from
runtime releases. `kustomization.yaml` contains only application resources. The
release workflow renders both application images with the same immutable
`sha-<commit>` tag before applying the manifests. Do not apply the base deployment
images with the `placeholder` tag.

Required secrets in `relaypine-dev`:

- `tcr-secret`: registry pull credentials for `sgccr.ccs.tencentyun.com`.
- `relaypine-secrets`: `MYSQL_DSN`, `MASTER_KEY`, `REDIS_PASSWORD`, `MAILGUN_API_KEY`.
  The Redis password must be a generated URL-safe token without whitespace. App configuration
  belongs in `relaypine-config`, not the secret. Preserve `MASTER_KEY` across
  releases so stored upstream credentials remain decryptable.
- `relaypine-tls`: created and renewed by cert-manager using the existing
  `letsencrypt-prod-http01` ClusterIssuer.

Only the gateway receives all application secrets. Redis receives its own
password field; the web container receives no secrets. A secret or ConfigMap
change requires restarting the gateway to load the new environment.

The gateway applies additive MySQL schema migrations during startup. It creates
the fixed super administrator account and restricts all other accounts to the
waitlist. No password or Stripe payment service is used.

## Routing and transport

Ingress sends `/api`, `/openai`, `/anthropic`, `/healthz`, and `/readyz` to the Go
gateway on port 4811. Other paths reach Next.js on port 3000. Requests are limited
to 2 MiB at ingress to match the relay; console JSON endpoints enforce 1 MiB in
the application. Streaming buffering is disabled and proxy timeouts are 360
seconds, above the gateway's five-minute request deadline.

Cloudflare must use **Full (Strict)** with the valid origin certificate. DNS-only
mode may be used during the initial HTTP-01 certificate challenge. Never enable
Flexible TLS for an authenticated service.

Client IP forwarding is trusted only from the configured nginx controller peer.
When that pod's address changes, update `TRUSTED_PROXY_CIDRS` and restart the
gateway. An out-of-date peer address fails closed to the socket address and may
cause shared rate limits until updated. Cloudflare client headers are accepted
only after validating the forwarding hop against `CLOUDFLARE_PROXY_CIDRS`.

The optional NetworkPolicy file is deliberately excluded from releases because
enforcement has not been established for this cluster's TKE VPC CNI. Apply it only
after confirming support and testing ingress isolation. No namespace isolation
claim depends on merely creating a NetworkPolicy object.

## Verification

```bash
kubectl rollout status deployment/relaypine-redis -n relaypine-dev --timeout=5m
kubectl rollout status deployment/relaypine-gateway -n relaypine-dev --timeout=5m
kubectl rollout status deployment/relaypine-web -n relaypine-dev --timeout=5m
kubectl get certificate relaypine-tls -n relaypine-dev
curl --fail --silent --show-error https://relaypine.com/readyz
```

`/readyz` verifies MySQL and Redis connectivity. Verify Mailgun domain state and a
deliberate end-to-end login separately; health probes do not send email. Retain a
recent CDB backup before schema changes and verify that Redis PVC is `Bound`.

To roll back application code, render the previous known-good image SHA for both
deployments and apply that release. Rolling back images does not roll back schema
or customer data. Do not delete the namespace or PVC during a release.
