package relay

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"relaypine.com/gateway/internal/domain"
)

// The Responses image_generation tool. The text model's tokens arrive in
// usage; the image model's in tool_usage.image_gen, which OpenAI documents as
// possibly missing or undercounted. A request therefore names its image model
// explicitly and bounds its image calls with max_tool_calls. It settles at the
// reported image tokens, raised to OpenAI's calculator for every generated image
// of known size and quality; without a report, at the worst case for each call.
// https://developers.openai.com/api/docs/guides/tools-image-generation
const (
	defaultImageToolCalls = 1
	maxImageToolCalls     = 4
	// The image prompt is written by the text model and its length is not
	// documented; the Images API accepts 32,000 characters.
	imageToolTextBound = 32768
	// An edit reads images already in the conversation: the request's own,
	// plus up to this many from earlier responses the gateway cannot see.
	imageToolContextImages = 4
	// A reply carries each generated image as base64.
	maxImageToolResponseBytes = 64 << 20
)

// imageToolPlan is the validated image_generation tool of one request.
type imageToolPlan struct {
	model         domain.Model
	family        string
	quality       string
	width, height int
	partials      int
	calls         int
	stream        bool
	// contextImages bounds the images one edit can read: the request's own
	// image inputs and images from earlier responses.
	contextImages int
}

// prepareImageTool validates the image_generation tool of a Responses request,
// pins its image model and call bound, and rewrites both into the body.
func (h *Handler) prepareImageTool(p *plan, m map[string]json.RawMessage, models []domain.Model) error {
	var tools []json.RawMessage
	if raw, ok := m["tools"]; !ok || json.Unmarshal(raw, &tools) != nil {
		return nil
	}
	index := -1
	for i, t := range tools {
		if str(object(t)["type"]) == "image_generation" {
			if index >= 0 {
				return errors.New("declare the image generation tool once")
			}
			index = i
		}
	}
	if index < 0 {
		return nil
	}
	if raw, ok := m["multi_agent"]; ok && string(raw) != "null" {
		return errors.New("multi-agent requests cannot use the image generation tool")
	}
	tool, err := imageJSONObject(tools[index])
	if err != nil {
		return errors.New("invalid image generation tool")
	}
	for key := range tool {
		switch key {
		case "type", "model", "size", "quality", "background", "output_format", "output_compression", "moderation", "partial_images", "action":
		case "input_fidelity":
			return errors.New("input_fidelity is not supported by GPT Image 2 models")
		case "input_image_mask":
			return errors.New("masks are not supported by the image generation tool")
		default:
			return errors.New("unsupported image generation tool field: " + key)
		}
	}
	// An omitted model is pinned: OpenAI's default may change, and the bill
	// uses this model's rates.
	name := "gpt-image-2"
	if raw, ok := tool["model"]; ok {
		if name, ok = jsonString(raw); !ok {
			return errors.New("image generation model must be a string")
		}
	}
	var image domain.Model
	for _, candidate := range models {
		if candidate.Enabled && candidate.IsImage() && candidate.Provider == "openai" && allowed(p.key.Models, candidate.ID) && (candidate.ID == name || candidate.UpstreamModel == name && image.ID == "") {
			image = candidate
			if candidate.ID == name {
				break
			}
		}
	}
	if image.ID == "" {
		return fmt.Errorf("image model %s is not available to this key", name)
	}
	t := &imageToolPlan{model: image, family: domain.ImageFamily(image.UpstreamModel), quality: "auto", stream: p.stream, contextImages: imageToolContextImages + p.inputImages}
	if t.family == "" || !validImageAccounting(image) {
		return errors.New("the image generation model has no complete image price configuration")
	}
	if raw, ok := tool["size"]; ok {
		s, valid := jsonString(raw)
		if !valid {
			return errors.New("size must be auto or WIDTHxHEIGHT")
		}
		if t.width, t.height, err = parseImageSize(s); err != nil {
			return err
		}
	}
	if raw, ok := tool["quality"]; ok {
		if t.quality, err = oneOf(raw, "quality", imageQualities[t.family]...); err != nil {
			return err
		}
	}
	background, format := "auto", "png"
	if raw, ok := tool["background"]; ok {
		if background, err = imageBackground(raw, t.family); err != nil {
			return err
		}
	}
	if raw, ok := tool["output_format"]; ok {
		if format, err = oneOf(raw, "output_format", "png", "jpeg", "webp"); err != nil {
			return err
		}
	}
	if background == "transparent" && format == "jpeg" {
		return errors.New("transparent backgrounds need png or webp output")
	}
	if raw, ok := tool["output_compression"]; ok {
		if n, valid := integer(raw); !valid || n > 100 {
			return errors.New("output_compression must be between 0 and 100")
		}
	}
	if raw, ok := tool["moderation"]; ok {
		if _, err = oneOf(raw, "moderation", "low", "auto"); err != nil {
			return err
		}
	}
	if raw, ok := tool["action"]; ok {
		if _, err = oneOf(raw, "action", "generate", "edit", "auto"); err != nil {
			return err
		}
	}
	if raw, ok := tool["partial_images"]; ok {
		n, valid := integer(raw)
		if !valid || n > 3 {
			return errors.New("partial_images must be between 0 and 3")
		}
		t.partials = int(n)
	}
	// max_tool_calls bounds every built-in tool call of a response, and this
	// is the only built-in tool the gateway relays.
	t.calls = defaultImageToolCalls
	if raw, ok := m["max_tool_calls"]; ok && string(raw) != "null" {
		n, valid := integer(raw)
		if !valid || n < 1 || n > maxImageToolCalls {
			return fmt.Errorf("max_tool_calls must be between 1 and %d with the image generation tool", maxImageToolCalls)
		}
		t.calls = int(n)
	}
	m["max_tool_calls"] = json.RawMessage(strconv.Itoa(t.calls))
	tool["model"], _ = json.Marshal(image.UpstreamModel)
	if tools[index], err = json.Marshal(tool); err != nil {
		return err
	}
	if m["tools"], err = json.Marshal(tools); err != nil {
		return err
	}
	p.imageTool = t
	return nil
}

