package relay

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"relaypine.com/gateway/internal/domain"
)

// The Images API: generations, and edits of uploaded images. Only GPT Image 2
// and 2.5 are served (see domain.ImageFamily). A request is reserved at its
// worst case before it is sent and settled on the text and image tokens the
// reply reports.
// https://developers.openai.com/api/docs/guides/image-generation
const (
	imageGenerationsPath = "/v1/images/generations"
	imageEditsPath       = "/v1/images/edits"
	// Edits carry their input images, so they get a larger local admission
	// limit than JSON requests. It still bounds the memory of one call.
	maxImageRequestBytes = 24 << 20
	maxImageInputs       = 16
	maxImageMaskBytes    = 4 << 20
	maxImagePromptRunes  = 32000
	maxImagesPerRequest  = 10
	maxImageTextField    = 128 << 10
	// Each streamed partial image adds 100 image output tokens.
	partialImageTokens = 100
	// Input image token counts are undocumented for GPT Image 2 and 2.5, and 2
	// always reads inputs at high fidelity. Reserve more than the largest
	// documented output count for any size (23,719) per input image or mask.
	imageInputTokenBound = 32768
	// Text input is at most one token per prompt byte. The allowance covers the
	// instructions OpenAI adds around a prompt and mask.
	imageTextAllowance = 2048
	// Output size rules for GPT Image 2 and 2.5.
	minOutputPixels = 655360
	maxOutputPixels = 8294400
	maxOutputEdge   = 3840
)

// imageCells is the cell count along the long side of an image for each
// quality, from OpenAI's token calculator. "auto" may resolve to any quality.
var imageCells = map[string]map[string]int{
	domain.ImageFamily2:  {"low": 16, "medium": 48, "high": 96, "auto": 96},
	domain.ImageFamily25: {"low": 16, "medium": 24, "high": 48, "xhigh": 64, "max": 96, "auto": 96},
}

// imageQualities lists each family's qualities in order, for messages.
var imageQualities = map[string][]string{
	domain.ImageFamily2:  {"low", "medium", "high", "auto"},
	domain.ImageFamily25: {"low", "medium", "high", "xhigh", "max", "auto"},
}

// imageMargin scales the calculator's count for the reservation, in percent.
// The calculator reproduces GPT Image 2's published per-image prices exactly;
// the GPT Image 2.5 model pages say it does not estimate 2.5.
var imageMargin = map[string]int64{domain.ImageFamily2: 125, domain.ImageFamily25: 200}

var imageSizePattern = regexp.MustCompile(`^([1-9][0-9]{1,3})x([1-9][0-9]{1,3})$`)

// imageRequest is one validated Images API call.
type imageRequest struct {
	family          string
	n, partials     int
	width, height   int
	quality         string
	stream          bool
	promptBytes     int
	inputImages     int
	multipart       bool
	files           []imageFile
	inspectionNotes map[string]any
}

type imageFile struct {
	field, contentType string
	data               []byte
}

// imageOutputTokens is OpenAI's calculator: the long side gets cells, the
// short side cells scaled by the aspect ratio (rounded half to even), and
// tokens = ceil(cells_w * cells_h * (2,000,000 + W*H) / 4,000,000).
func imageOutputTokens(cells, width, height int) int64 {
	long, short := int64(max(width, height)), int64(min(width, height))
	q, r := int64(cells)*short/long, int64(cells)*short%long
	if 2*r > long || 2*r == long && q%2 == 1 {
		q++
	}
	return (int64(cells)*q*(2_000_000+int64(width)*int64(height)) + 3_999_999) / 4_000_000
}

// maxImageOutputTokens covers every valid size: a square at the pixel limit
// (2880x2880) has the most cells and the largest pixel term.
func maxImageOutputTokens(cells int) int64 {
	return (int64(cells)*int64(cells)*(2_000_000+maxOutputPixels) + 3_999_999) / 4_000_000
}

