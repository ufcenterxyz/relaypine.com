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
3. 在 **Models & rates → Import from upstream** 选择保存的 Key 和模型，自动填入
   协议、模型别名、上游 ID 和输出上限；补齐销售和成本的输入/输出费率后启用。
   也可手动添加。若导入发现了尚未保存的工作区，应先在流程中明确保存到凭据。
4. 检查 RelayPine 访问 Key 的模型权限和可用余额，以及上游 Key 的预算、容量和出口。

新增 Key 时可先点击验证：有权限的身份 Key 可以读取工作区列表，只有一个选项时
自动选择，多个选项时请明确选择。模型列表及配置限额会作为可审阅的发现结果显示；
工作区绑定 Key 或权限不足时，不会要求提供额外管理员 Key。

**验证 Key 不等于发布模型。** `/anthropic/v1/models` 读取 RelayPine 的本地模型目录，
只列出已启用、协议匹配且访问 Key 和上游配置允许的条目。本地目录为空时返回空列表，
即使上游发现已成功。余额不影响模型列表，但余额为零会阻止付费生成请求。

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

## 内联图片

`POST /anthropic/v1/messages` 与 `POST /anthropic/v1/messages/count_tokens`
支持 user 消息 content 数组中的 `image` 块，也支持 user 消息的
`tool_result.content` 数组内的图片。assistant、system、工具参数或其他位置不会
因为形似图片 JSON 而获得图片支持；嵌套的 `tool_result` 不支持。

图片 `source` 只允许 `type`、`media_type`、`data` 三个字段：`type` 必须为
`base64`，`media_type` 必须为 `image/png` 或 `image/jpeg`，`data` 为标准 base64
字符串，不带 data URL 前缀、换行或空格。图片块只允许 `type`、`source` 和可选的
`cache_control`；缓存规则不变，仍拒绝需要单独定价的一小时缓存写入。

| 本地限制 | 上限 |
| --- | --- |
| 整个 HTTP JSON 请求，含文本、base64 和封装 | 2 MiB |
| 每个请求的图片总数，含工具结果中的图片 | 20 |
| 每张图的宽、高 | 各 8,000 像素 |
| 每张图的像素面积 | 16,000,000 |
| 请求内所有图片的像素面积之和 | 32,000,000 |

所有限制同时生效，不代表上游模型保证接受该大小。文件必须完整可解码且媒体类型
匹配；APNG、GIF、WebP、损坏或截断文件、URL 来源和文件 ID 引用均拒绝。OpenAI
路由仍不支持图片。网关不下载远程图片、不缩放或重新编码图片，已验证的图片
payload 保留后转发，也不写图片请求日志；既有模型别名替换和输出上限处理不变。

SDK 图片示例（沿用上文的 `client`）：

```ts
import { readFile } from "node:fs/promises";

const input: Parameters<typeof client.messages.countTokens>[0] = {
  model: "your-enabled-vision-model",
  messages: [{ role: "user", content: [
    { type: "image", source: {
      type: "base64", media_type: "image/png",
      data: (await readFile("screenshot.png")).toString("base64"),
    } },
    { type: "text", text: "描述这张图片。" },
  ] }],
};
// 可选：客户端明确发起的免费 token 查询；网关不会自动代为调用。
const tokens = await client.messages.countTokens(input);
const message = await client.messages.create({ ...input, max_tokens: 256 });
```

使用 JPEG 时同时改为真实 JPEG 文件和 `media_type: "image/jpeg"`。编码前先裁剪
或缩小不必要的区域；照片可降低 JPEG 质量，文字和图表可保留 PNG。base64 约增加
三分之一体积，必须为文本与 JSON 留出空间。网关自身不会压缩以绕过请求上限。

请求预留只从原文本/请求体估算中扣除已验证图片位置的 base64 逻辑长度，再为每图
加入 `2 * ceil(width / 28) * ceil(height / 28) + 256` 的保守输入上界。相同上界用于
客户/上游资金预留和长上下文计价判断，TPM 准入也计入其中的图片上界；它不是实际 token 数或最终账单，
可信 usage 到达后按价格快照结算。`count_tokens` 不预留金额或扣取 RelayPine
余额，但保留模型、鉴权、上下文和容量限制，且不支持 streaming。详见
[用量与额度说明](commercial-billing.md)。

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
旧 beta。此列表是 RelayPine 针对现有文本、内联图片、客户端函数工具和单模型计费范围的支持
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

Beta 请求仍受标准服务等级、支持的文本/图片与函数工具、缓存费率、最大输出量等检查约束。
工作区和 beta 头不会被转发给 OpenAI 上游。
