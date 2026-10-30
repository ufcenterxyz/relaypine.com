import type { LimitsCopy } from "./limits-en";

export const limitsZh: LimitsCopy = {
  limits: "限额",
  local: "{rpm} RPM · {tpm} TPM · 并发 {concurrency}",
  reported: "{model}：{rpm} RPM · {tpm} TPM",
  reportedSplit: "{model}：{rpm} RPM · 输入 {input} TPM · 输出 {output} TPM",
  reportedTitle: "服务商于 {date} 报告的限额。请按此设置该 Key 的限额。",
  reportedMore: "另有 {count} 个模型",
};
