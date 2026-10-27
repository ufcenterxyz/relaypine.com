package relay

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
)

func fixtureImage(t *testing.T, format string, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 13), uint8(y * 29), 120, 255})
		}
	}
	var b bytes.Buffer
	var err error
	if format == "jpeg" {
		err = jpeg.Encode(&b, img, nil)
	} else {
		err = png.Encode(&b, img)
	}
	if err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func imageBlock(format string, data []byte) map[string]any {
	return map[string]any{"type": "image", "source": map[string]any{
		"type": "base64", "media_type": "image/" + format, "data": base64.StdEncoding.EncodeToString(data),
	}}
}

func imageMessages(blocks ...any) json.RawMessage {
	raw, _ := json.Marshal([]any{map[string]any{"role": "user", "content": blocks}})
	return raw
}

func TestInlineImagesSummaries(t *testing.T) {
	for _, path := range []string{"/v1/messages", "/v1/messages/count_tokens"} {
		t.Run(path, func(t *testing.T) {
			p := fixtureImage(t, "png", 31, 17)
			j := fixtureImage(t, "jpeg", 19, 23)
			m := map[string]json.RawMessage{"messages": imageMessages(
				map[string]any{"type": "text", "text": "Describe these images"},
				imageBlock("png", p), map[string]any{"type": "tool_result", "tool_use_id": "call_1", "content": []any{imageBlock("jpeg", j)}},
			)}
			before := string(m["messages"])
			summary, err := validatePayloadWithImages(m, path)
			if err != nil {
				t.Fatal(err)
			}
			if len(summary.Images) != 2 || summary.Images[0] != (imageDimensions{31, 17}) || summary.Images[1] != (imageDimensions{19, 23}) {
				t.Fatalf("unexpected dimensions: %+v", summary.Images)
			}
			want := int64(base64.StdEncoding.EncodedLen(len(p)) + base64.StdEncoding.EncodedLen(len(j)))
			if summary.Base64Bytes != want || string(m["messages"]) != before {
				t.Fatal("wrong image accounting or mutated original messages")
			}
			safe, _ := json.Marshal(summary)
			if bytes.Contains(safe, []byte(base64.StdEncoding.EncodeToString(p))) || bytes.Contains(safe, []byte("source")) || bytes.Contains(safe, []byte("data")) {
				t.Fatal("summary retained image payload")
			}
		})
	}
}

func TestImagePositionsAndOpaqueUserData(t *testing.T) {
	img := imageBlock("png", fixtureImage(t, "png", 2, 3))
	text, _ := json.Marshal(img)
	for _, value := range []any{
		map[string]any{"type": "text", "text": string(text)},
		map[string]any{"type": "tool_use", "id": "call_1", "name": "lookup", "input": img},
		map[string]any{"type": "function_call", "arguments": img},
		map[string]any{"type": "tool_result", "tool_use_id": "call_1", "content": string(text)},
	} {
		summary, err := inspectAnthropicImages(imageMessages(value))
		if err != nil || len(summary.Images) != 0 || summary.Base64Bytes != 0 {
			t.Fatalf("opaque text/tool data treated as an image: %v", err)
		}
	}
	for name, m := range map[string]map[string]json.RawMessage{
		"system":             {"system": mustImageJSON([]any{img})},
		"input":              {"input": mustImageJSON([]any{img})},
		"assistant":          {"messages": mustImageJSON([]any{map[string]any{"role": "assistant", "content": []any{img}}})},
		"unknown role":       {"messages": mustImageJSON([]any{map[string]any{"role": "other", "content": []any{img}}})},
		"missing role":       {"messages": mustImageJSON([]any{map[string]any{"content": []any{img}}})},
		"unknown wrapper":    {"messages": imageMessages(map[string]any{"type": "text", "extra": img})},
		"nonarray content":   {"messages": mustImageJSON([]any{map[string]any{"role": "user", "content": img}})},
		"nested tool result": {"messages": imageMessages(map[string]any{"type": "tool_result", "content": []any{map[string]any{"type": "tool_result", "content": []any{img}}}})},
	} {
		t.Run(name, func(t *testing.T) {
			if summary, err := validatePayloadWithImages(m, "/v1/messages"); err == nil || len(summary.Images) != 0 || summary.Base64Bytes != 0 {
				t.Fatal("unsupported image position accepted or partial metadata returned")
			}
		})
	}
	for _, path := range []string{"/v1/chat/completions", "/v1/responses"} {
		if _, err := validatePayloadWithImages(map[string]json.RawMessage{"messages": imageMessages(img)}, path); err == nil {
			t.Fatal("accepted an OpenAI image")
		}
	}
}

