package relay

import "errors"

// Keep the existing conservative text/wrapper allowance, excluding only the
// logical base64 bytes that the image validator actually inspected. JSON escapes
// can leave extra bytes in this bound; that over-reserves rather than discounts
// unvalidated input. The original image payload is still forwarded unchanged.
func estimatedInputTokens(bodyBytes int, images imageInputSummary) (int64, error) {
	if bodyBytes < 0 || bodyBytes > maxRequestBytes || images.Base64Bytes < 0 || images.Base64Bytes > int64(bodyBytes) {
		return 0, errors.New("invalid image input bound")
	}
	input := 2*(int64(bodyBytes)-images.Base64Bytes) + 4096
	for _, image := range images.Images {
		if image.Width < 1 || image.Height < 1 || image.Width > 8000 || image.Height > 8000 {
			return 0, errors.New("invalid image dimensions")
		}
		// Claude documents ceil(width/28)*ceil(height/28) visual tokens, with
		// oversized images downscaled. Use the original dimensions, a 2x margin
		// and per-image framing allowance rather than guessing a tier from a
		// configurable model alias. This is a reservation, never the final bill.
		// https://platform.claude.com/docs/en/build-with-claude/vision
		patches := ((int64(image.Width) + 27) / 28) * ((int64(image.Height) + 27) / 28)
		input += 2*patches + 256
	}
	return input, nil
}
