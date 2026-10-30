import type { LongContextCopy } from "./long-context-en";

export const longContextZh: LongContextCopy = {
  longContextRates: "长上下文价格",
  longContextEnabled: "长 prompt 加价",
  longContextHelp:
    "服务商对超过一定长度的 prompt 收更高价格时使用，例如 OpenAI 对超过 272K Token 的 prompt 加价。prompt 超过阈值时，整个请求（包括输出）都按这里的价格计费；prompt 长度也包括缓存读取和缓存写入。",
  longContextAbove: "prompt 超过（Token）",
  longInputRate: "长上下文输入",
  longOutputRate: "长上下文输出",
  longCachedRate: "长上下文缓存输入",
  longCacheWriteRate: "长上下文缓存写入",
  costLongInputRate: "上游长上下文输入成本",
  costLongOutputRate: "上游长上下文输出成本",
  costLongCachedRate: "上游长上下文缓存输入成本",
  costLongCacheWriteRate: "上游长上下文缓存写入成本",
  thresholdRange: "请输入 1 到 100,000,000 之间的整数 Token 数。",
  positiveRate: "请输入大于 $0 的价格。",
  belowStandard: "不能低于标准价格。",
  over: "超过 {tokens}",
  promptsOver: "prompt 超过 {tokens} Token",
  noLongContext: "无",
  priceTier: "计价",
  longContextApplied: "长上下文价格",
};
