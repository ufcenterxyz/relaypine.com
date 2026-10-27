package relay

import "testing"

func TestImageInputBoundPreservesTextAndSeparatesEncoding(t *testing.T) {
	text, err := estimatedInputTokens(1000, imageInputSummary{})
	if err != nil || text != 6096 {
		t.Fatalf("text estimate %d, error %v", text, err)
	}
	images := imageInputSummary{Base64Bytes: 700000, Images: []imageDimensions{{Width: 1568, Height: 1018}}}
	got, err := estimatedInputTokens(701000, images)
	want := int64(6096 + 2*56*37 + 256)
	if err != nil || got != want {
		t.Fatalf("image estimate %d want %d, error %v", got, want, err)
	}
	// Larger encoding of the same dimensions changes only the removed payload.
	images.Base64Bytes += 500000
	again, err := estimatedInputTokens(1201000, images)
	if err != nil || again != got {
		t.Fatal("encoded image size changed its visual token reservation")
	}
	// Escapes that occur in the original JSON stay conservatively accounted for.
	escaped, err := estimatedInputTokens(1201100, images)
	if err != nil || escaped != got+200 {
		t.Fatal("raw JSON overhead was incorrectly discounted")
	}
}

func TestImageInputBoundRejectsImpossibleSummary(t *testing.T) {
	for _, summary := range []imageInputSummary{
		{Base64Bytes: -1}, {Base64Bytes: 101},
		{Images: []imageDimensions{{Width: 0, Height: 1}}},
		{Images: []imageDimensions{{Width: 1, Height: 8001}}},
	} {
		if _, err := estimatedInputTokens(100, summary); err == nil {
			t.Fatal("invalid image summary accepted")
		}
	}
}
