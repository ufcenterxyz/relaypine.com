package relay

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strings"
)

// OpenAI image inputs in Chat Completions and Responses. OpenAI counts image
// tokens inside the reported input tokens, so settlement stays exact. The
// reservation adds a bound per image that holds for any image size, because a
// remote image's dimensions are unknown until OpenAI reads it.

const maxOpenAIImageURLBytes = 2048

var openAIImageDataURL = regexp.MustCompile(`^data:(image/(?:png|jpeg|webp|gif));base64,`)

const openAIImageDetails = "auto, low, high or original"

// openAIVisionProfile is the most tokens one image can cost per detail level.
// Resizing caps every image, so the bound holds whatever the source size.
type openAIVisionProfile struct{ low, high, original, auto int64 }

// The largest image cost per model and detail level, from OpenAI's vision
// sizing rules and token calculator: patch models fit an image within a size
// limit, count 32 px patches up to a budget and multiply; tile models count 512
// px tiles after resizing. An omitted detail means auto.
// https://developers.openai.com/api/docs/guides/images-vision
var openAIVisionProfiles = []struct {
	model   *regexp.Regexp
	profile openAIVisionProfile
}{
	{regexp.MustCompile(`^(?:gpt-6-astra|gpt-5\.6-(?:sol|terra|luna))(?:-[0-9]{4}-[0-9]{2}-[0-9]{2})?$`), openAIVisionProfile{low: 308, high: 3000, original: 36000, auto: 36000}},
	{regexp.MustCompile(`^gpt-5\.5(?:-[0-9]{4}-[0-9]{2}-[0-9]{2})?$`), openAIVisionProfile{low: 308, high: 3000, original: 12000, auto: 12000}},
	{regexp.MustCompile(`^gpt-5\.4(?:-mini|-nano)?(?:-[0-9]{4}-[0-9]{2}-[0-9]{2})?$`), openAIVisionProfile{low: 4916, high: 3000, original: 12000, auto: 3000}},
	{regexp.MustCompile(`^gpt-5\.2(?:-[0-9]{4}-[0-9]{2}-[0-9]{2})?$`), openAIVisionProfile{low: 4916, high: 4916, original: 4916, auto: 4916}},
	{regexp.MustCompile(`^gpt-4\.1-mini(?:-[0-9]{4}-[0-9]{2}-[0-9]{2})?$`), openAIVisionProfile{low: 6636, high: 6636, original: 6636, auto: 6636}},
	{regexp.MustCompile(`^gpt-4o-mini(?:-[0-9]{4}-[0-9]{2}-[0-9]{2})?$`), openAIVisionProfile{low: 2833, high: 48169, original: 48169, auto: 48169}},
	{regexp.MustCompile(`^(?:gpt-4o|gpt-4\.1)(?:-[0-9]{4}-[0-9]{2}-[0-9]{2})?$`), openAIVisionProfile{low: 85, high: 1445, original: 1445, auto: 1445}},
	{regexp.MustCompile(`^gpt-5(?:\.1)?(?:-[0-9]{4}-[0-9]{2}-[0-9]{2})?$`), openAIVisionProfile{low: 70, high: 1190, original: 1190, auto: 1190}},
	{regexp.MustCompile(`^o3(?:-[0-9]{4}-[0-9]{2}-[0-9]{2})?$`), openAIVisionProfile{low: 75, high: 1275, original: 1275, auto: 1275}},
	{regexp.MustCompile(`^gpt-5-mini(?:-[0-9]{4}-[0-9]{2}-[0-9]{2})?$`), openAIVisionProfile{low: 36000, high: 36000, original: 36000, auto: 36000}},
	{regexp.MustCompile(`^gpt-5-nano(?:-[0-9]{4}-[0-9]{2}-[0-9]{2})?$`), openAIVisionProfile{low: 45000, high: 45000, original: 45000, auto: 45000}},
}

// Models without published sizing rules, such as gpt-6.1, gpt-6-sol and pro
// variants: OpenAI rejects any image above 30,000 patches, at the standard
// 1.2 tokens per patch.
var unknownVisionProfile = openAIVisionProfile{low: 36000, high: 36000, original: 36000, auto: 36000}

// openAIImageTokenBound is the most tokens one image can cost on a model at a
// detail level, plus one token for rounding.
func openAIImageTokenBound(upstream, detail string) (int64, bool) {
	profile := unknownVisionProfile
	for _, p := range openAIVisionProfiles {
		if p.model.MatchString(upstream) {
			profile = p.profile
			break
		}
	}
	var bound int64
	switch detail {
	case "low":
		bound = profile.low
	case "high":
		bound = profile.high
	case "original":
		bound = profile.original
	case "auto":
		bound = profile.auto
	default:
		return 0, false
	}
	return bound + 1, true
}

// addOpenAIImage validates one image source and adds its token bound.
func (s *imageInputSummary) addOpenAIImage(upstream, source, detail string) error {
	if len(s.Images)+s.OpenAIImages >= maxInputImages {
		return errors.New("request exceeds the 20 image limit")
	}
	bound, ok := openAIImageTokenBound(upstream, detail)
	if !ok {
		return errors.New("image detail must be " + openAIImageDetails)
	}
	switch {
	case strings.HasPrefix(source, "data:"):
		match := openAIImageDataURL.FindStringSubmatch(source)
		if match == nil {
			return errors.New("image data URLs must be base64 PNG, JPEG, WebP or GIF")
		}
		data := source[len(match[0]):]
		if len(data) < 16 || len(data)%4 != 0 {
			return errors.New("invalid image data")
		}
		head := make([]byte, 12)
		n, err := base64.StdEncoding.Decode(head, []byte(data[:16]))
		if err != nil || !matchesImageType(head[:n], match[1]) {
			return errors.New("image data does not match its image type")
		}
		s.Base64Bytes += int64(len(data))
	case strings.HasPrefix(source, "https://"):
		u, err := url.Parse(source)
		if err != nil || u.Host == "" || u.User != nil || len(source) > maxOpenAIImageURLBytes {
			return errors.New("image URLs must be valid https URLs of at most 2048 bytes")
		}
	default:
		return errors.New("images must be https URLs or base64 data URLs; file IDs are not supported")
	}
	s.OpenAIImages++
	s.OpenAITokens += bound
	return nil
}

