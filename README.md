# RelayPine

`relaypine.com` 的商业 LLM API 中转平台。首版提供 OpenAI / Claude 原生协议、多上游与多 Key 调度、独立 SOCKS5 出口、客户钱包、按量扣费和 Stripe 充值。

当前状态：可运行的本地 v0.1，含客户控制台、运营后台、Go 网关及 MySQL / Redis。尚未部署到公网，也未配置真实上游或 Stripe 商户密钥。

## 本地启动

需要 Go 1.26、Node.js 20.9+、Yarn 1、Docker Compose。

```sh
make setup     # 生成随机本地凭据并安装依赖；保留已有 .env
make infra     # 启动 MySQL / Redis
make gateway   # 终端一
make console   # 终端二
```

- 控制台：http://localhost:4810
- 网关：http://127.0.0.1:4811；`/healthz`、`/readyz`
- 首个运营账号：读取根目录 `.env` 的 `BOOTSTRAP_EMAIL` / `BOOTSTRAP_PASSWORD`。首次启动自动创建，无默认客户赠金。
- 注册客户从零余额开始；运营可带理由入账，或配置 Stripe 后由客户充值。
- `.env`、供应商 Key、支付密钥禁止提交。`MASTER_KEY` 用于加密上游 Key 和代理凭据，必须独立备份，不可随意重生成。

## 首次配置

1. 运营后台 **Upstreams**：默认官方 OpenAI / Anthropic 地址，可添加兼容 host；host 支持是否带 `/v1`。
2. **Proxies**：按需创建 `socks5://user:password@host:port`；每把上游 Key 可以选择独立出口。代理不可用时请求失败，绝不自动直连。
3. **Upstreams → Credentials**：填写 Key、共享额度组、RPM、TPM、并发上限和总预算。相同供应商组织 / 项目共享限流时，使用同一额度组。组内限制取启用 Key 的保守值。
4. **Models**：配置对客户可见的模型别名、上游模型名、销售价、采购价、缓存价和输出上限。首版没有预置价格，避免模型过期或成本误配。
5. **Routing**：选择最低负载、最高剩余预算或均衡策略。先满足权限、容量、额度与出口条件，再排序。
6. 客户创建访问 Key，充值或由运营带理由入账后，使用原生 SDK 请求。

上游预算是**本地配置的总成本上限**，剩余量为预算减去已花费和预留。`0` 表示显式不限预算，并不代表供应商实际余额；尚未接供应商余额自动同步。单个模型当前共用一套采购价，不支持不同 Endpoint 的差异采购价。

## API 入口

| 协议 | 本地 base URL | 支持接口 |
| --- | --- | --- |
| OpenAI | `http://127.0.0.1:4811/openai/v1` | `chat/completions`、`responses` 创建/查询/删除、`models` |
| Anthropic | `http://127.0.0.1:4811/anthropic` | `v1/messages`、`v1/messages/count_tokens`、`v1/models` |

客户使用 `Authorization: Bearer rp_live_...` 或 Anthropic 的 `x-api-key`。上游 Key 不传给客户。公开部署后相应入口为 `https://relaypine.com/openai/v1` 和 `https://relaypine.com/anthropic`。

保留 JSON / SSE 原生字段；不做 Claude 与 OpenAI 之间的协议转换。当前支持文本和客户端执行的函数工具，拒绝图片、音频、文件、供应商托管工具、后台生成、会话引用及额外收费档位，防止未计量成本。Responses 的 `previous_response_id` 按租户和项目校验，并固定原上游凭据。开始发送请求后不自动重放生成。

## 计费与支付

金额使用整数 micro-USD；价格为每百万 token 的 micro-USD。请求前保守预留客户余额及上游成本预算，收到最终用量后按请求时价格快照结算。缓存读/写独立计价，客户收费不超过该请求预留。上游或平台失败时不向客户收费；客户端取消且最终用量已确认时仍按实际用量收费。未知用量客户预留最多保留 24 小时，上游未知成本继续待核实。原子 MySQL 事务防并发透支，Redis 原子脚本控制租户、访问 Key、上游 Key 及额度组容量。

Stripe 采用一次性 Checkout 充值，USD、每次 $5–$1,000。支付通知先验签再持久化，由后台处理；按事件和 PaymentIntent 双层幂等，订单与钱包同事务入账。没有支付密钥时，界面明确显示在线充值不可用。

填写 `.env` 的 `STRIPE_SECRET_KEY`、`STRIPE_WEBHOOK_SECRET`，重启网关。Webhook 地址为 `/api/payments/stripe/webhook`。事件及联调说明见 [运维说明](docs/operations.md)。运营可发起**完整未使用充值**的退款；部分退款、争议裁决和已消费充值的退款需人工核对，不能当作已实现的自动退款功能。

## 验证

```sh
make check
make test
make build
# 真实 MySQL / Redis 集成测试（仅自己的开发或专用测试库）
make test-integration
```

未传测试数据库 / Redis 时，相关集成测试会明确跳过。CI 会启动临时 MySQL / Redis 执行；尚未创建远程仓库或运行远程 CI。

## 代码结构与后续范围

- `apps/console`：Next.js 16、React 19、HeroUI、Tailwind 4；响应式、深浅色，英文界面及对应中文词典。
- `services/gateway`：Go 原生转发、运营 API、支付 worker、加密存储及计费账本。
- `contracts/http.md`：控制台和网关接口契约。
- [架构规划](docs/architecture.md)、[商业计费规划](docs/commercial-billing.md)、[开发路线](docs/implementation-plan.md)：完整目标范围，超出本 README 的条目仍为规划。

上线前还需完成真实模型/代理与 Stripe test mode 联调、商户配置、域名 TLS 与部署、数据库备份/恢复、监控告警、邮箱验证与密码重置、团队角色、审计/对账运营流程及服务条款。当前自注册仅单成员工作区，控制台显示最近记录；不是完整的财务总账，也没有供应商余额探测和自动成本对账。
