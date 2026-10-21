# LLM Relay

用于统一接入 Claude 和 OpenAI 的内部 API 网关。管理多个官方或兼容上游、多把 API Key、共享限流与预算，并为每把 Key 配置独立 SOCKS5 出口。

## 项目状态

2026-10-21：完成立项与技术规划。当前仓库仅包含规划文档，尚未生成应用脚手架、实现网关或部署服务。

暂定名称为 LLM Relay，目录名为 `llm-relay`。默认按内部自用、多业务接入设计；客户售卖、充值与计费门户放在后续阶段。

## 核心设计

- Claude 与 OpenAI 各自保留原生协议，包括 JSON 与 SSE。
- 平台提供官方地址默认值，每个平台允许创建多个兼容 Endpoint。
- 多 Key 共享组织、项目或 workspace 的限额时，统一按额度组调度。
- 支持负载优先、额度优先、均衡三种路由策略。
- 区分本地预算、手工余额和供应商余额，执行预留、结算与对账。
- 每 Key 显式选择直连或 SOCKS5，代理故障不静默直连。
- 业务方使用独立的访问 Key，上游密钥只保存在服务器端。

## 规划文档

- [产品与架构方案](docs/architecture.md)
- [开发阶段与验收标准](docs/implementation-plan.md)

## 拟定技术栈

| 模块 | 方案 |
| --- | --- |
| 中转与管理 API | Go，常驻容器 |
| 管理后台 | Next.js、TypeScript、HeroUI、Tailwind CSS |
| 配置与预算账本 | MySQL |
| 限流、并发租约、冷却 | Redis |
| 本地开发 | Docker Compose |
| 后续部署 | TKE，网关与后台独立扩容 |

后台脚手架参考本机 `qruiq-create-app` 模板；安装时重新核对框架的受支持版本与安全补丁，不直接锁定旧模板版本。