func matchesImageType(head []byte, mediaType string) bool {
	if mediaType == "image/gif" {
		return bytes.HasPrefix(head, []byte("GIF87a")) || bytes.HasPrefix(head, []byte("GIF89a"))
	}
	return sniffImage(head) == mediaType
}

// inspectOpenAIContent walks one content value. Strings stay opaque; image
// parts are accepted only where the protocol allows them, and every other part
// keeps the existing validation.
func (s *imageInputSummary) inspectOpenAIContent(raw json.RawMessage, allowImages bool, imageType, upstream string) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil
	}
	if trimmed[0] != '[' {
		var value any
		if json.Unmarshal(raw, &value) != nil {
			return errors.New("invalid message content")
		}
		return validateContent(value, "content")
	}
	var parts []json.RawMessage
	if json.Unmarshal(raw, &parts) != nil {
		return errors.New("invalid message content")
	}
	for _, rawPart := range parts {
		part, err := imageJSONObject(rawPart)
		if err != nil {
			return errors.New("invalid content part")
		}
		if str(part["type"]) != imageType {
			if err := validateImageContainer(part, ""); err != nil {
				return err
			}
			continue
		}
		if !allowImages {
			return errors.New("images are supported only in user message content")
		}
		source, detail, err := openAIImagePart(part, imageType)
		if err != nil {
			return err
		}
		if err := s.addOpenAIImage(upstream, source, detail); err != nil {
			return err
		}
	}
	return nil
}

// openAIImagePart reads Chat's {type:image_url, image_url:{url, detail}} or
// Responses' {type:input_image, image_url, detail}. A file ID could name
// another customer's upload in the shared upstream account, so it is refused.
func openAIImagePart(part map[string]json.RawMessage, imageType string) (string, string, error) {
	fields, source := part, part["image_url"]
	if imageType == "image_url" {
		inner, err := imageJSONObject(part["image_url"])
		if err != nil {
			return "", "", errors.New("image_url must be an object with url")
		}
		for key := range part {
			if key != "type" && key != "image_url" {
				return "", "", errors.New("unsupported image field: " + key)
			}
		}
		fields, source = inner, inner["url"]
	}
	for key := range fields {
		switch key {
		case "type", "image_url", "url", "detail":
		case "file_id":
			return "", "", errors.New("file_id references are not supported; send the image data or an https URL")
		default:
			return "", "", errors.New("unsupported image field: " + key)
		}
	}
	value, ok := jsonString(source)
	if !ok {
		return "", "", errors.New("image URL must be a string")
	}
	detail := "auto"
	if raw, exists := fields["detail"]; exists {
		if detail, ok = jsonString(raw); !ok {
			return "", "", errors.New("image detail must be a string")
		}
	}
	return value, detail, nil
}

// inspectOpenAIChatImages validates Chat Completions messages: images only in
// user content, everything else under the existing rules.
func inspectOpenAIChatImages(raw json.RawMessage, upstream string) (imageInputSummary, error) {
	var summary imageInputSummary
	var messages []json.RawMessage
	if json.Unmarshal(raw, &messages) != nil {
		return summary, errors.New("invalid messages")
	}
	for _, message := range messages {
		m, err := imageJSONObject(message)
		if err != nil {
			return imageInputSummary{}, errors.New("invalid message object")
		}
		if err := validateImageContainer(m, "content"); err != nil {
			return imageInputSummary{}, err
		}
		if err := summary.inspectOpenAIContent(m["content"], str(m["role"]) == "user", "image_url", upstream); err != nil {
			return imageInputSummary{}, err
		}
	}
	return summary, nil
}

// inspectResponsesImages validates Responses input: images in user messages
// and in function call outputs, everything else under the existing rules.
func inspectResponsesImages(raw json.RawMessage, upstream string) (imageInputSummary, error) {
	var summary imageInputSummary
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		var value any
		if json.Unmarshal(raw, &value) != nil {
			return summary, errors.New("invalid input")
		}
		return summary, validateContent(value, "")
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return summary, errors.New("invalid input")
	}
	for _, rawItem := range items {
		item, err := imageJSONObject(rawItem)
		if err != nil {
			return imageInputSummary{}, errors.New("invalid input item")
		}
		kind := str(item["type"])
		switch {
		case kind == "message" || kind == "" && item["role"] != nil:
			if err := validateImageContainer(item, "content"); err != nil {
				return imageInputSummary{}, err
			}
			if err := summary.inspectOpenAIContent(item["content"], str(item["role"]) == "user", "input_image", upstream); err != nil {
				return imageInputSummary{}, err
			}
		case kind == "function_call_output" || kind == "custom_tool_call_output":
			if err := validateImageContainer(item, "output"); err != nil {
				return imageInputSummary{}, err
			}
			if err := summary.inspectOpenAIContent(item["output"], true, "input_image", upstream); err != nil {
				return imageInputSummary{}, err
			}
		default:
			if err := validateImageContainer(item, ""); err != nil {
				return imageInputSummary{}, err
			}
		}
	}
	return summary, nil
}