// perCall is the worst-case image output of one call, partial images included.
func (t *imageToolPlan) perCall(quality string, width, height int) int64 {
	cells := imageCells[t.family][quality]
	per := maxImageOutputTokens(cells)
	if width > 0 {
		per = imageOutputTokens(cells, width, height)
	}
	per = (per*imageMargin[t.family] + 99) / 100
	if t.stream {
		per += int64(t.partials) * partialImageTokens
	}
	return per
}

// bounds is the reservation for every allowed call.
func (t *imageToolPlan) bounds() (text, image, output int64) {
	calls := int64(t.calls)
	return calls * imageToolTextBound, calls * int64(t.contextImages) * imageInputTokenBound, calls * t.perCall(t.quality, t.width, t.height)
}

// usage is the image model's billed tokens for one Response object. partials
// counts the partial images a stream delivered, 100 output tokens each.
func (t *imageToolPlan) usage(response map[string]json.RawMessage, partials int) (text, image, output int64) {
	type call struct {
		Status, Size, Quality, Action, RevisedPrompt string
	}
	var items []json.RawMessage
	_ = json.Unmarshal(response["output"], &items)
	calls := []call{}
	for _, raw := range items {
		item := object(raw)
		if str(item["type"]) != "image_generation_call" {
			continue
		}
		calls = append(calls, call{str(item["status"]), str(item["size"]), str(item["quality"]), str(item["action"]), str(item["revised_prompt"])})
	}
	// OpenAI's calculator for each generated image whose size and quality
	// the reply states. An "auto" value says nothing about the result.
	expected := int64(0)
	for _, c := range calls {
		if c.Status != "completed" {
			continue
		}
		if cells, known := imageCells[t.family][c.Quality]; known && c.Quality != "auto" {
			if w, h, err := parseImageSize(c.Size); err == nil && w > 0 {
				expected += imageOutputTokens(cells, w, h)
			}
		}
	}
	expected += int64(partials) * partialImageTokens
	if reported, ok := parseImageUsage(object(response["tool_usage"])["image_gen"]); ok {
		// Cached image inputs are invisible in the report, so every input is
		// billed at the uncached rate. GPT Image 2 and 2.5 bill no text output.
		return reported.InputTokens, reported.ImageInputTokens, max(reported.ImageOutputTokens, expected)
	}
	// Without a report, every call that started is billed at its worst case:
	// failed and moderated calls may still be charged upstream.
	for _, c := range calls {
		quality := c.Quality
		if _, known := imageCells[t.family][quality]; !known {
			quality = "auto"
		}
		w, h, err := parseImageSize(c.Size)
		if err != nil {
			w, h = 0, 0
		}
		output += t.perCall(quality, w, h)
		text += int64(len(c.RevisedPrompt)) + imageTextAllowance
		if c.Action != "generate" {
			image += imageInputTokenBound
		}
	}
	return text, image, output
}

// imageDataKeys name the base64 image fields of Images and Responses replies.
var imageDataKeys = map[string]bool{"result": true, "b64_json": true, "partial_image_b64": true}

// compactImageJSON copies a JSON reply with every long base64 image replaced
// by a short note, so a reply carrying images is parsed and stored cheaply.
// Malformed input is copied unchanged and fails the later parse.
func compactImageJSON(data []byte) []byte {
	out := make([]byte, 0, min(len(data), 64<<10))
	key, afterColon := "", false
	for i := 0; i < len(data); {
		c := data[i]
		switch {
		case c == '"':
			end := i + 1
			for end < len(data) && data[end] != '"' {
				if data[end] == '\\' {
					end++
				}
				end++
			}
			if end >= len(data) {
				return append(out, data[i:]...)
			}
			token := data[i : end+1]
			next := end + 1
			for next < len(data) && space(data[next]) {
				next++
			}
			switch {
			case !afterColon && next < len(data) && data[next] == ':':
				key = string(token[1 : len(token)-1])
				out = append(out, token...)
			case afterColon && imageDataKeys[key] && len(token) > 1024:
				out = append(out, fmt.Sprintf(`"[image omitted: %d bytes]"`, len(token)-2)...)
			default:
				out = append(out, token...)
			}
			afterColon = false
			i = end + 1
		case c == ':':
			afterColon = true
			out = append(out, c)
			i++
		default:
			if !space(c) {
				afterColon = false
			}
			out = append(out, c)
			i++
		}
	}
	return out
}
