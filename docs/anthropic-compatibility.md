# Anthropic 接入

RelayPine 保留 Anthropic 的 Messages 和 SSE 协议。使用 Anthropic SDK，base URL
为 `https://relaypine.com/anthropic`。OpenAI 路由不会转换为 Claude 请求。

## 后台设置

1. 在 **Upstreams** 选择或创建 Anthropic host。官方地址为
   `https://api.anthropic.com`，认证模式支持 Bearer 和 `x-api-key`；此设置属于
   host，按上游的要求选择。
2. 在 **Credentials** 添加上游 Key。对于未绑定工作区的 identity key，在
   **Anthropic workspace ID** 填入 `wrkspc_...`。若 Key 已绑定工作区，可以留空，
   或填写与其绑定一致的工作区。不能仅根据 `sk-ant-usr` 前缀判断是否需要填写。
3. 添加模型别名、实际上游模型 ID、用量和成本费率、输出上限，并启用模型。
4. 检查 RelayPine 访问 Key 的模型权限和可用余额，以及上游 Key 的预算、容量和出口。

新增 Key 时可先点击验证：有权限的身份 Key 可以读取工作区列表，只有一个选项时
自动选择，多个选项时请明确选择。模型列表及配置限额会作为可审阅的发现结果显示；
工作区绑定 Key 或权限不足时，不会要求提供额外管理员 Key。

官方 [Rate Limits API](https://platform.claude.com/docs/en/manage-claude/rate-limits-api)
允许未绑定工作区且具有相应权限的个人或服务账号 Key 查询组织和工作区限额。
RelayPine 读取模型组限额，并使用组织及工作区的保守最小值作为本地默认值；缺失值
不代表无限额度。普通模型列表的响应头不能可靠说明 Messages 的 RPM，所以不会
将其直接用于调度。工作区及模型查询的定义见
[Workspaces](https://platform.claude.com/docs/en/manage-claude/workspaces) 和
[Models API](https://platform.claude.com/docs/en/api/models/list)。

Workspace ID 与每把上游 Key 一起保存。调度选中该 Key 后，网关注入
`anthropic-workspace-id`。调用者提交同名请求头无法覆盖它，多 Key 调度也不会复用
另一把 Key 的工作区。输入格式为 `wrkspc_` 后接字母或数字，总长度不超过 128。

工作区规则依据 [Anthropic authentication](https://platform.claude.com/docs/en/manage-claude/authentication)
及 [Messages reference](https://platform.claude.com/docs/en/api/beta/messages/create)。

## SDK 调用

```ts
import Anthropic from "@anthropic-ai/sdk";

const client = new Anthropic({
  apiKey: process.env.RELAYPINE_API_KEY, // RelayPine 的 rp_live_...，不是上游 Key
  baseURL: "https://relaypine.com/anthropic",
});

const message = await client.messages.create({
  model: "claude-opus-5-5", // 必须与后台已启用且授权的模型别名一致
  max_tokens: 1024,
  messages: [{ role: "user", content: "Hello from RelayPine" }],
});
```

不需要在 SDK 中传工作区 ID；它由管理员配置。模型、价格和真实 Key 不会由本次
兼容性更新自动创建。

## Beta 支持范围

网关同时解析重复的 `anthropic-beta` 头和逗号分隔列表，清理空格并去重；不会再
静默丢弃。列表最多 16 项、总值长度最多 1024 字节。当前允许：

| Beta | 能力 |
| --- | --- |
| `task-budgets-2026-03-13` | 任务 token 预算提示 |
| `thinking-display-updates-2026-08-18` | 思考内容显示控制 |
| `thinking-binding-controls-2026-08-01` | 思考块绑定控制 |
| `interleaved-thinking-2025-05-14` | 需要此开关的模型的交错思考 |
| `fine-grained-tool-streaming-2025-05-14` | 需要此开关的模型的工具参数流式输出 |

具体模型是否支持某个 beta 仍由上游验证。新版模型已经正式提供的能力不必额外添加
旧 beta。此列表是 RelayPine 针对现有文本、客户端函数工具和单模型计费范围的支持
决策，不意味着任意 Anthropic beta 都可以使用。

例如，在已配置模型支持的前提下：

```ts
const message = await client.beta.messages.create({
  model: "claude-opus-5-5",
  max_tokens: 1024,
  betas: ["task-budgets-2026-03-13"],
  output_config: {
    effort: "high",
    task_budget: { type: "tokens", total: 4096 },
  },
  messages: [{ role: "user", content: "Explain this function." }],
});
```

该预算是模型的提示，不替代 RelayPine 的 token、并发、金额和输出上限。相关定义见
[Beta headers](https://platform.claude.com/docs/en/api/beta-headers)、
[Task budgets](https://platform.claude.com/docs/en/build-with-claude/task-budgets)、
[Thinking](https://platform.claude.com/docs/en/build-with-claude/thinking)。

## 明确拒绝的能力

未知 beta、空列表项、超长列表和当前不支持的 beta 会立即返回 HTTP 400。
`fallbacks` 请求字段和 `server-side-fallback-*` beta 同样被拒绝，发生在选路额度
预留和上游调用之前。

服务端 fallback 可以发起多次不同模型的推理，每次使用对应模型的费率。顶层
`usage` 只描述最后一次尝试，完整记录在 `usage.iterations`；直接透传会绕过候选
模型授权并错算成本。因此本版本仍要求客户端对已授权的模型分别发起请求，尚未
实现服务端 fallback 的逐模型授权、预留和结算。详见
[Refusals and fallback](https://platform.claude.com/docs/en/build-with-claude/refusals-and-fallback)。

Beta 请求仍受标准服务等级、文本与函数工具、缓存费率、最大输出量等检查约束。
工作区和 beta 头不会被转发给 OpenAI 上游。
