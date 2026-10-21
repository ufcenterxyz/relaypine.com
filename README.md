# RelayPine

`relaypine.com` 的 Claude / OpenAI API 中转平台：原生协议、多上游与多 Key 调度、独立 SOCKS5 出口，以及用量和预算管理。

当前采用 **超级管理员 + waitlist** 访问模式。唯一超级管理员为 `ufcenterxyz@gmail.com`；其他访客只提交候补申请，不会获得账号、会话或 API Key。Stripe 充值、退款、支付回调与支付任务已移除。项目目前运行于本地，尚未部署公网或配置真实模型上游。

## 本地启动

需要 Go 1.26、Node.js 20.9+、Yarn 1、Docker Compose。

```sh
make setup
make infra
make gateway  # 终端一
make console  # 终端二
```

- 控制台：[localhost:4810](http://localhost:4810)
- 网关：`http://127.0.0.1:4811`，提供 `/healthz`、`/readyz`。
- 登录使用邮箱收到的 6 位验证码，不需要密码。验证 `ufcenterxyz@gmail.com` 后进入管理控制台；其他邮箱验证成功后加入 waitlist，不获得登录会话。
- 配置 `.env` 中的 `MAILGUN_API_KEY`、`MAILGUN_DOMAIN`、`MAILGUN_FROM`，使用已验证的发信域名。`MAILGUN_API_BASE` 默认为美国区域，欧盟区域使用 `https://api.eu.mailgun.net`。未配置时发送接口返回邮件不可用，不会在页面或日志中显示验证码。
- 初始化脚本仅生成数据库和加密凭据；`BOOTSTRAP_EMAIL` 不决定管理员身份，`BOOTSTRAP_PASSWORD` 已停用。
- `.env`、上游 Key、代理凭据和 `MASTER_KEY` 禁止提交到 Git。`MASTER_KEY` 用于加密上游秘密，必须独立备份。

## 候补与权限

官网 **Join waitlist** 收集姓名和邮箱，完成邮件验证码验证后保存候补记录。服务端规范化邮箱并去重；候补提交不创建用户、工作区、钱包、会话或访问 Key。后台 **Waitlist** 展示真实申请记录，升级导入的历史记录保留。

旧密码登录、注册及直接提交候补入口已停用，不能绕过邮箱验证。普通账号即使历史上有 operator 标记、会话或 API Key，也无法进入当前管理控制台或调用中转。升级时保留历史业务记录，将非管理员用户加入候补名单，并撤销其管理身份。候补名单当前不提供自助开通或审批放行功能。

管理员身份需同时满足固定邮箱和服务端管理标记，并通过一次性验证码验证。旧密码会话失效；原有同邮箱普通账号提升时撤销旧会话与访问 Key。验证码有效期为 10 分钟，60 秒后可重发，最多尝试 5 次，成功后立即消费；Redis 只存带密钥的摘要，不保存明文验证码。

## 首次配置

1. **Upstreams**：默认官方 OpenAI / Anthropic host，也可添加兼容 host；支持是否带 `/v1`。
2. **Egress proxies**：按需添加 `socks5://user:password@host:port`，每把上游 Key 可绑定固定出口。代理不可用不会自动直连。
3. **Credentials**：配置上游 Key、共享额度组、RPM、TPM、并发上限和预算。同组织/项目共享限额的 Key 使用同一额度组。
4. **Models & pricing**：配置模型别名、上游模型名、用量费率、成本费率和最大输出量。没有预置真实模型价格。
5. **Routing**：选择最低负载、最高剩余预算或均衡策略，先过滤权限、容量、预算和出口不满足条件的 Key。
6. 管理员创建访问 Key；通过带理由的内部额度调整配置可用余额后，使用原生 SDK 进行验证。

预算是**本网关记录的本地上限**，不代表供应商实时余额。上游预算为 `0` 表示不设置本地总成本上限；它仍受速率和并发限制。平台不自动探测供应商余额。

## API 入口

| 协议 | 本地 base URL | 支持接口 |
| --- | --- | --- |
| OpenAI | `http://127.0.0.1:4811/openai/v1` | Chat Completions、Responses 创建/查询/删除、Models |
| Anthropic | `http://127.0.0.1:4811/anthropic` | Messages、Count tokens、Models |

使用 `Authorization: Bearer rp_live_...` 或 Anthropic 的 `x-api-key`。上游秘密只存于服务端。保留 JSON / SSE 原生字段，不做 Claude 与 OpenAI 跨协议转换。当前支持文本及客户端执行的函数工具；图片、音频、文件、托管工具、后台生成等无法可靠计量的能力被拒绝。Responses 续接按租户和项目校验并固定原上游凭据；发送后不自动重放生成。

## 用量与额度

保留整数 micro-USD 账本、模型价格快照、请求前预留与请求后结算，以及独立的上游成本预算。内部额度调整要求原因并记录操作者。该机制用于预算和使用控制，不会发起外部扣款。

没有在线充值、支付订单操作、退款、支付 webhook 或支付 worker。历史支付表和历史资金记录保留以避免破坏已有数据，当前服务不再写入或处理外部付款。详见[用量与额度说明](docs/commercial-billing.md)。

## 验证

```sh
make check
make test
make build
make test-integration
```

本地集成测试默认创建独立 `relaypine_test` 数据库，不使用应用库执行访问策略迁移测试。也可显式提供 `MYSQL_TEST_DSN` 和 `REDIS_TEST_ADDR` 指向专用测试服务。测试按包顺序执行，避免固定管理员身份跨测试包冲突；未配置测试服务时，直接运行 Go 测试会明确跳过相关集成用例。

## 代码结构

- `apps/console`：Next.js、HeroUI、候补表单、管理控制台、代码实现的交互路由演示。
- `services/gateway`：Go 转发、访问控制、候补名单、加密存储、限流和用量结算。
- `contracts/http.md`：接口契约。
- [运维说明](docs/operations.md)、[架构说明](docs/architecture.md)、[开发阶段](docs/implementation-plan.md)。

公网开放前还需验证邮件实际投递、真实上游与代理、TLS、备份恢复和监控。验证邮件用于确认邮箱所有权，不代表获准使用服务；将来开放普通用户需另行实现明确的准入流程。
