# RelayPine

`relaypine.com` 的 Claude / OpenAI API 中转平台：原生协议、多上游与多 Key 调度、独立 SOCKS5 出口，以及用量和预算管理。

当前采用 **超级管理员 + waitlist** 访问模式。唯一超级管理员为 `ufcenterxyz@gmail.com`；其他访客只提交候补申请，不会获得账号、会话或 API Key。Stripe 充值、退款、支付回调与支付任务已移除。线上预览入口为 [relaypine.com](https://relaypine.com)，真实模型和上游密钥需由管理员配置。

## 线上发布

GitHub Actions 在推送 `dev-tag/v*` 标签时运行检查，构建 Web 和 Gateway 镜像，部署至新加坡 TKE 的 `relaypine-dev` 命名空间。该环境运行生产配置，使用独立 MySQL 数据库和持久化 Redis；不要将命名空间中的数据视为可随时丢弃的测试数据。

邮箱登录入口为 [relaypine.com/?auth=login](https://relaypine.com/?auth=login)。在线 API 的 base URL 分别为 `https://relaypine.com/openai/v1` 和 `https://relaypine.com/anthropic`。部署、TLS、密钥备份与回滚约束见 [k8s/README.md](k8s/README.md)。

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
3. **Credentials**：选择平台/上游并粘贴 API Key，点击验证即可读取可用模型和有权限获取的工作区、限额，再保存。名称、共享额度组和容量都有默认值；出口代理、预算及手动覆盖位于高级设置。Anthropic 未绑定工作区的 identity key 可自动发现工作区；存在多个工作区时需要选择一个。
4. **Models & rates**：通过 **Import from upstream** 选择已保存的上游 Key 和模型，自动填入协议、模型 ID、名称和输出上限；补齐用量与成本费率后启用。也可手动添加。没有预置真实模型价格；导入不会覆盖已有模型。
5. **Routing**：选择最低负载、最高剩余预算或均衡策略，先过滤权限、容量、预算和出口不满足条件的 Key。
6. 管理员创建访问 Key；通过带理由的内部额度调整配置可用余额后，使用原生 SDK 进行验证。

预算是**本网关记录的本地上限**，不代表供应商实时余额。上游预算为 `0` 表示不设置本地总成本上限；它仍受速率和并发限制。平台不自动探测供应商余额。

## API 入口

| 协议 | 本地 base URL | 支持接口 |
| --- | --- | --- |
| OpenAI | `http://127.0.0.1:4811/openai/v1` | Chat Completions、Responses 创建/查询/删除、Models |
| Anthropic | `http://127.0.0.1:4811/anthropic` | Messages、Count tokens、Models |

使用 `Authorization: Bearer rp_live_...` 或 Anthropic 的 `x-api-key`。上游秘密只存于服务端。保留 JSON / SSE 原生字段，不做 Claude 与 OpenAI 跨协议转换。当前支持文本及客户端执行的函数工具；图片、音频、文件、托管工具、后台生成等无法可靠计量的能力被拒绝。Responses 续接按租户和项目校验并固定原上游凭据；发送后不自动重放生成。

Anthropic 的工作区头由选中的上游 Key 配置决定，调用者不能覆盖。`anthropic-beta` 支持经过核对的单模型能力，未知 beta 和 `fallbacks` 会在转发及额度预留前明确拒绝。完整支持列表、后台设置及 SDK 示例见 [Anthropic 接入说明](docs/anthropic-compatibility.md)。

## 上游自动发现

验证只调用模型和配置查询接口，不发起付费生成。OpenAI 与 Anthropic 均支持读取模型列表；官方 Anthropic 在 Key 权限允许时，还会读取工作区和模型组的配置限额。第三方 host 使用兼容的模型列表接口，不尝试官方组织管理接口；不支持发现的服务仍可在高级设置中手动保存。

限额显示为“上游导入”或“本地默认”。当前调度对一把 Key 使用统一的 RPM/TPM，因此导入时采用可用模型组、组织及工作区限额的保守最小值，输入和输出 token 上限也按更低者约束组合 token。没有可靠限额时使用 60 RPM、100,000 TPM、并发 10 的本地保护值，这不是上游能力保证。未知归属的 Key 默认共享同一 endpoint 的额度组；已识别组织的 Key 使用组织共享组，可在高级设置调整。

发现结果不会自动发布模型、猜测销售价格或余额，也不会把模型列表接口的限速误当作推理限额。模型可见不代表余额充足或所有调用能力都可用。OpenAI 完整项目限额接口需要 Admin Key；本流程不要求保存额外的管理员凭据。

验证 Key 后还需在 **Models & rates** 导入并发布模型。客户端 `/v1/models` 只列出本地已启用、协议匹配且权限允许的模型；本地目录没有模型时，两条协议路由都会返回空列表。导入遇到新识别的 Anthropic 工作区时，须明确保存到该上游凭据，再继续配置模型。销售和成本的输入/输出费率必须全部为正数才能启用；零费率可以作为禁用草稿保存。

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

开放真实模型调用前还需验证实际邮件投递、真实上游与代理、备份恢复和监控。验证邮件用于确认邮箱所有权，不代表获准使用服务；将来开放普通用户需另行实现明确的准入流程。