func TestImageSourceAndEncodingRejections(t *testing.T) {
	p := fixtureImage(t, "png", 2, 3)
	valid := base64.StdEncoding.EncodeToString(p)
	cases := map[string]func(map[string]any){
		"url": func(b map[string]any) {
			b["source"] = map[string]any{"type": "url", "url": "https://example.invalid/secret"}
		},
		"file":                 func(b map[string]any) { b["source"] = map[string]any{"type": "file", "file_id": "private"} },
		"unknown source":       func(b map[string]any) { b["source"].(map[string]any)["extra"] = "payload-sentinel" },
		"unknown block":        func(b map[string]any) { b["extra"] = "payload-sentinel" },
		"missing source":       func(b map[string]any) { delete(b, "source") },
		"null source":          func(b map[string]any) { b["source"] = nil },
		"GIF":                  func(b map[string]any) { b["source"].(map[string]any)["media_type"] = "image/gif" },
		"WebP":                 func(b map[string]any) { b["source"].(map[string]any)["media_type"] = "image/webp" },
		"MIME mismatch":        func(b map[string]any) { b["source"].(map[string]any)["media_type"] = "image/jpeg" },
		"source type":          func(b map[string]any) { b["source"].(map[string]any)["type"] = "payload-sentinel" },
		"empty":                func(b map[string]any) { b["source"].(map[string]any)["data"] = "" },
		"null data":            func(b map[string]any) { b["source"].(map[string]any)["data"] = nil },
		"number data":          func(b map[string]any) { b["source"].(map[string]any)["data"] = 42 },
		"CRLF":                 func(b map[string]any) { b["source"].(map[string]any)["data"] = valid[:4] + "\r\n\r\n" + valid[4:] },
		"space":                func(b map[string]any) { b["source"].(map[string]any)["data"] = valid[:4] + "    " + valid[4:] },
		"URL alphabet":         func(b map[string]any) { b["source"].(map[string]any)["data"] = "____" },
		"data URL":             func(b map[string]any) { b["source"].(map[string]any)["data"] = "data:image/png;base64," + valid },
		"invalid padding bits": func(b map[string]any) { b["source"].(map[string]any)["data"] = "Zh==" },
		"missing padding":      func(b map[string]any) { b["source"].(map[string]any)["data"] = "Zg" },
		"padding in middle":    func(b map[string]any) { b["source"].(map[string]any)["data"] = "Zg==Zg==" },
		"one hour cache":       func(b map[string]any) { b["cache_control"] = map[string]any{"type": "ephemeral", "ttl": "1h"} },
	}
	for name, alter := range cases {
		t.Run(name, func(t *testing.T) {
			block := imageBlock("png", p)
			alter(block)
			summary, err := inspectAnthropicImages(imageMessages(block))
			if err == nil || len(summary.Images) != 0 || summary.Base64Bytes != 0 {
				t.Fatal("invalid image accepted or partial metadata returned")
			}
			if strings.Contains(err.Error(), "payload-sentinel") || strings.Contains(err.Error(), valid) || strings.Contains(err.Error(), "example.invalid") {
				t.Fatal("image error reflects supplied data")
			}
		})
	}
	for _, raw := range []string{
		`[{"role":"user","content":[{"type":"image","type":"text","source":{}}]}]`,
		`[{"role":"user","content":[{"type":"image","source":{"type":"base64","type":"url","media_type":"image/png","data":"abcd"}}]}]`,
		`[{"role":"user","content":[{"type":"image","source":{},"source":{}}]}]`,
	} {
		if _, err := inspectAnthropicImages(json.RawMessage(raw)); err == nil {
			t.Fatal("accepted ambiguous duplicate fields")
		}
	}
}

