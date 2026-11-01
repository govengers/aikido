package openrouter

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/govengers/aikido/llm"
)

// TestBuildAPIMessages_ImageURL pins that an image the model generated (Data
// only, no URL) goes back inline as a data URI: an empty url would make every
// later request of that conversation fail.
func TestBuildAPIMessages_ImageURL(t *testing.T) {
	t.Parallel()
	png := []byte("\x89PNG\r\n\x1a\nrest")
	testCases := []struct {
		name    string
		image   llm.ImagePart
		wantURL string
		wantErr error
	}{
		{name: "url kept", image: llm.ImagePart{URL: "https://x/y.png", Data: png}, wantURL: "https://x/y.png"},
		{name: "data inlined", image: llm.ImagePart{ContentType: "image/webp", Data: []byte("AB")}, wantURL: "data:image/webp;base64,QUI="},
		{name: "content type sniffed", image: llm.ImagePart{Data: png}, wantURL: "data:image/png;base64,iVBORw0KGgpyZXN0"},
		{name: "neither", image: llm.ImagePart{ContentType: "image/png"}, wantErr: llm.ErrInvalidRequest},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			messages, err := buildAPIMessages([]llm.Message{{Role: llm.RoleAssistant, Images: []llm.ImagePart{testCase.image}}})
			if testCase.wantErr != nil {
				if !errors.Is(err, testCase.wantErr) {
					t.Fatalf("err = %v, want %v", err, testCase.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("buildAPIMessages: %v", err)
			}
			var parts []apiContentPart
			if err := json.Unmarshal(messages[0].Content, &parts); err != nil {
				t.Fatalf("decode content %s: %v", messages[0].Content, err)
			}
			if len(parts) != 1 || parts[0].ImageURL == nil || parts[0].ImageURL.URL != testCase.wantURL {
				t.Errorf("content = %s, want one image_url %q", messages[0].Content, testCase.wantURL)
			}
		})
	}
}
