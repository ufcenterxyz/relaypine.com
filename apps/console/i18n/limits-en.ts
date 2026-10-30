export const limitsEn = {
  limits: "Limits",
  local: "{rpm} RPM · {tpm} TPM · {concurrency} concurrent",
  reported: "{model}: {rpm} RPM · {tpm} TPM",
  reportedSplit: "{model}: {rpm} RPM · {input} input TPM · {output} output TPM",
  reportedTitle: "Reported by the provider on {date}. Set this key's limits to match its tier.",
  reportedMore: "+{count} more models",
} as const;

type Widen<T> = T extends string ? string : { [K in keyof T]: Widen<T[K]> };
export type LimitsCopy = Widen<typeof limitsEn>;