func TestStaticCompleteImageAndDimensionLimits(t *testing.T) {
	p := fixtureImage(t, "png", 3, 2)
	j := fixtureImage(t, "jpeg", 3, 2)
	animated := append([]byte{}, p[:33]...)
	animated = append(animated, imagePNGChunk("acTL", make([]byte, 8))...)
	animated = append(animated, p[33:]...)
	corrupt := append([]byte{}, p...)
	corrupt[len(corrupt)-5] ^= 1
	for name, data := range map[string][]byte{
		"header only": p[:33], "truncated PNG": p[:len(p)-1], "PNG trailer": append(append([]byte{}, p...), 0),
		"animated PNG": animated, "corrupt PNG": corrupt, "PNG zero dimensions": imagePNGDimensions(p, 0, 2),
		"PNG axis too large": imagePNGDimensions(p, 8001, 1), "PNG too many pixels": imagePNGDimensions(p, 4001, 4000),
		"PNG allocation bomb": imagePNGDimensions(p, 0xffffffff, 0xffffffff),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := inspectAnthropicImages(imageMessages(imageBlock("png", data))); err == nil {
				t.Fatal("invalid PNG accepted")
			}
		})
	}
	for name, data := range map[string][]byte{
		"truncated JPEG": j[:len(j)-1], "JPEG trailer": append(append([]byte{}, j...), 0),
		"concatenated JPEG": append(append([]byte{}, j...), j...),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := inspectAnthropicImages(imageMessages(imageBlock("jpeg", data))); err == nil {
				t.Fatal("invalid JPEG accepted")
			}
		})
	}
}

func TestImageAggregateAndBodyLimits(t *testing.T) {
	p := fixtureImage(t, "png", 1, 1)
	blocks := make([]any, maxInputImages)
	for i := range blocks {
		blocks[i] = imageBlock("png", p)
	}
	if s, err := inspectAnthropicImages(imageMessages(blocks...)); err != nil || len(s.Images) != maxInputImages {
		t.Fatalf("20 small images rejected: %v", err)
	}
	if s, err := inspectAnthropicImages(imageMessages(append(blocks, imageBlock("png", p))...)); err == nil || len(s.Images) != 0 {
		t.Fatal("more than 20 images accepted")
	}
	block, err := imageJSONObject(mustImageJSON(imageBlock("png", p)))
	if err != nil {
		t.Fatal(err)
	}
	summary := imageInputSummary{Images: []imageDimensions{{4000, 4000}, {4000, 4000}}}
	if err := summary.addImage(block); err == nil || len(summary.Images) != 2 || summary.Base64Bytes != 0 {
		t.Fatal("aggregate pixel bound was not enforced before adding image")
	}
	if _, err := inspectAnthropicImages(json.RawMessage(strings.Repeat(" ", maxRequestBytes+1))); err == nil {
		t.Fatal("oversized message bytes accepted")
	}
	summary = imageInputSummary{Base64Bytes: maxRequestBytes}
	if err := summary.addImage(block); err == nil || len(summary.Images) != 0 {
		t.Fatal("aggregate image data bound was not enforced")
	}
	// Escaped JSON must count only the validated logical data bytes. The raw
	// escape overhead remains in the caller's conservative text estimate.
	raw := imageMessages(imageBlock("png", p))
	escaped := bytes.Replace(raw, []byte("iVBOR"), []byte(`\u0069VBOR`), 1)
	if bytes.Equal(raw, escaped) {
		t.Fatal("fixture did not contain expected PNG prefix")
	}
	s, err := inspectAnthropicImages(escaped)
	if err != nil || s.Base64Bytes != int64(base64.StdEncoding.EncodedLen(len(p))) {
		t.Fatalf("escaped source bytes counted incorrectly: %v", err)
	}
}

func mustImageJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func imagePNGChunk(kind string, data []byte) []byte {
	b := make([]byte, 12+len(data))
	binary.BigEndian.PutUint32(b[:4], uint32(len(data)))
	copy(b[4:8], kind)
	copy(b[8:], data)
	binary.BigEndian.PutUint32(b[len(b)-4:], crc32.ChecksumIEEE(b[4:len(b)-4]))
	return b
}

func imagePNGDimensions(data []byte, width, height uint32) []byte {
	b := append([]byte{}, data...)
	binary.BigEndian.PutUint32(b[16:20], width)
	binary.BigEndian.PutUint32(b[20:24], height)
	binary.BigEndian.PutUint32(b[29:33], crc32.ChecksumIEEE(b[12:29]))
	return b
}
