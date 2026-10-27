package relay

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
)

// These are local admission limits, deliberately narrower than the provider's.
// The HTTP handler still enforces its existing 2 MiB limit for the whole body.
const (
	maxInputImages            = 20
	maxImageDimension         = 8000
	maxImagePixels      int64 = 16_000_000
	maxTotalImagePixels       = 32_000_000
)

type imageDimensions struct{ Width, Height int }

// Only validated image metadata escapes the parser. Base64Bytes counts data at
// image source positions, never similar strings in text or tool arguments.
type imageInputSummary struct {
	Images      []imageDimensions
	Base64Bytes int64
}

func inspectAnthropicImages(raw json.RawMessage) (imageInputSummary, error) {
	var summary imageInputSummary
	if len(raw) == 0 {
		return summary, nil
	}
	if len(raw) > maxRequestBytes {
		return summary, errors.New("request body exceeds 2 MiB")
	}
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
		if err := summary.inspectContent(m["content"], str(m["role"]) == "user", true); err != nil {
			return imageInputSummary{}, err
		}
	}
	return summary, nil
}

func (s *imageInputSummary) inspectContent(raw json.RawMessage, allowImages, allowToolResults bool) error {
	if len(raw) == 0 {
		return nil
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		// Strings are opaque. In particular, text containing image-shaped JSON
		// must never be decoded or subtracted from the text cost estimate.
		var value any
		if json.Unmarshal(raw, &value) != nil {
			return errors.New("invalid message content")
		}
		return validateContent(value, "content")
	}
	var blocks []json.RawMessage
	if json.Unmarshal(raw, &blocks) != nil {
		return errors.New("invalid message content")
	}
	for _, rawBlock := range blocks {
		block, err := imageJSONObject(rawBlock)
		if err != nil {
			return errors.New("invalid content block")
		}
		switch str(block["type"]) {
		case "image":
			if !allowImages {
				return errors.New("images are supported only in user message content")
			}
			if err := s.addImage(block); err != nil {
				return err
			}
		case "tool_result":
			if !allowToolResults {
				return errors.New("nested tool result content is not supported")
			}
			if err := validateImageContainer(block, "content"); err != nil {
				return err
			}
			if err := s.inspectContent(block["content"], allowImages, false); err != nil {
				return err
			}
		default:
			if err := validateImageContainer(block, ""); err != nil {
				return err
			}
		}
	}
	return nil
}

// Preserve all existing capability/cache validation around the protocol's
// image positions. The original validator treats input/arguments as opaque.
func validateImageContainer(m map[string]json.RawMessage, omit string) error {
	v := make(map[string]any, len(m))
	for k, raw := range m {
		if k == omit {
			continue
		}
		var item any
		if json.Unmarshal(raw, &item) != nil {
			return errors.New("invalid content block")
		}
		v[k] = item
	}
	return validateContent(v, "")
}

