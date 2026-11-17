export const flexEn = {
  flex: "Flex",
  flexRates: "Flex rates",
  flexPromptsOver: "Flex, prompts over {tokens} tokens",
  noFlex: "None",
  serviceTier: "Service tier",
  flexRan: "Flex",
  flexRanStandard: "Flex requested, ran at the standard tier",
} as const;

type Widen<T> = T extends string ? string : { [K in keyof T]: Widen<T[K]> };
export type FlexCopy = Widen<typeof flexEn>;