// outputTokens bounds every image output token of the request, including the
// partial images of a stream.
func (q *imageRequest) outputTokens() int64 {
	cells := imageCells[q.family][q.quality]
	per := maxImageOutputTokens(cells)
	if q.width > 0 {
		per = imageOutputTokens(cells, q.width, q.height)
	}
	per = (per*imageMargin[q.family] + 99) / 100
	per += int64(q.partials) * partialImageTokens
	return per * int64(q.n)
}

// inputTokens bounds text and image input tokens.
func (q *imageRequest) inputTokens() (text, image int64) {
	return int64(q.promptBytes) + imageTextAllowance, int64(q.inputImages) * imageInputTokenBound
}

func parseImageSize(s string) (int, int, error) {
	if s == "auto" {
		return 0, 0, nil
	}
	match := imageSizePattern.FindStringSubmatch(s)
	if match == nil {
		return 0, 0, errors.New("size must be auto or WIDTHxHEIGHT")
	}
	w, _ := strconv.Atoi(match[1])
	h, _ := strconv.Atoi(match[2])
	switch {
	case w%16 != 0 || h%16 != 0:
		return 0, 0, errors.New("image width and height must be multiples of 16")
	case max(w, h) > maxOutputEdge:
		return 0, 0, errors.New("image edges must be at most 3840 pixels")
	case w*h < minOutputPixels || w*h > maxOutputPixels:
		return 0, 0, errors.New("image size must be between 655,360 and 8,294,400 pixels")
	case max(w, h) > 3*min(w, h):
		return 0, 0, errors.New("image aspect ratio must be between 1:3 and 3:1")
	}
	return w, h, nil
}

func jsonString(raw json.RawMessage) (string, bool) {
	var s string
	if len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	return s, true
}

func oneOf(raw json.RawMessage, name string, values ...string) (string, error) {
	s, ok := jsonString(raw)
	if ok {
		for _, v := range values {
			if s == v {
				return s, nil
			}
		}
	}
	return "", fmt.Errorf("%s must be one of %s", name, strings.Join(values, ", "))
}

// validate checks every field against an allowlist: an unknown parameter
// could change what the request costs.
func (q *imageRequest) validate(m map[string]json.RawMessage, edits bool) error {
	for k, v := range m {
		if string(v) == "null" {
			delete(m, k)
		}
	}
	for k := range m {
		switch k {
		case "model", "prompt", "n", "size", "quality", "background", "output_format", "output_compression", "moderation", "stream", "partial_images", "safety_identifier", "user":
		case "images", "mask":
			if !edits {
				return errors.New(k + " is supported only by image edits")
			}
		case "response_format":
			return errors.New("response_format is not supported; GPT Image models always return base64 images")
		case "input_fidelity":
			return errors.New("input_fidelity is not supported by GPT Image 2 models")
		default:
			return errors.New("unsupported image parameter: " + k)
		}
	}
	prompt, ok := jsonString(m["prompt"])
	if !ok || strings.TrimSpace(prompt) == "" {
		return errors.New("prompt is required")
	}
	if utf8.RuneCountInString(prompt) > maxImagePromptRunes {
		return errors.New("prompt exceeds 32,000 characters")
	}
	q.promptBytes = len(prompt)
	q.n = 1
	if raw, ok := m["n"]; ok {
		n, valid := integer(raw)
		if !valid || n < 1 || n > maxImagesPerRequest {
			return errors.New("n must be between 1 and 10")
		}
		q.n = int(n)
	}
	if raw, ok := m["size"]; ok {
		s, valid := jsonString(raw)
		if !valid {
			return errors.New("size must be auto or WIDTHxHEIGHT")
		}
		var err error
		if q.width, q.height, err = parseImageSize(s); err != nil {
			return err
		}
	}
	q.quality = "auto"
	if raw, ok := m["quality"]; ok {
		s, err := oneOf(raw, "quality", imageQualities[q.family]...)
		if err != nil {
			return err
		}
		q.quality = s
	}
	background, format := "auto", "png"
	var err error
	if raw, ok := m["background"]; ok {
		if background, err = oneOf(raw, "background", "transparent", "opaque", "auto"); err != nil {
			return err
		}
	}
	if raw, ok := m["output_format"]; ok {
		if format, err = oneOf(raw, "output_format", "png", "jpeg", "webp"); err != nil {
			return err
		}
	}
	if background == "transparent" && format == "jpeg" {
		return errors.New("transparent backgrounds need png or webp output")
	}
	if raw, ok := m["output_compression"]; ok {
		if n, valid := integer(raw); !valid || n > 100 {
			return errors.New("output_compression must be between 0 and 100")
		}
	}
	if raw, ok := m["moderation"]; ok {
		if _, err = oneOf(raw, "moderation", "low", "auto"); err != nil {
			return err
		}
	}
	if raw, ok := m["stream"]; ok {
		if string(raw) != "true" && string(raw) != "false" {
			return errors.New("stream must be a boolean")
		}
		q.stream = string(raw) == "true"
	}
	if raw, ok := m["partial_images"]; ok {
		n, valid := integer(raw)
		if !valid || n > 3 {
			return errors.New("partial_images must be between 0 and 3")
		}
		q.partials = int(n)
	}
	if q.partials > 0 && !q.stream {
		return errors.New("partial_images requires stream")
	}
	if q.stream && q.n > 1 {
		return errors.New("a streamed request generates one image; set n to 1")
	}
	for field, limit := range map[string]int{"safety_identifier": 128, "user": 256} {
		if raw, ok := m[field]; ok {
			if s, valid := jsonString(raw); !valid || len(s) > limit {
				return fmt.Errorf("%s must be a string of at most %d bytes", field, limit)
			}
		}
	}
	return nil
}

