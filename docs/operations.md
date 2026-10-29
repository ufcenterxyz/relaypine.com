# RelayPine 运维说明

## 环境与进程

本地端口：Next.js 4810、Go 4811、MySQL 4812、Redis 4813。`yarn build` 准备 standalone 静态资源，`HOSTNAME=127.0.0.1 PORT=4810 yarn start` 启动本地生产预览。用户入口为 `localhost:4810`，管理入口为 `admin.localhost:4810`。Next.js 的服务端 `/api` 代理在运行时读取 `GATEWAY_URL`，并按 `APP_URL` / `ADMIN_APP_URL` 验证及保留 Host；修改后重启 Web 进程。初始化脚本会将这三个非秘密配置补入 `apps/console/.env.local`，自定义域名时确保 Web 与网关配置一致。

Compose 数据卷属于 `relaypine`。停止依赖可用 `docker compose stop`；不要用 `down -v` 删除业务数据。

请求详情需要的请求与响应正文保存在 `request_payloads` 表，gzip 压缩后用 `MASTER_KEY` 加密，与请求 ID 绑定。`REQUEST_BODY_RETENTION_DAYS` 控制保留天数（默认 7，范围 0–3650），所有副本必须使用相同取值；设为 `0` 会停止记录并在下一轮清理中删除已存正文。每个请求最多保存 2 MiB 请求和 4 MiB 响应，正文较大或流量较高时需相应评估数据库容量。备份数据库时同样包含这些加密正文，需配合 `MASTER_KEY` 才能读取。

Go 网关每 15 秒运行请求恢复，并分批删除超过保留期的请求正文。Stripe 客户端、支付收件箱处理、支付对账及相关 HTTP 入口已移除，环境不再需要 `STRIPE_*` 变量。历史支付数据保留，不再自动处理。

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
- `requests` 中未知用量、`cost_pending` 需供应商证据核对，不能当作零成本。
- `wallets.held_micros` 与 `credentials.held_micros` 分别表示调用额度和上游成本预留。
- 配置代理不可用时拒绝相应路线；不允许静默绕过代理。
- 内部额度调整需填写原因，保留操作者及唯一业务参考。
- 备用通道（OpenRouter / ZenMux）在 Upstreams 配置，使用 **Check key** 确认 Key 和模型覆盖范围；Key 检查不发起付费推理。备用通道只在官方路线全部不可用时承接 Chat Completions 和 Messages，消费上限需在服务商账户设置。管理员请求记录中带多次尝试的请求即发生过换路，可按尝试的 `error_code` 判断官方故障类型。被拒绝的 Key 会暂时冷却（401/402/403 五分钟，429 按 Retry-After，5xx 与未发出请求 30 秒），冷却期间请求直接走下一条路线。
- 备用通道余额需要服务商的管理 Key：OpenRouter 的 Management Key（同时可以创建和删除 API Key，权限较大）或 ZenMux 的 Management API Key。管理 Key 只用于读取余额，按服务商加密保存，不参与转发，也不会返回给浏览器；不需要时可在编辑弹框中删除。余额每分钟最多读取一次，点击刷新会重新读取。
- 备用通道兼容性实测：在 `services/gateway` 运行 `RELAYPINE_LIVE_OPENROUTER_KEY=... RELAYPINE_LIVE_ZENMUX_KEY=... go test -run TestLiveBackupCompatibility -v ./internal/relay`。测试让本地官方上游返回 529，经真实故障切换路径向服务商发送 Chat、流式 Chat、工具调用、Messages、流式 Messages 和带 beta 头的工具流式请求，每项最多 64 个输出 Token，总成本不到一美分。未设置 Key 时自动跳过。

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
