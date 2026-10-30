import type { LimitsCopy } from "./limits-en";

export const limitsZh: LimitsCopy = {
  limits: "限额",
  local: "{rpm} RPM · {tpm} TPM · 并发 {concurrency}",
  reported: "{model}：{rpm} RPM · {tpm} TPM",
  reportedSplit: "{model}：{rpm} RPM · 输入 {input} TPM · 输出 {output} TPM",
  reportedTitle: "服务商于 {date} 报告的限额。请按此设置该 Key 的限额。",
  reportedMore: "另有 {count} 个模型",
  anthropicTPM: "填写 Anthropic 的每分钟输入 Token 限额（ITPM）。与 Anthropic 一样，缓存读取和输出不计入。",
};