// sniffImage returns the MIME type of PNG, JPEG or WebP data.
func sniffImage(data []byte) string {
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return "image/png"
	case bytes.HasPrefix(data, []byte{0xff, 0xd8, 0xff}):
		return "image/jpeg"
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return "image/webp"
	}
	return ""
}

var dataURLPrefix = regexp.MustCompile(`^data:(image/(?:png|jpeg|webp));base64,`)

// imageDataURL checks a data URL by its declared and actual image type. Only
// inline data is accepted: a file ID could name another customer's upload in
// the shared upstream account, and a remote URL has no size bound.
func imageDataURL(raw json.RawMessage, pngOnly bool) (int, error) {
	entry := object(raw)
	if entry == nil {
		return 0, errors.New("each image must be an object with image_url")
	}
	for key := range entry {
		if key == "file_id" {
			return 0, errors.New("file_id references are not supported; send the image data")
		}
		if key != "image_url" {
			return 0, errors.New("unsupported image field: " + key)
		}
	}
	url, ok := jsonString(entry["image_url"])
	if !ok {
		return 0, errors.New("image_url must be a string")
	}
	match := dataURLPrefix.FindStringSubmatch(url)
	if match == nil {
		return 0, errors.New("image_url must be a base64 PNG, JPEG or WebP data URL; remote image URLs are not supported")
	}
	data := url[len(match[0]):]
	head := make([]byte, 12)
	if len(data) < 16 {
		return 0, errors.New("image_url data is too short to be an image")
	}
	n, err := base64.StdEncoding.Decode(head, []byte(data[:16]))
	if err != nil || sniffImage(head[:n]) != match[1] {
		return 0, errors.New("image_url data does not match its image type")
	}
	if pngOnly && match[1] != "image/png" {
		return 0, errors.New("the mask must be a PNG image")
	}
	if pngOnly && base64.StdEncoding.DecodedLen(len(data)) > maxImageMaskBytes {
		return 0, errors.New("the mask must be smaller than 4 MB")
	}
	return len(url), nil
}

