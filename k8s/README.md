# RelayPine hosted deployment

These manifests describe the maintainer's deployment, not a portable installer.
For a self-hosted deployment, replace the namespace, image registry, ingress
domain, administrator email, Mailgun domain, trusted proxy CIDRs, storage class,
and certificate issuer with your own values. Provision your own database and
secrets. The release workflow is guarded to run only in
`ufcenterxyz/relaypine.com`; forks must configure their own release target.

The hosted service uses namespace `relaypine-dev`, customer origin `https://relaypine.com`, and administration origin `https://admin.relaypine.com`.
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
- `relaypine-secrets`: `MYSQL_DSN`, `MASTER_KEY`, `REDIS_PASSWORD`, `MAILGUN_API_KEY`, and, once
  request bodies move to COS, `OBJECT_STORAGE_ACCESS_KEY_ID` and `OBJECT_STORAGE_SECRET_ACCESS_KEY`.
  The Redis password must be a generated URL-safe token without whitespace. App configuration
  belongs in `relaypine-config`, not the secret. Preserve `MASTER_KEY` across
  releases so stored upstream credentials remain decryptable.
- `relaypine-tls` and `relaypine-admin-tls`: created and renewed by cert-manager using the existing
  `letsencrypt-prod-http01` ClusterIssuer.

Only the gateway receives all application secrets. Redis receives its own
password field; the web container receives no secrets. Both web and gateway receive
the public `APP_URL` and `ADMIN_APP_URL` origins. A secret or ConfigMap
change requires restarting the gateway to load the new environment.

The gateway requires a valid `SUPERADMIN_EMAIL` in the ConfigMap and fails closed
without it. The hosted deployment retains its existing administrator explicitly.
Changing this setting switches the allowed identity; it does not transfer the old
administrator's tenant or wallet to the new email.

The gateway applies additive MySQL schema migrations during startup. It creates
the super administrator account configured by `SUPERADMIN_EMAIL`. Other verified
mailboxes receive isolated customer workspaces with zero initial balance. No password
or Stripe payment service is used.

## Request bodies

The gateway keeps encrypted request and response bodies for request details
indefinitely while `REQUEST_BODY_RETENTION_DAYS` is unset. Releases before
dev-tag/v0.1.13 delete bodies older than 7 days by default, and delete every
body when it is `0`; before rolling the gateway back to one of them, set
`REQUEST_BODY_RETENTION_DAYS: "3650"`. Until object storage is configured,
bodies are stored in the CDB database. To move them to COS as volume
grows:

1. Create a private bucket in `ap-singapore`, the cluster's region, named
   `<name>-<AppId>`. Keep the default virtual-hosted addressing; COS buckets
   created since 2024 do not accept path-style requests.
2. Add a lifecycle rule for prefix `request-bodies/`, objects larger than 64 KB,
   that moves them to `STANDARD_IA` after 30 days:

   ```xml
   <LifecycleConfiguration>
     <Rule>
       <ID>request-bodies-infrequent-access</ID>
       <Filter><And><Prefix>request-bodies/</Prefix><ObjectSizeGreaterThan>65536</ObjectSizeGreaterThan></And></Filter>
       <Status>Enabled</Status>
       <Transition><Days>30</Days><StorageClass>STANDARD_IA</StorageClass></Transition>
     </Rule>
   </LifecycleConfiguration>
   ```

   Smaller objects stay in standard storage because COS bills infrequent-access
   objects as at least 64 KB. Do not add archive transitions: archived bodies
   cannot be viewed until restored.
3. Create a CAM sub-user allowed only `cos:PutObject` and `cos:GetObject`, plus
   `cos:DeleteObject` if a retention period is set, on
   `qcs::cos:ap-singapore:uid/<AppId>:<bucket>/request-bodies/*`.
4. Add `OBJECT_STORAGE_ENDPOINT: https://cos-internal.ap-singapore.tencentcos.cn`,
   `OBJECT_STORAGE_REGION: ap-singapore` and `OBJECT_STORAGE_BUCKET` to
   `relaypine-config`, and the sub-user's SecretId and SecretKey as
   `OBJECT_STORAGE_ACCESS_KEY_ID` and `OBJECT_STORAGE_SECRET_ACCESS_KEY` to
   `relaypine-secrets`, then restart the gateway. The internal endpoint resolves
   only inside Tencent Cloud and avoids public traffic charges.

The gateway logs `Request bodies are stored in object storage` at startup and
moves bodies already in CDB to the bucket in the background. The bucket is not
part of CDB backups; enable versioning if deleted bodies must be recoverable.

## Routing and transport

Ingress sends `/api`, `/openai`, `/anthropic`, `/healthz`, and `/readyz` to the Go
gateway on port 4811. The separate admin ingress sends `/api` and health probes
to Go; it does not expose the native relay routes. Other paths reach Next.js on port 3000.
The gateway checks the actual Host against `APP_URL` and `ADMIN_APP_URL`; the ingress
must preserve it. Mutation Origin must match that same host. Cookies and OTP/session
scopes are separated per host; the public origin never receives operator data. Requests are limited
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
kubectl get certificate relaypine-tls relaypine-admin-tls -n relaypine-dev
curl --fail --silent --show-error https://relaypine.com/readyz
```

`/readyz` verifies MySQL and Redis connectivity. Verify Mailgun domain state and a
deliberate end-to-end login separately; health probes do not send email. Retain a
recent CDB backup before schema changes and verify that Redis PVC is `Bound`.

To roll back application code, render the previous known-good image SHA for both
deployments and apply that release. Rolling back images does not roll back schema
or customer data. Do not delete the namespace or PVC during a release.
