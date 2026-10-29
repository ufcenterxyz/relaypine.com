package domain

import "regexp"

// Image generation model families the gateway can bill exactly. GPT Image 2
// and 2.5 report text and image tokens separately, and the size of their
// output is bounded by OpenAI's documented token calculator. Retired and
// retiring models (DALL-E, gpt-image-1, 1.5, 1-mini, chatgpt-image-latest)
// are not served.
// https://developers.openai.com/api/docs/guides/image-generation
const (
	ImageFamily2  = "gpt-image-2"
	ImageFamily25 = "gpt-image-2.5"
)

var (
	imageModel2  = regexp.MustCompile(`^gpt-image-2(?:-[0-9]{4}-[0-9]{2}-[0-9]{2})?$`)
	imageModel25 = regexp.MustCompile(`^gpt-image-2\.5-[a-z]+(?:-[0-9]{4}-[0-9]{2}-[0-9]{2})?$`)
	imageDated   = regexp.MustCompile(`-[0-9]{4}-[0-9]{2}-[0-9]{2}$`)
)

// ImageFamily returns the supported family of an OpenAI upstream model ID, or
// an empty string.
func ImageFamily(upstream string) string {
	switch {
	case imageModel2.MatchString(upstream):
		return ImageFamily2
	case imageModel25.MatchString(upstream):
		return ImageFamily25
	}
	return ""
}

// ImagePriceModel is the model whose published price covers an image model ID:
// a dated snapshot shares the price of its undated model.
func ImagePriceModel(upstream string) string {
	if ImageFamily(upstream) == "" {
		return upstream
	}
	return imageDated.ReplaceAllString(upstream, "")
}

// ModelKindFor derives a model's kind from where it routes. It is never taken
// from a client, so an image model cannot be published as a text model or the
// reverse.
func ModelKindFor(provider, upstream string) string {
	if provider == "openai" && ImageFamily(upstream) != "" {
		return ModelKindImage
	}
	return ""
}

// ImageRates lists the image sales and upstream cost rates.
func (m Model) ImageRates() []int64 {
	return []int64{m.ImageInputRate, m.ImageOutputRate, m.CostImageInputRate, m.CostImageOutputRate}
}
