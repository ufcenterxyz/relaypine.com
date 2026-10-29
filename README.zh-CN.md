# RelayPine

[English](README.md) · 简体中文 · [官网](https://relaypine.com) · [GitHub](https://github.com/ufcenterxyz/relaypine.com)

开源、自部署的 Claude / OpenAI API 中转平台：原生协议、多上游与多 Key 调度、独立 SOCKS5 出口，以及用量和预算管理。

用户在 [relaypine.com](https://relaypine.com) 使用邮箱验证码登录，获得独立工作区、API Key 和初始为零的钱包。管理入口为 [admin.relaypine.com](https://admin.relaypine.com)，同样使用邮箱验证码，仅允许 `SUPERADMIN_EMAIL` 指定的管理员访问。Stripe 充值、退款、支付回调与支付任务已移除；管理员通过带理由的额度调整为工作区记入余额。

## 线上发布

仓库中的 `k8s/` 和发布工作流用于维护者的 relaypine.com 部署。自部署需替换域名、命名空间、镜像仓库、管理员邮箱及反向代理地址，并配置自己的秘密；fork 默认不会运行维护者的发布任务。

GitHub Actions 在推送 `dev-tag/v*` 标签时运行检查，构建 Web 和 Gateway 镜像，部署至新加坡 TKE 的 `relaypine-dev` 命名空间。该环境运行生产配置，使用独立 MySQL 数据库和持久化 Redis；不要将命名空间中的数据视为可随时丢弃的测试数据。

邮箱登录入口为 [relaypine.com/?auth=login](https://relaypine.com/?auth=login)。在线 API 的 base URL 分别为 `https://relaypine.com/openai/v1` 和 `https://relaypine.com/anthropic`。部署、TLS、密钥备份与回滚约束见 [k8s/README.md](k8s/README.md)。

## 本地启动

需要 Go 1.26、Node.js 22.23.4（在仓库根目录运行 `nvm install && nvm use`）、Yarn 1、Docker Compose。

```sh
SUPERADMIN_EMAIL=you@example.com make setup
# 在 .env 配置 MAILGUN_API_KEY、MAILGUN_DOMAIN、MAILGUN_FROM
make infra
make gateway  # 终端一
make console  # 终端二
```

- 用户控制台：[localhost:4810](http://localhost:4810)；管理控制台：[admin.localhost:4810](http://admin.localhost:4810)。
- 网关：`http://127.0.0.1:4811`，提供 `/healthz`、`/readyz`。
- 登录使用邮箱收到的 6 位验证码，不需要密码。普通邮箱验证后创建客户账号；管理域名只允许已验证的配置管理员。
- 配置 `.env` 中的 `MAILGUN_API_KEY`、`MAILGUN_DOMAIN`、`MAILGUN_FROM`，使用已验证的发信域名。`MAILGUN_API_BASE` 默认为美国区域，欧盟区域使用 `https://api.eu.mailgun.net`。未配置时发送接口返回邮件不可用，不会在页面或日志中显示验证码。
- 初始化脚本生成数据库和加密凭据，并保存你提供的管理员邮箱。缺少或无效的 `SUPERADMIN_EMAIL` 会阻止网关启动；`BOOTSTRAP_EMAIL` 与 `BOOTSTRAP_PASSWORD` 不再使用。已有安装可执行 `SUPERADMIN_EMAIL=you@example.com make setup`，只补齐缺失配置，不覆盖现有秘密。
- `.env`、上游 Key、代理凭据和 `MASTER_KEY` 禁止提交到 Git。`MASTER_KEY` 用于加密上游秘密，必须独立备份。

## 登录与权限

官网统一使用邮箱验证码登录。验证邮箱后按事务创建或恢复该用户的工作区、默认项目和钱包，不自动发放余额。历史候补记录本身不能用于登录或授予权限，必须重新完成邮箱验证。

主站和管理站的 Cookie、验证码和会话作用域分开。服务端按实际 Host 和对应 Origin 验证请求；主站不提供管理 API 或采购成本，即使管理员登录主站，也只获取自己的客户视图。普通用户只能读取和操作自己工作区的资源。旧密码登录入口仍已停用。

管理员身份需同时满足配置的邮箱和服务端管理标记，并通过一次性验证码验证。旧密码会话失效；原有同邮箱普通账号提升时撤销旧会话与访问 Key。验证码有效期为 10 分钟，60 秒后可重发，最多尝试 5 次，成功后立即消费；Redis 只存带密钥的摘要，不保存明文验证码。

## 首次配置

1. **Upstreams**：默认官方 OpenAI / Anthropic host，也可添加兼容 host；支持是否带 `/v1`。
2. **Egress proxies**：按需添加 `socks5://user:password@host:port`，每把上游 Key 可绑定固定出口。代理不可用不会自动直连。
3. **Credentials**：选择平台/上游并粘贴 API Key，点击验证即可读取可用模型和有权限获取的工作区、限额，再保存。名称、共享额度组和容量都有默认值；出口代理、预算及手动覆盖位于高级设置。Anthropic 未绑定工作区的 identity key 可自动发现工作区；存在多个工作区时需要选择一个。
4. **Models & rates**：通过 **Import from upstream** 多选模型并预览参考价，协议、ID、名称和输出上限自动填入。匹配官方标准价格时，售价和参考成本默认 1:1；未知型号及第三方 host 需明确配置价格后启用。已有模型跳过，不覆盖原配置。使用 **Update reference prices** 预览并更新既有模型的最新参考价。
5. **Routing**：选择最低负载、最高剩余预算或均衡策略，先过滤权限、容量、预算和出口不满足条件的 Key。
6. **备用通道（可选）**：在 Upstreams 添加 OpenRouter 或 ZenMux 的 Key。**Check key** 不发起付费推理即可确认 Key，并列出该服务商按官方模型名提供了哪些已启用模型。只有某个模型的所有官方通道都出错、Key 被拒绝、被限流或预算用完时才会使用备用通道，并按设置的顺序尝试。如需显示账户余额，请填写服务商的管理 Key（可选），普通推理 Key 无法读取余额；设置了额度上限的 OpenRouter Key 还会显示该 Key 剩余的额度。
7. 管理员为目标客户工作区记入带理由的额度调整。该客户创建访问 Key 后使用原生 SDK 调用；请求预留并扣减 RelayPine 余额，余额不足时不会发送到上游。

预算是**本网关记录的本地上限**，不代表供应商实时余额。上游预算为 `0` 表示不设置本地总成本上限；它仍受速率和并发限制。平台不自动探测供应商余额。

## API 入口

| 协议 | 本地 base URL | 支持接口 |
| --- | --- | --- |
| OpenAI | `http://127.0.0.1:4811/openai/v1` | Chat Completions、Responses 创建/查询/删除、Models |
| Anthropic | `http://127.0.0.1:4811/anthropic` | Messages、Count tokens、Models |

使用 `Authorization: Bearer rp_live_...` 或 Anthropic 的 `x-api-key`。上游秘密只存于服务端。保留 JSON / SSE 原生字段，不做 Claude 与 OpenAI 跨协议转换。支持文本及客户端执行的函数工具；Anthropic Messages 和 Count tokens 还支持 user content 中的内联 base64 PNG/JPEG，包括 user 消息内的 `tool_result.content`。OpenAI 图片输入、图片 URL、文件输入及引用、其他图片格式、音频、托管工具及后台生成仍不支持。Responses 续接按租户和项目校验并固定原上游凭据。只有上游在产生任何输出之前明确拒绝（请求没有发出，或返回 HTTP 401、402、403、429、5xx），请求才会换到下一条通道；每个请求最多尝试三条通道，官方通道优先。结果未知的请求（例如请求发出后连接中断）不会重放。Responses 和 Token 计数不会使用备用通道，因为其他服务商无法接续它们保存的状态。

已配置且支持视觉的模型可使用原生 SDK：

```ts
import Anthropic from "@anthropic-ai/sdk";
import { readFile } from "node:fs/promises";

const client = new Anthropic({
  apiKey: process.env.RELAYPINE_API_KEY,
  baseURL: "https://relaypine.com/anthropic",
});
await client.messages.create({
  model: "your-enabled-vision-model", // 替换为已启用且授权的模型别名
  max_tokens: 256,
  messages: [{ role: "user", content: [
    { type: "image", source: {
      type: "base64", media_type: "image/png",
      data: (await readFile("screenshot.png")).toString("base64"),
    } },
    { type: "text", text: "描述这张图片。" },
  ] }],
});
```

包含 base64、文本和 JSON 的**整个请求仍限 2 MiB**。本地同时限制每请求最多 20 张图、每轴最多 8,000 像素、单图最多 1,600 万像素、总计最多 3,200 万像素；只接受完整静态 PNG/JPEG，声明类型必须与文件一致。建议编码前裁剪或缩小尺寸；照片可用 JPEG 压缩，文字和图表通常适合 PNG。base64 比二进制约增加三分之一体积，还需为 JSON 和文本留出空间。网关不缩放或重新压缩图片，图片 payload 原样转发，不写入请求日志。

控制台的请求详情展示客户端发出的请求正文和客户端收到的响应，并按对话、工具调用、流式输出和内联图片格式化显示。正文加密存储，默认保留 7 天（`REQUEST_BODY_RETENTION_DAYS`；设为 `0` 停止记录并删除已存正文）。客户只能查看自己工作区的请求；管理员可查看所有工作区，并能看到失败请求中未返回给客户端的上游原始回复。

Anthropic 的工作区头由选中的上游 Key 配置决定，调用者不能覆盖。`anthropic-beta` 支持经过核对的单模型能力，未知 beta 和 `fallbacks` 会在转发及额度预留前明确拒绝。完整支持列表、后台设置及 SDK 示例见 [Anthropic 接入说明](docs/anthropic-compatibility.md)。

## 上游自动发现

验证只调用模型和配置查询接口，不发起付费生成。OpenAI 与 Anthropic 均支持读取模型列表；官方 Anthropic 在 Key 权限允许时，还会读取工作区和模型组的配置限额。第三方 host 使用兼容的模型列表接口，不尝试官方组织管理接口；不支持发现的服务仍可在高级设置中手动保存。

限额显示为“上游导入”或“本地默认”。当前调度对一把 Key 使用统一的 RPM/TPM，因此导入时采用可用模型组、组织及工作区限额的保守最小值，输入和输出 token 上限也按更低者约束组合 token。没有可靠限额时使用 60 RPM、100,000 TPM、并发 10 的本地保护值，这不是上游能力保证。未知归属的 Key 默认共享同一 endpoint 的额度组；已识别组织的 Key 使用组织共享组，可在高级设置调整。

发现结果不会自动发布模型或增加余额，参考定价在导入预览阶段独立获取，也不会把模型列表接口的限速误当作推理限额。模型可见不代表余额充足或所有调用能力都可用。OpenAI 完整项目限额接口需要 Admin Key；本流程不要求保存额外的管理员凭据。

验证 Key 后还需在 **Models & rates** 选中模型并批量导入。客户端 `/v1/models` 只列出本地已启用、协议匹配且权限允许的模型；本地目录没有模型时，两条协议路由都会返回空列表。导入遇到新识别的 Anthropic 工作区时，须明确保存到该上游凭据，再继续导入。整批先校验再创建，已有模型跳过；失败可重试，不会覆盖已有价格或修改访问权限。

## 用量与额度

启用的模型必须按费率计费。批量导入从官方公开价表获取标准文本参考价，显示来源和检查时间；模型列表 API 本身不返回这些单价。更新入口先展示旧价与新价，选中后按模型版本应用，防止覆盖并发编辑。参考价不是账号专属合同价格；在常规路线中，自动官方参考价只适用于对应官方上游（已配置的备用通道也可承接该模型）。

由备用通道完成的请求按模型的正常售价向客户计费；上游成本按模型的上游成本价估算记录，备用通道的消费上限在 OpenRouter 或 ZenMux 账户中设置。被拒绝的尝试没有上游成本，管理员的请求记录会列出每一条尝试过的通道。

历史“仅记录用量”请求保留原始快照，不追溯扣款；迁移模型只影响后续请求。没有可靠价格的模型保持禁用等待定价，不默认为免费。鉴权、RPM、TPM、并发、上游预算、输出上限和固定代理规则继续执行。启用前，销售和成本的输入/输出费率须全部为正数。

保留整数 micro-USD 账本、模型价格快照、请求前预留与请求后结算，以及独立的上游成本预算。图片请求在原文本/请求体保守估算中只扣除已验证图片的 base64 逻辑长度，再按每图 `2 * ceil(width / 28) * ceil(height / 28) + 256` 加入图片上界；同一上界用于上下文、TPM 和金额预留，最终按上游可信 usage 结算。这不会放宽 2 MiB 请求上限。显式 `count_tokens` 请求不收取 RelayPine 金额费用，但仍受权限和容量检查；网关不会在生成前自动额外调用它。

内部额度调整要求原因并记录操作者。该机制用于预算和使用控制，不会发起外部扣款。

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

- `apps/console`：Next.js、HeroUI、邮箱登录、用户与管理控制台、代码实现的交互路由演示。
- `services/gateway`：Go 转发、租户访问控制、参考价格、加密存储、限流和用量结算。
- `contracts/http.md`：接口契约。
- [运维说明](docs/operations.md)、[架构说明](docs/architecture.md)、[开发阶段](docs/implementation-plan.md)。

部署后验证实际邮件投递、真实上游与代理、备份恢复和监控。邮箱验证不赋予管理权限，也不会增加钱包余额。

## 开源与贡献

本项目采用 [MIT 许可证](LICENSE)。欢迎通过 [GitHub Issues](https://github.com/ufcenterxyz/relaypine.com/issues) 反馈问题，贡献前请阅读 [贡献指南](CONTRIBUTING.md)。漏洞请通过 [私密安全报告](https://github.com/ufcenterxyz/relaypine.com/security/advisories/new) 提交，详见 [安全政策](SECURITY.md)。

开源代码可自行部署，支持独立客户账号与单管理员后台；不包含团队角色或在线支付服务。
