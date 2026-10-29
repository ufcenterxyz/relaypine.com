export const imageEn = {
  imageModel: "Image",
  text: "Text",
  image: "Image",
  imageInputRate: "Image input",
  imageOutputRate: "Image output",
  costImageInputRate: "Upstream image input cost",
  costImageOutputRate: "Upstream image output cost",
  imageRates: "Image rates",
  imageRatesHelp:
    "GPT Image models bill text input, input images and generated images per token. They have no text output price, so leave text output at 0.",
  textInput: "Text input",
  imageInput: "Image input",
  imageOutput: "Image output",
  notApplicable: "—",
  imageInputTokens: "Image input tokens",
  imageOutputTokens: "Image output tokens",
  imageTextTokens: "Image prompt tokens",
} as const;

type Widen<T> = T extends string ? string : { [K in keyof T]: Widen<T[K]> };
export type ImageCopy = Widen<typeof imageEn>;
