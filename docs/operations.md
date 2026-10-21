# RelayPine v0.1 运维说明

## 环境与进程

本地端口：Next.js 4810，Go 4811，MySQL 4812，Redis 4813。Compose 数据卷属于 `relaypine` 项目。`docker compose stop` 可停止依赖；不要使用 `down -v`，除非明确要删除数据。

Go 网关同时运行恢复及支付任务；多副本依靠数据库行锁与幂等保护重复事件。每 15 秒处理支付收件箱及请求恢复，每约 60 秒核对待处理订单。API 暂无配置缓存；部署先采用单个 Redis 实例，Lua 跨多个额度键的脚本不能直接用于 Redis Cluster。

生产必须设置 `APP_ENV=production`、`APP_URL=https://relaypine.com`、关闭 `ALLOW_PRIVATE_UPSTREAMS`。网关可设 `GATEWAY_ADDR=:8080`；反向代理将 `/api/*`、`/openai/*`、`/anthropic/*` 指向 Go，其余指向 Next.js。SSE 禁止代理缓冲，并设置足够读超时；Next.js rewrites 仅用于控制台 `/api`，原生流量直接到 Go。

本版网关单次请求最长 5 分钟；并发租约 7 分钟，客户端写入有超时。失败后不自动再次提交生成，避免双重上游成本。

## Stripe 联调

先用 Stripe 测试商户密钥，保持 Webhook 与 API key 同为 test mode。在线充值只有 secret key 和 webhook secret 都有值时启用。

```sh
stripe listen --forward-to http://localhost:4811/api/payments/stripe/webhook
```

将监听器提供的 signing secret 安全写入 `.env`，重启网关，从真实客户控制台创建 Checkout 并在测试支付页完成付款。单纯 `stripe trigger` 生成的随机订单不匹配本地订单，不会入账。

订阅事件：

- `checkout.session.completed`
- `checkout.session.async_payment_succeeded`
- `refund.created`、`refund.updated`、`refund.failed`
- `charge.refunded`
- `charge.dispute.created`、`charge.dispute.closed`

Webhook 校验原始请求体 HMAC、5 分钟时间窗和模式；先落 `payment_events` 再返回 200。重复事件可以安全重送；入账前从 Stripe 读取当前 Checkout 和 PaymentIntent / Charge。不能凭成功回跳 URL 或客户端报文加钱。

退款只支持完全未使用订单的全额退款。退款请求前锁定钱包；网络超时保留冻结并按相同幂等键重试。平台外退款及争议冻结受影响客户进入人工核对；争议关闭不会自动恢复账户。已有消费、部分退款、奖金归属、手续费与税务单据需要后续账务功能。

## 需要监控的状态

- `payment_events.status='pending'`、`attempts`、`last_error`：应报警并核查，不能删除支付事件来“解决”重复充值。
- `topup_orders`：`pending`、`refund_pending`、`review_required`、`refund_failed` 需要核对 Stripe 当前状态。
- `requests`：未知用量与 `cost_pending` 需要供应商账单证据；不能当作零成本。
- `wallets.held_micros` 与 `credentials.held_micros`：客户和供应商预留分别管理。

后台不是完整财务管理系统。人工状态修复必须核对订单、钱包、账本、上游证据，并留下审计记录。正式上线前应实现专门的修复界面、报警和周期性对账任务。

## 密钥与备份

上游 Key / SOCKS5 凭据用 AES-GCM 加密；客户 API Key 与会话 token 仅存哈希，密码用 bcrypt。代理和 API Key 列表只返回掩码。不要将 `.env`、截图中的完整访问 Key、数据库备份放进 Git。

备份 MySQL 与 MASTER_KEY，并验证恢复；单独备份任一项不足以恢复上游凭据。当前未实现密钥轮换迁移工具。生产应关闭已完成初始化的 BOOTSTRAP 环境变量，并通过基础设施 Secret 注入。

生产认证仍需邮箱验证、密码找回和更完善的滥用控制。原型的 IP 限流取网关连接地址；反向代理部署前需要明确受信代理与真实客户端 IP 的处理方式，不能无条件信任客户端传入的 X-Forwarded-For。

## 实现参考

- [Stripe Webhook](https://docs.stripe.com/webhooks)
- [Stripe PaymentIntent 查询](https://docs.stripe.com/api/payment_intents/retrieve)
- [Stripe 退款](https://docs.stripe.com/refunds)
- [OpenAI API](https://platform.openai.com/docs/api-reference)
- [Anthropic API](https://platform.claude.com/docs/en/api/overview)
