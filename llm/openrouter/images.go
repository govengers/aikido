package openrouter

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/govengers/aikido/llm"
)

// Compile-time conformance for the optional images capability.
var _ llm.ImageGenerator = (*Client)(nil)

// GenerateImage renders one prompt through OpenRouter's dedicated images
// endpoint (POST /api/v1/images).
//
// Some OpenRouter image models are served only there. openai/gpt-image-2.5-flare
// and friends never appear on chat completions and are missing from a plain
// GET /models listing. Models that accept both endpoints (the Gemini image
// family) should keep using Complete with Modalities: []string{"image", "text"},
// which is the only path that also carries a conversation.
//
// An invalid request (no model or prompt, a malformed Size, Size together
// with AspectRatio or ImageSize, a reference without URL) fails with
// llm.ErrInvalidRequest before any request. Retry and error classification
// are Complete's: 429 and 5xx retry per Options.MaximumAttempts, auth and
// bad-request short-circuit. A 200 carrying a top-level error envelope that
// reads like content filtering surfaces as ErrContentFiltered; a non-200 is
// classified by status, which has no content-filter case. Zero images in a
// 200 is not an error.
func (c *Client) GenerateImage(ctx context.Context, request llm.ImageRequest) (llm.ImageResponse, error) {
	body, err := buildImagesBody(request)
	if err != nil {
		return llm.ImageResponse{}, fmt.Errorf("openrouter: images: %w", err)
	}
	start := time.Now()
	raw, err := c.postJSON(ctx, "/images", body)
	if err != nil {
		return llm.ImageResponse{}, err
	}
	response, err := parseImagesResponse(raw)
	if response.Usage != nil {
		response.Usage.SetTiming(start, time.Time{}, time.Now())
	}
	return response, err
}

// sizePattern is the endpoint's `size` shape: WIDTHxHEIGHT in pixels.
var sizePattern = regexp.MustCompile(`^[1-9][0-9]{2,3}x[1-9][0-9]{2,3}$`)

// buildImagesBody assembles the images-endpoint JSON body. Unset optional
// fields stay off the wire. ImageSize maps onto `resolution`, never `size`:
// the endpoint's `size` field wants WxH pixels (ImageRequest.Size) and rejects
// the 1K/2K/4K tiers.
func buildImagesBody(request llm.ImageRequest) ([]byte, error) {
	if err := validateImageRequest(request); err != nil {
		return nil, err
	}
	body := imagesRequest{
		Model:       request.Model,
		Prompt:      request.Prompt,
		AspectRatio: request.AspectRatio,
		Resolution:  request.ImageSize,
		Size:        request.Size,
		Quality:     request.Quality,
	}
	for index, reference := range request.References {
		if reference.URL == "" {
			return nil, fmt.Errorf("reference image %d has no URL: %w", index, llm.ErrInvalidRequest)
		}
		body.InputReferences = append(body.InputReferences, apiImagePart{
			Type:     "image_url",
			ImageURL: &apiImageURL{URL: reference.URL},
		})
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode images request: %w", err)
	}
	return encoded, nil
}

func validateImageRequest(request llm.ImageRequest) error {
	if strings.TrimSpace(request.Model) == "" {
		return fmt.Errorf("model is required: %w", llm.ErrInvalidRequest)
	}
	if strings.TrimSpace(request.Prompt) == "" {
		return fmt.Errorf("prompt is required: %w", llm.ErrInvalidRequest)
	}
	if request.Size == "" {
		return nil
	}
	if !sizePattern.MatchString(request.Size) {
		return fmt.Errorf("size %q: want WIDTHxHEIGHT in pixels: %w", request.Size, llm.ErrInvalidRequest)
	}
	if request.AspectRatio != "" || request.ImageSize != "" {
		return fmt.Errorf("size and aspect ratio/resolution are mutually exclusive: %w", llm.ErrInvalidRequest)
	}
	return nil
}

// parseImagesResponse maps an images-endpoint JSON body onto llm.ImageResponse.
// An empty data array is not an error here: the caller decides whether zero
// images is a failure.
func parseImagesResponse(body []byte) (llm.ImageResponse, error) {
	var decoded imagesResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return llm.ImageResponse{}, decodeError("images: decode response", err)
	}
	// A 200 may still carry a top-level error envelope, same as chat completions.
	if decoded.Error != nil {
		return llm.ImageResponse{}, fmt.Errorf("openrouter: images: %s: %w", decoded.Error.message(), decoded.Error.cause())
	}

	var response llm.ImageResponse
	for index, datum := range decoded.Data {
		image, ok, err := toImagePart(datum)
		if err != nil {
			return llm.ImageResponse{}, fmt.Errorf("openrouter: images: image %d: %w", index, err)
		}
		if ok {
			response.Images = append(response.Images, image)
		}
	}
	response.Usage = toLLMUsage(decoded.Usage)
	return response, nil
}

// toImagePart maps one datum: base64 bytes (a missing media_type is sniffed)
// or a URL (keeping whatever media type the provider reported). A datum with
// neither is skipped (ok false).
func toImagePart(datum imagesDatum) (llm.ImagePart, bool, error) {
	switch {
	case datum.B64JSON != "":
		data, err := base64.StdEncoding.DecodeString(datum.B64JSON)
		if err != nil {
			return llm.ImagePart{}, false, decodeError("bad base64 payload", err)
		}
		contentType := datum.MediaType
		if contentType == "" {
			contentType = http.DetectContentType(data)
		}
		return llm.ImagePart{ContentType: contentType, Data: data}, true, nil
	case datum.URL != "":
		return llm.ImagePart{URL: datum.URL, ContentType: datum.MediaType}, true, nil
	default:
		return llm.ImagePart{}, false, nil
	}
}