// readJSONEdit validates the images of a JSON edit and returns a copy of the
// body without image data for request inspection.
func (q *imageRequest) readJSONEdit(m map[string]json.RawMessage) error {
	var images []json.RawMessage
	if json.Unmarshal(m["images"], &images) != nil || len(images) == 0 || len(images) > maxImageInputs {
		return errors.New("images must list between 1 and 16 images")
	}
	notes := []string{}
	for _, image := range images {
		size, err := imageDataURL(image, false)
		if err != nil {
			return err
		}
		notes = append(notes, fmt.Sprintf("[image data omitted: %d bytes]", size))
	}
	q.inputImages = len(images)
	q.inspectionNotes = map[string]any{"images": notes}
	if raw, ok := m["mask"]; ok {
		size, err := imageDataURL(raw, true)
		if err != nil {
			return err
		}
		q.inputImages++
		q.inspectionNotes["mask"] = fmt.Sprintf("[mask data omitted: %d bytes]", size)
	}
	return nil
}

// readMultipart parses a multipart edit into typed fields and image files.
func (q *imageRequest) readMultipart(body []byte, boundary string) (map[string]json.RawMessage, error) {
	if boundary == "" {
		return nil, errors.New("multipart boundary is missing")
	}
	reader := multipart.NewReader(bytes.NewReader(body), boundary)
	fields := map[string]json.RawMessage{}
	mask, images := false, 0
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, errors.New("invalid multipart body")
		}
		name := part.FormName()
		if part.FileName() == "" {
			value, err := io.ReadAll(io.LimitReader(part, maxImageTextField+1))
			if err != nil || len(value) > maxImageTextField || !utf8.Valid(value) {
				return nil, errors.New("invalid form field: " + name)
			}
			if _, exists := fields[name]; exists {
				return nil, errors.New("duplicate form field: " + name)
			}
			switch name {
			case "n", "output_compression", "partial_images":
				if _, err := strconv.ParseUint(string(value), 10, 31); err != nil {
					return nil, errors.New(name + " must be a whole number")
				}
				fields[name] = json.RawMessage(value)
			case "stream":
				if string(value) != "true" && string(value) != "false" {
					return nil, errors.New("stream must be true or false")
				}
				fields[name] = json.RawMessage(value)
			default:
				fields[name], _ = json.Marshal(string(value))
			}
			continue
		}
		data, err := io.ReadAll(io.LimitReader(part, maxImageRequestBytes+1))
		if err != nil || len(data) > maxImageRequestBytes {
			return nil, errors.New("invalid image upload")
		}
		kind := sniffImage(data)
		switch name {
		case "image", "image[]":
			if kind == "" {
				return nil, errors.New("images must be PNG, JPEG or WebP files")
			}
			if images == maxImageInputs {
				return nil, errors.New("send at most 16 images")
			}
			images++
		case "mask":
			if mask {
				return nil, errors.New("send one mask")
			}
			if kind != "image/png" || len(data) > maxImageMaskBytes {
				return nil, errors.New("the mask must be a PNG smaller than 4 MB")
			}
			mask = true
		default:
			return nil, errors.New("unsupported file field: " + name)
		}
		q.files = append(q.files, imageFile{field: name, contentType: kind, data: data})
	}
	q.inputImages = len(q.files)
	if images == 0 {
		return nil, errors.New("send at least one image")
	}
	files := []string{}
	for _, f := range q.files {
		files = append(files, fmt.Sprintf("%s: %s, %d bytes (omitted)", f.field, f.contentType, len(f.data)))
	}
	q.inspectionNotes = map[string]any{"files": files}
	return fields, nil
}

