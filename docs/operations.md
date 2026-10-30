# RelayPine 运维说明

## 环境与进程

本地端口：Next.js 4810、Go 4811、MySQL 4812、Redis 4813。`yarn build` 准备 standalone 静态资源，`HOSTNAME=127.0.0.1 PORT=4810 yarn start` 启动本地生产预览。用户入口为 `localhost:4810`，管理入口为 `admin.localhost:4810`。Next.js 的服务端 `/api` 代理在运行时读取 `GATEWAY_URL`，并按 `APP_URL` / `ADMIN_APP_URL` 验证及保留 Host；修改后重启 Web 进程。初始化脚本会将这三个非秘密配置补入 `apps/console/.env.local`，自定义域名时确保 Web 与网关配置一致。

Compose 数据卷属于 `relaypine`。停止依赖可用 `docker compose stop`；不要用 `down -v` 删除业务数据。

请求详情需要的请求与响应正文默认长期保存。每个请求最多保存 2 MiB 请求和 4 MiB 响应，gzip 压缩后用 `MASTER_KEY` 加密并绑定请求 ID；更换 `MASTER_KEY` 后旧正文无法读取。`REQUEST_BODY_RETENTION_DAYS` 留空（默认）时一直保留，设为 1–3650 则到期后分批删除；旧版本把 `0` 当作停止记录并删除全部正文，现在设为 `0` 会拒绝启动。`REQUEST_BODY_CAPTURE=false` 停止记录新正文，已存正文不受影响。所有副本必须使用相同的正文配置。

正文默认存于 MySQL 的 `request_payloads` 表，适合本地和小流量部署，数据库备份也包含这些密文。流量变大后配置 S3 兼容对象存储（腾讯云 COS、Amazon S3、Cloudflare R2、MinIO）：`OBJECT_STORAGE_ENDPOINT`、`OBJECT_STORAGE_REGION`、`OBJECT_STORAGE_BUCKET`、`OBJECT_STORAGE_ACCESS_KEY_ID`、`OBJECT_STORAGE_SECRET_ACCESS_KEY` 需同时设置，缺一项网关拒绝启动；MinIO 等需要路径寻址的服务加 `OBJECT_STORAGE_PATH_STYLE=true`，COS 保持默认的虚拟主机寻址。此后 MySQL 只保留索引行，正文写入 `request-bodies/<年>/<月>/<日>/<请求 ID>`，内容在网关内加密，存储服务看不到明文。正文在响应结束后于后台上传，不增加请求延迟；上传失败、待上传积压超过 32 MiB 或网关正在停止时，正文先写入 MySQL，之后自动迁移到对象存储；启用前已存在 MySQL 的正文也会自动迁移。网关被强制终止时，尚未上传完的正文可能丢失，通常不到一秒的流量。存储桶不在数据库备份范围内，按需开启版本控制或跨地域复制。

旧正文很少被查看，用存储桶生命周期规则降低成本：`request-bodies/` 前缀下大于 64 KB 的对象 30 天后转为低频存储（COS 与 S3 均为 `STANDARD_IA`）。低频存储可以直接读取；COS 低频存储不足 64 KB 的对象按 64 KB 计费，小对象转换反而更贵，所以规则按对象大小过滤。不要转入归档或深度归档：这类对象需要先恢复才能读取，控制台只会显示“正文已归档”。网关只需要该前缀的上传和读取权限，设置保留期时再加删除权限，不需要列举或管理存储桶。

Go 网关每 15 秒运行请求恢复；另一个后台任务每 15 秒删除超过保留期的正文，并把暂存在 MySQL 的正文迁移到对象存储，多副本时同一时间只有一个副本执行。Stripe 客户端、支付收件箱处理、支付对账及相关 HTTP 入口已移除，环境不再需要 `STRIPE_*` 变量。历史支付数据保留，不再自动处理。

生产要求 `APP_ENV=production`、不同主机名的 HTTPS `APP_URL` 和 `ADMIN_APP_URL`，并关闭 `ALLOW_PRIVATE_UPSTREAMS`。反向代理将 `/api/*`、`/openai/*`、`/anthropic/*` 指向 Go，其余指向 Next.js。流式接口禁用代理缓冲并设置足够超时；单请求最长 5 分钟；结果未知的请求不重放，只有上游在产生输出前明确拒绝时才换到下一条路线。

## 用户与管理入口

每个部署必须通过 `SUPERADMIN_EMAIL` 指定唯一管理员邮箱，通过邮件中的 6 位验证码登录。缺少或无效的邮箱会阻止网关启动。`BOOTSTRAP_NAME` 可设置显示名称；`BOOTSTRAP_EMAIL` 与 `BOOTSTRAP_PASSWORD` 不再使用。线上 relaypine.com 的管理员仍由其部署配置指定。