func (s *imageInputSummary) addImage(block map[string]json.RawMessage) error {
	if len(s.Images) >= maxInputImages {
		return errors.New("request exceeds the 20 image limit")
	}
	for key := range block {
		if key != "type" && key != "source" && key != "cache_control" {
			return errors.New("unsupported image block field")
		}
	}
	if cache, ok := block["cache_control"]; ok {
		var v any
		if json.Unmarshal(cache, &v) != nil {
			return errors.New("invalid image cache control")
		}
		if err := validateContent(v, "cache_control"); err != nil {
			return err
		}
	}
	source, err := imageJSONObject(block["source"])
	if err != nil || len(source) != 3 {
		return errors.New("image source must contain only type, media_type and data")
	}
	for key := range source {
		if key != "type" && key != "media_type" && key != "data" {
			return errors.New("unsupported image source field")
		}
	}
	if str(source["type"]) != "base64" {
		return errors.New("only inline base64 image sources are supported")
	}
	mime := str(source["media_type"])
	if mime != "image/png" && mime != "image/jpeg" {
		return errors.New("only static PNG and JPEG images are supported")
	}
	var data string
	if json.Unmarshal(source["data"], &data) != nil || len(data) == 0 || len(data)%4 != 0 || len(data) > maxRequestBytes {
		return errors.New("invalid image base64 data")
	}
	// Strict() rejects nonzero padding bits, but Go deliberately ignores CR/LF.
	// Reject them and every other non-standard alphabet byte explicitly.
	for i := range data {
		c := data[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '+' || c == '/' || c == '=') {
			return errors.New("invalid image base64 data")
		}
	}
	if s.Base64Bytes+int64(len(data)) > maxRequestBytes {
		return errors.New("request body exceeds 2 MiB")
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(data)
	if err != nil {
		clear(decoded)
		return errors.New("invalid image base64 data")
	}
	defer clear(decoded)
	config, format, err := image.DecodeConfig(bytes.NewReader(decoded))
	if err != nil || format != "png" && format != "jpeg" || mime != "image/"+format {
		return errors.New("image media type does not match a valid PNG or JPEG")
	}
	pixels := int64(config.Width) * int64(config.Height)
	if config.Width <= 0 || config.Height <= 0 || config.Width > maxImageDimension || config.Height > maxImageDimension || pixels > maxImagePixels {
		return errors.New("image dimensions exceed the local limit")
	}
	for _, d := range s.Images {
		pixels += int64(d.Width) * int64(d.Height)
	}
	if pixels > maxTotalImagePixels {
		return errors.New("request exceeds the total image pixel limit")
	}
	if format == "png" && !staticPNG(decoded) || format == "jpeg" && !singleJPEG(decoded) {
		return errors.New("image must be one complete static PNG or JPEG")
	}
	// DecodeConfig alone accepts header-only/truncated files. Decode only after
	// dimensions and the aggregate pixel budget are checked, and retain no pixels.
	img, actual, err := image.Decode(bytes.NewReader(decoded))
	if err != nil || actual != format || img.Bounds().Dx() != config.Width || img.Bounds().Dy() != config.Height {
		return errors.New("invalid image pixel data")
	}
	s.Images = append(s.Images, imageDimensions{Width: config.Width, Height: config.Height})
	s.Base64Bytes += int64(len(data))
	return nil
}

// Reject duplicate object keys instead of letting parser differences select a
// different type/source. Opaque tool input and strings are never parsed here.
func imageJSONObject(raw json.RawMessage) (map[string]json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("invalid object")
	}
	m := map[string]json.RawMessage{}
	for d.More() {
		token, err = d.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return nil, errors.New("invalid object")
		}
		if _, duplicate := m[key]; duplicate {
			return nil, errors.New("duplicate object field")
		}
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return nil, errors.New("invalid object")
		}
		m[key] = value
	}
	if token, err = d.Token(); err != nil || token != json.Delim('}') {
		return nil, errors.New("invalid object")
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, errors.New("invalid object")
	}
	return m, nil
}

func staticPNG(data []byte) bool {
	if len(data) < 8 || !bytes.Equal(data[:8], []byte("\x89PNG\r\n\x1a\n")) {
		return false
	}
	for pos := 8; pos+12 <= len(data); {
		n := int64(binary.BigEndian.Uint32(data[pos : pos+4]))
		if n > int64(len(data)-pos-12) {
			return false
		}
		kind := string(data[pos+4 : pos+8])
		if kind == "acTL" || kind == "fcTL" || kind == "fdAT" {
			return false
		}
		pos += int(n) + 12
		if kind == "IEND" {
			return n == 0 && pos == len(data)
		}
	}
	return false
}

// JPEG is static; reject concatenated images/trailing data as well. Segment
// lengths skip metadata safely; scan bytes honor stuffing and restart markers.
func singleJPEG(data []byte) bool {
	if len(data) < 4 || data[0] != 0xff || data[1] != 0xd8 {
		return false
	}
	scan := false
	for pos := 2; pos < len(data); {
		if data[pos] != 0xff {
			if !scan {
				return false
			}
			pos++
			continue
		}
		for pos < len(data) && data[pos] == 0xff {
			pos++
		}
		if pos == len(data) {
			return false
		}
		marker := data[pos]
		pos++
		if scan && (marker == 0 || marker >= 0xd0 && marker <= 0xd7) {
			continue
		}
		if marker == 0xd9 {
			return pos == len(data)
		}
		if marker == 0xd8 || marker == 0 || pos+2 > len(data) {
			return false
		}
		n := int(binary.BigEndian.Uint16(data[pos : pos+2]))
		if n < 2 || n > len(data)-pos {
			return false
		}
		pos += n
		scan = marker == 0xda
	}
	return false
}
