# RelayPine 运维说明

## 环境与进程

本地端口：Next.js 4810、Go 4811、MySQL 4812、Redis 4813。`yarn build` 准备 standalone 静态资源，`HOSTNAME=127.0.0.1 PORT=4810 yarn start` 启动本地生产预览。`GATEWAY_URL` 用于构建时生成 API 转发地址，变更后需要重新构建。

Compose 数据卷属于 `relaypine`。停止依赖可用 `docker compose stop`；不要用 `down -v` 删除业务数据。

Go 网关每 15 秒运行请求恢复。Stripe 客户端、支付收件箱处理、支付对账及相关 HTTP 入口已移除，环境不再需要 `STRIPE_*` 变量。历史支付数据保留，不再自动处理。

生产要求 `APP_ENV=production`、HTTPS `APP_URL`，并关闭 `ALLOW_PRIVATE_UPSTREAMS`。反向代理将 `/api/*`、`/openai/*`、`/anthropic/*` 指向 Go，其余指向 Next.js。流式接口禁用代理缓冲并设置足够超时；单请求最长 5 分钟，发送后不自动重放。

## 管理员与候补名单

唯一管理员邮箱固定为 `ufcenterxyz@gmail.com`，通过邮件中的 6 位验证码登录。`BOOTSTRAP_NAME` 可设置显示名称；`BOOTSTRAP_EMAIL` 不用于授权，不能通过环境替换它来增加第二个管理员。`BOOTSTRAP_PASSWORD` 已停用，可以从旧环境配置中删除。

启动初始化会确保固定管理员存在，将其他现有用户去重导入候补并撤销其他 operator 标记。原有同邮箱普通用户提升时撤销旧会话与 API Key。旧密码登录会话不再可用；只有通过验证码建立的管理员会话可访问后台。

邮箱字符串本身不授予权限。验证码验证成功后，固定管理员获得会话，其他邮箱仅保存候补联系资料，不获得 API、钱包或控制台权限。管理员在 **Waitlist** 查看名单；当前没有放行申请的 API。升级导入的历史候补资料不应视为已验证身份。

升级前备份 MySQL。迁移是增加候补表并限制访问，历史用户、钱包、请求、订单等数据不会被删除。现有非管理员会话与访问 Key 即使仍存在于数据库，也会被访问检查拒绝。

## 验证码邮件

通过部署 Secret 注入 `MAILGUN_API_KEY`、`MAILGUN_DOMAIN` 和 `MAILGUN_FROM`。发信域名必须在 Mailgun 验证完成，From 示例为 `RelayPine <login@mg.relaypine.com>`。默认 API 区域为 `https://api.mailgun.net`；欧盟域名使用 `MAILGUN_API_BASE=https://api.eu.mailgun.net`。具体发送参数参见 [Mailgun 官方接口说明](https://documentation.mailgun.com/docs/mailgun/api-reference/send/mailgun/messages/post-v3--domain-name--messages)。

未配置发信服务时网关仍可启动，但发送验证码返回 `503 email_unavailable`。应用不会把验证码写入日志、返回给浏览器或启用固定测试码。只有邮件服务接受发送后才开放该验证码的校验；发送失败不会发放会话。

验证码有效 10 分钟，每封最多尝试 5 次，重发间隔 60 秒；重发使旧码失效，成功验证以 Redis 原子操作消费。Redis 保存 HMAC 摘要及临时候补姓名，摘要密钥由 `MASTER_KEY` 派生；验证码不以明文持久化。邮箱和连接地址均限流，Redis 故障时拒绝验证。

上线需完成实际收件测试，并监测发信失败率、限流和邮件延迟。发信凭据应限制到需要的域名；不要将密钥或验证码加入监控日志。

## 用量与预算监控

- `requests` 中未知用量、`cost_pending` 需供应商证据核对，不能当作零成本。
- `wallets.held_micros` 与 `credentials.held_micros` 分别表示调用额度和上游成本预留。
- 配置代理不可用时拒绝相应路线；不允许静默绕过代理。
- 内部额度调整需填写原因，保留操作者及唯一业务参考。

本地预算不等于供应商余额；管理员自己的调用仍受访问 Key、租户限流和预算约束。移除 Stripe 不等于将钱包设为无限。

## 备份与秘密

上游 Key / SOCKS5 凭据使用 AES-GCM，访问 Key 和会话只存哈希，验证码使用 HMAC 摘要。历史密码字段不再用于认证。备份 MySQL 和 `MASTER_KEY` 并验证恢复；不要随意重新生成加密主密钥。邮件密钥、环境文件、完整访问 Key 和备份不进入 Git。

反向代理部署前需配置受信代理及真实 IP 处理；当前限流使用网关连接地址，不无条件信任 `X-Forwarded-For`。候补表单与登录均有来源检查和频率限制。

## 集成测试

`make test-integration` 默认建立独立 `relaypine_test` schema 并运行串行包级测试。不要将 `MYSQL_TEST_DSN` 指向应用库；脚本会拒绝与 `MYSQL_DSN` 相同的连接串。CI 使用临时 MySQL / Redis。

集成测试使用注入的测试发信器，不向真实邮箱发送邮件。历史候补记录及新验证记录都不能用于直接授予管理权限。