// multipartBody re-encodes a validated edit: typed fields, then the image
// files with neutral file names, so nothing unchecked reaches the upstream.
func (q *imageRequest) multipartBody(fields map[string]json.RawMessage) ([]byte, string, error) {
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool { return names[i] == "model" || names[j] != "model" && names[i] < names[j] })
	for _, name := range names {
		value := string(fields[name])
		if s, ok := jsonString(fields[name]); ok {
			value = s
		}
		if err := writer.WriteField(name, value); err != nil {
			return nil, "", err
		}
	}
	for i, f := range q.files {
		header := textproto.MIMEHeader{}
		header.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename="image-%d.%s"`, f.field, i+1, strings.TrimPrefix(f.contentType, "image/")))
		header.Set("Content-Type", f.contentType)
		part, err := writer.CreatePart(header)
		if err != nil {
			return nil, "", err
		}
		if _, err = part.Write(f.data); err != nil {
			return nil, "", err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), writer.FormDataContentType(), nil
}

// prepareImage validates an Images API request, bounds its tokens and builds
// the upstream body.
func (h *Handler) prepareImage(r *http.Request, p *plan, models []domain.Model) error {
	if enc := r.Header.Get("Content-Encoding"); enc != "" && enc != "identity" {
		return errors.New("compressed request bodies are not supported")
	}
	limit := maxRequestBytes
	if p.path == imageEditsPath {
		limit = maxImageRequestBytes
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, int64(limit)+1))
	if err != nil {
		return errors.New("unable to read request")
	}
	if len(body) > limit {
		return fmt.Errorf("request body exceeds %d MiB", limit>>20)
	}
	p.original = body
	q := &imageRequest{}
	mediaType, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	var fields map[string]json.RawMessage
	switch {
	case mediaType == "multipart/form-data" && p.path == imageEditsPath:
		q.multipart = true
		if fields, err = q.readMultipart(body, params["boundary"]); err != nil {
			return err
		}
	case mediaType == "application/json" || mediaType == "":
		if fields = object(body); fields == nil {
			return errors.New("a JSON object is required")
		}
	default:
		return errors.New("send a JSON body, or multipart form data for image edits")
	}
	id, _ := jsonString(fields["model"])
	for _, model := range models {
		if model.ID == id && model.Enabled && model.Provider == p.provider && allowed(p.key.Models, id) {
			p.model = model
			break
		}
	}
	if p.model.ID == "" {
		return errors.New("model is unavailable or not allowed for this key")
	}
	if !p.model.IsImage() {
		return errors.New("this model does not generate images; use an image model such as gpt-image-2")
	}
	if q.family = domain.ImageFamily(p.model.UpstreamModel); q.family == "" {
		return errors.New("this image model is not supported; use GPT Image 2 or GPT Image 2.5")
	}
	if !validImageAccounting(p.model) {
		return errors.New("model has no complete image price configuration")
	}
	if err = q.validate(fields, p.path == imageEditsPath); err != nil {
		return err
	}
	if p.path == imageEditsPath && !q.multipart {
		if err = q.readJSONEdit(fields); err != nil {
			return err
		}
	}
	text, images := q.inputTokens()
	p.imageInput = images
	p.input = text + images
	p.output = q.outputTokens()
	p.tpm = p.input + p.output
	p.stream = q.stream
	p.image = q
	if q.inspectionNotes != nil {
		// Taken before model aliasing, so it shows the client's own model ID.
		if p.original, err = q.inspectionCopy(body, fields); err != nil {
			return err
		}
		p.originalBytes = len(body)
	}
	fields["model"], _ = json.Marshal(p.model.UpstreamModel)
	p.contentType = "application/json"
	if q.multipart {
		p.body, p.contentType, err = q.multipartBody(fields)
	} else {
		p.body, err = json.Marshal(fields)
	}
	return err
}

// inspectionCopy is what request inspection stores for an edit: every field
// as the client sent it, with image data replaced by its size.
func (q *imageRequest) inspectionCopy(body []byte, fields map[string]json.RawMessage) ([]byte, error) {
	source := fields
	if !q.multipart {
		source = object(body)
	}
	copied := map[string]any{}
	for k, v := range source {
		copied[k] = v
	}
	for k, v := range q.inspectionNotes {
		copied[k] = v
	}
	return json.Marshal(copied)
}