已有本地安装可运行 `SUPERADMIN_EMAIL=you@example.com make setup`，仅补齐缺失配置，不覆盖现有秘密。修改管理员邮箱是身份切换，不是转移账号：新邮箱需要重新验证，不继承原管理员的工作区或余额，旧管理员的会话和 API Key 会被撤销。切换前备份并确认新邮箱可以收信。

启动初始化确保配置的管理员存在，保留已通过邮箱验证的普通客户和工作区。撤销旧管理员的管理权限、会话及 Key；旧密码会话不能用于登录。变更角色不转移原工作区或账本。

邮箱字符串本身不授予权限。主站验证码验证成功后创建或恢复该邮箱的独立客户工作区，初始余额为零；独立管理域名只允许配置的管理员。Cookie 不设置跨域 Domain，验证码与会话绑定客户/管理作用域，不能互换。主站任何账号都只返回客户视图。历史候补资料必须重新完成邮箱验证。

升级前备份 MySQL。历史用户、钱包、请求、候补及支付记录不会被删除。旧非验证码会话始终拒绝；价格迁移只更新模型配置，不修改历史请求或账本。反向代理必须保留真实 Host，不可使用任意客户端提供的 X-Forwarded-Host 替代。

## 验证码邮件

通过部署 Secret 注入 `MAILGUN_API_KEY`、`MAILGUN_DOMAIN` 和 `MAILGUN_FROM`。发信域名必须在 Mailgun 验证完成，From 示例为 `RelayPine <login@mg.relaypine.com>`。默认 API 区域为 `https://api.mailgun.net`；欧盟域名使用 `MAILGUN_API_BASE=https://api.eu.mailgun.net`。具体发送参数参见 [Mailgun 官方接口说明](https://documentation.mailgun.com/docs/mailgun/api-reference/send/mailgun/messages/post-v3--domain-name--messages)。

未配置发信服务时网关仍可启动，但发送验证码返回 `503 email_unavailable`。应用不会把验证码写入日志、返回给浏览器或启用固定测试码。只有邮件服务接受发送后才开放该验证码的校验；发送失败不会发放会话。

验证码有效 10 分钟，每封最多尝试 5 次，重发间隔 60 秒；重发使旧码失效，成功验证以 Redis 原子操作消费。Redis 保存 HMAC 摘要及临时显示姓名，摘要密钥由 `MASTER_KEY` 派生；验证码不以明文持久化。邮箱和连接地址均限流，Redis 故障时拒绝验证。

上线需完成实际收件测试，并监测发信失败率、限流和邮件延迟。发信凭据应限制到需要的域名；不要将密钥或验证码加入监控日志。

## 用量与预算监控

- 启用的模型必须具备完整 metered 价格。批量导入与“更新参考价格”从官方标准价表匹配，管理员预览后应用；第三方 host 和未知型号需手工定价。历史 `usage_only` 请求保留原记录，不追溯扣款。
- 长上下文价格：升级到包含长上下文计价的版本后，在 Models & rates 点“更新参考价格”，全选并应用。OpenAI gpt-6.x、5.6、5.5、5.4（mini、nano 除外）和 Claude Haiku 5.5 会写入长 prompt 的价格，其余模型记录为全长度同价。应用前，这些模型仍拒绝输入上界超出标准价范围的请求（OpenAI 模型约 130 KB 以上的请求体）。手动定价的模型在编辑表单的“长上下文价格”中填写阈值和价格。
- `requests` 中未知用量、`cost_pending` 需供应商证据核对，不能当作零成本。
- `wallets.held_micros` 与 `credentials.held_micros` 分别表示调用额度和上游成本预留。
- 配置代理不可用时拒绝相应路线；不允许静默绕过代理。
- 内部额度调整需填写原因，保留操作者及唯一业务参考。
- 备用通道（OpenRouter / ZenMux）在 Upstreams 配置，使用 **Check key** 确认 Key 和模型覆盖范围；Key 检查不发起付费推理。备用通道只在官方路线全部不可用时承接 Chat Completions 和 Messages，消费上限需在服务商账户设置。管理员请求记录中带多次尝试的请求即发生过换路，可按尝试的 `error_code` 判断官方故障类型。被拒绝的 Key 会暂时冷却（401/402/403 五分钟，429 按 Retry-After，5xx 与未发出请求 30 秒），冷却期间请求直接走下一条路线。
- 图像生成：在 Models & rates 导入或添加上游模型 `gpt-image-2`、`gpt-image-2.5-sunburst`、`gpt-image-2.5-flare`（或其带日期的快照）。配置了启用的官方 OpenAI 上游时，“更新参考价格”会套用 OpenAI 价格页中的图像价格；手动添加时填写文本输入、图像输入和图像输出价格，文本输出填 0。图像请求只走官方 OpenAI 通道，不会改走备用通道。
- 限流：上游 Key 的默认限额（60 RPM、100,000 TPM、并发 10）只是本地保护值。Upstreams 页“限额”列会显示服务商在最近响应中报告的每分钟限额（按模型）；Key 处理过请求后即可按此填写。请按服务商的实际额度修改每把 Key 的 RPM、TPM 和并发，同组 Key 共用最低的限额。请求按贴近实际的 Token 估算准入，结算后改为实际用量；Anthropic Key 的 TPM 填 Anthropic 的输入限额（ITPM）：网关和 Anthropic 一样不计缓存读取和输出，输出限额由 Anthropic 执行；默认 100,000 TPM 下，同一时间大约只能执行一个不写输出上限的长上下文请求。客户端收到 `429 rate_limit_exceeded`（All eligible upstream capacity is in use）时，先检查 Key 的限额配置。
- Responses 图像工具：需要已启用且定价完整的 GPT Image 模型，客户的访问 Key 需允许使用该图像模型；工具默认使用 `gpt-image-2`，`max_tool_calls` 默认 1、最多 4。图像工具的用量在请求详情中显示为“图像提示词 Token”及图像输入/输出 Token。
- 备用通道余额：OpenRouter 直接用推理 API Key 读取账户余额（`GET /api/v1/credits`，2026-10-29 已用真实 Key 验证），无需管理 Key；只有 OpenRouter 拒绝时才需要 Management Key，它还能创建和删除 API Key，权限较大，不建议保存。ZenMux 余额需要 Management API Key。管理 Key 只用于读取余额，按服务商加密保存，不参与转发，也不会返回给浏览器；不需要时可在编辑弹框中删除。余额每分钟最多读取一次，点击刷新会重新读取。
- 备用通道兼容性实测：在 `services/gateway` 运行 `RELAYPINE_LIVE_OPENROUTER_KEY=... RELAYPINE_LIVE_ZENMUX_KEY=... go test -run TestLiveBackupCompatibility -v ./internal/relay`。测试让本地官方上游返回 529，经真实故障切换路径向服务商发送 Chat、流式 Chat、工具调用、长提示缓存、Messages、流式 Messages、带 beta 头的工具流式请求、提示缓存写入与读取以及扩展思考，并核对每次结算的 Token 与服务商返回的用量完全一致。总成本约两美分，未设置 Key 时自动跳过。本机代理若使用 fake-IP DNS（解析到 198.18.0.0/15），网关的私网保护会拒绝连接，本地检查 Key 需使用允许私网的开发配置；线上解析为公网地址，不受影响。

本地预算不等于供应商余额；管理员自己的调用仍受访问 Key、租户限流和预算约束。移除 Stripe 不等于将钱包设为无限。

## 既有模型价格迁移

日常更新使用管理站 **Models & rates → Update reference prices**。批量运维也可在网关的环境中运行 `cmd/pricing`：`-out /private/path/preview.json -scope all` 只生成私有预览，包含原模型配置、来源和过期时间；检查后用 `-apply /private/path/preview.json -scope all` 应用。默认 scope 为 `usage-only`，只处理旧未计价模型；`-ids` 可进一步限定型号。预览 15 分钟过期，来源失败时不生成可应用结果，模型版本变化则整批拒绝。

迁移保留已有停用状态和模型名称/输出上限，补齐可验证价格，不改变客户余额、历史请求或账本。不匹配型号只能禁用等待手工定价。预览文件应私有保存，不提交 Git；执行前后核对模型版本与钱包金额。

## 备份与秘密

上游 Key / SOCKS5 凭据使用 AES-GCM，访问 Key 和会话只存哈希，验证码使用 HMAC 摘要。历史密码字段不再用于认证。备份 MySQL 和 `MASTER_KEY` 并验证恢复；不要随意重新生成加密主密钥。邮件密钥、环境文件、完整访问 Key 和备份不进入 Git。

反向代理部署前需配置受信代理及真实 IP 处理；当前限流使用网关连接地址，不无条件信任 `X-Forwarded-For`。登录与控制台写操作均有对应域名的来源检查；验证码还有频率限制。

## 集成测试

`make test-integration` 默认建立独立 `relaypine_test` schema 并运行串行包级测试。不要将 `MYSQL_TEST_DSN` 指向应用库；脚本会拒绝与 `MYSQL_DSN` 相同的连接串。CI 使用临时 MySQL / Redis。

集成测试使用注入的测试发信器，不向真实邮箱发送邮件。历史候补记录及普通客户的邮箱验证都不能用于授予管理权限。
