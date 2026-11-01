package openrouter

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/govengers/aikido/llm"
)

// Compile-time conformance for the optional video capability.
var _ llm.VideoGenerator = (*Client)(nil)

// GenerateVideo renders one clip through OpenRouter's video endpoints: POST
// /videos enqueues a job, GET /videos/{id} is polled every
// Options.VideoPollInterval until it settles, GET /videos/{id}/content serves
// the file under the same bearer token. It blocks for minutes; ctx bounds it.
//
// An invalid request fails with llm.ErrInvalidRequest before any request.
// Each request retries like Complete's (429, 5xx). A refusal surfaces as
// ErrContentFiltered: a failed job or a 400 whose message reads like
// moderation, and a completed job without any output, which is how some
// providers (Veo) report a silent safety rejection. Once the job exists the
// response carries its JobID, also with an error.
func (c *Client) GenerateVideo(ctx context.Context, request llm.VideoRequest) (llm.VideoResponse, error) {
	body, err := buildVideoBody(request)
	if err != nil {
		return llm.VideoResponse{}, fmt.Errorf("openrouter: video: %w", err)
	}
	start := time.Now()
	job, err := c.submitVideo(ctx, body)
	if err != nil {
		return llm.VideoResponse{}, err
	}
	escapedID := url.PathEscape(job.ID)
	job, err = c.awaitVideo(ctx, escapedID, job)
	if err != nil {
		return llm.VideoResponse{JobID: job.ID}, err
	}
	response := llm.VideoResponse{JobID: job.ID, Usage: &llm.Usage{}}
	if job.Usage != nil {
		response.Usage.CostUSD = job.Usage.Cost
	}
	response.Video, _, err = c.do(ctx, httpCall{
		method:       http.MethodGet,
		url:          c.baseURL + "/videos/" + escapedID + "/content?index=0",
		accept:       "video/*",
		wantStatus:   http.StatusOK,
		maximumBytes: maximumVideoBytes,
	})
	if err != nil {
		return response, err
	}
	if len(response.Video) == 0 {
		return response, fmt.Errorf("openrouter: video: job %s: empty clip: %w", job.ID, llm.ErrServerError)
	}
	response.ContentType = http.DetectContentType(response.Video)
	response.Usage.SetTiming(start, time.Time{}, time.Now())
	return response, nil
}

// submitVideo enqueues the job; the endpoint answers 202 with its id.
func (c *Client) submitVideo(ctx context.Context, body []byte) (videoJob, error) {
	raw, _, err := c.do(ctx, httpCall{
		method:     http.MethodPost,
		url:        c.baseURL + "/videos",
		body:       body,
		accept:     "application/json",
		wantStatus: http.StatusAccepted,
	})
	if err != nil {
		if errors.Is(err, llm.ErrInvalidRequest) && isRefusal(err.Error()) {
			return videoJob{}, fmt.Errorf("%w: %w", err, llm.ErrContentFiltered)
		}
		return videoJob{}, err
	}
	var job videoJob
	if err := json.Unmarshal(raw, &job); err != nil {
		return videoJob{}, decodeError("video: decode submit answer", err)
	}
	if job.ID == "" {
		return videoJob{}, fmt.Errorf("openrouter: video: submit answered without a job id: %w", llm.ErrInvalidResponse)
	}
	return job, nil
}

// awaitVideo polls the job until it completes or fails. job is the submit
// answer, returned as is when it already settled.
func (c *Client) awaitVideo(ctx context.Context, escapedID string, job videoJob) (videoJob, error) {
	ticker := time.NewTicker(c.videoPollInterval)
	defer ticker.Stop()
	for {
		settled, err := videoJobOutcome(job)
		if settled {
			return job, err
		}
		select {
		case <-ctx.Done():
			return job, ctx.Err()
		case <-ticker.C:
		}
		raw, _, err := c.do(ctx, httpCall{
			method:     http.MethodGet,
			url:        c.baseURL + "/videos/" + escapedID,
			accept:     "application/json",
			wantStatus: http.StatusOK,
		})
		if err != nil {
			return job, err
		}
		if err := json.Unmarshal(raw, &job); err != nil {
			return job, decodeError("video: decode job", err)
		}
	}
}

// videoJobOutcome reports whether the job has settled and, if so, whether it
// failed. A completed job without any clip is a silent moderation refusal.
func videoJobOutcome(job videoJob) (bool, error) {
	switch job.Status {
	case "completed":
		if len(job.UnsignedURLs) == 0 && len(job.URLs) == 0 {
			return true, fmt.Errorf("openrouter: video: job %s completed without a clip (silent moderation refusal?): %w", job.ID, llm.ErrContentFiltered)
		}
		return true, nil
	case "failed", "cancelled", "expired":
		message := videoErrorMessage(job.Error)
		if message == "" {
			message = "job " + job.Status
		}
		cause := llm.ErrServerError
		if isRefusal(message) {
			cause = llm.ErrContentFiltered
		}
		return true, fmt.Errorf("openrouter: video: job %s: %s: %w", job.ID, message, cause)
	default:
		return false, nil
	}
}

// buildVideoBody assembles the POST /videos body. Unset optional fields stay
// off the wire.
func buildVideoBody(request llm.VideoRequest) ([]byte, error) {
	if err := validateVideoRequest(request); err != nil {
		return nil, err
	}
	body := videoRequest{
		Model:         request.Model,
		Prompt:        request.Prompt,
		AspectRatio:   request.AspectRatio,
		Resolution:    request.Resolution,
		Size:          request.Size,
		Duration:      request.DurationSeconds,
		GenerateAudio: request.GenerateAudio,
		Seed:          request.Seed,
	}
	frames, err := buildFrameImages(request.FirstFrame, request.LastFrame)
	if err != nil {
		return nil, err
	}
	body.FrameImages = frames
	for index, reference := range request.References {
		referenceURL, err := imageURL(reference)
		if err != nil {
			return nil, fmt.Errorf("reference image %d: %w", index, err)
		}
		body.InputReferences = append(body.InputReferences, apiImagePart{Type: "image_url", ImageURL: &apiImageURL{URL: referenceURL}})
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode video request: %w", err)
	}
	return encoded, nil
}

func validateVideoRequest(request llm.VideoRequest) error {
	if strings.TrimSpace(request.Model) == "" {
		return fmt.Errorf("model is required: %w", llm.ErrInvalidRequest)
	}
	if strings.TrimSpace(request.Prompt) == "" {
		return fmt.Errorf("prompt is required: %w", llm.ErrInvalidRequest)
	}
	if request.Size != "" && (request.AspectRatio != "" || request.Resolution != "") {
		return fmt.Errorf("size and aspect ratio/resolution are mutually exclusive: %w", llm.ErrInvalidRequest)
	}
	return nil
}

// buildFrameImages maps the optional anchor frames, first before last.
func buildFrameImages(first, last *llm.ImagePart) ([]apiFrameImage, error) {
	var frames []apiFrameImage
	for _, frame := range []struct {
		part *llm.ImagePart
		kind string
	}{{first, "first_frame"}, {last, "last_frame"}} {
		if frame.part == nil {
			continue
		}
		frameURL, err := imageURL(*frame.part)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", frame.kind, err)
		}
		frames = append(frames, apiFrameImage{Type: "image_url", ImageURL: &apiImageURL{URL: frameURL}, FrameType: frame.kind})
	}
	return frames, nil
}

// imageURL is the part's URL, or its bytes as a data URI.
func imageURL(part llm.ImagePart) (string, error) {
	if part.URL != "" {
		return part.URL, nil
	}
	if len(part.Data) == 0 {
		return "", fmt.Errorf("image has neither URL nor data: %w", llm.ErrInvalidRequest)
	}
	contentType := part.ContentType
	if contentType == "" {
		contentType = http.DetectContentType(part.Data)
	}
	return "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(part.Data), nil
}

// videoErrorMessage flattens the job's error field: a string or an object
// with a message.
func videoErrorMessage(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var envelope apiError
	if json.Unmarshal(raw, &envelope) == nil && envelope.Message != "" {
		return envelope.Message
	}
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	return string(raw)
}

// refusalMarkers word a moderation refusal. No video provider sends a
// dedicated code, and each words it differently.
var refusalMarkers = []string{"moderation", "content policy", "safety", "flagged", "sensitive", "prohibited", "nsfw"}

// isRefusal recognises a moderation refusal in a video provider's message.
func isRefusal(message string) bool {
	lowered := strings.ToLower(message)
	for _, marker := range refusalMarkers {
		if strings.Contains(lowered, marker) {
			return true
		}
	}
	return false
}

// VideoModel is one entry of GET /videos/models: the closed lists of values a
// model accepts. Send only listed values; the endpoint rejects the rest.
type VideoModel struct {
	ID                    string   `json:"id"`
	Name                  string   `json:"name"`
	Description           string   `json:"description"`
	Created               int64    `json:"created"`
	SupportedResolutions  []string `json:"supported_resolutions"`
	SupportedAspectRatios []string `json:"supported_aspect_ratios"`
	SupportedSizes        []string `json:"supported_sizes"`
	SupportedDurations    []int    `json:"supported_durations"`
	// SupportedFrameImages lists "first_frame" and/or "last_frame".
	SupportedFrameImages []string `json:"supported_frame_images"`
	// GenerateAudio is nil when the model has no audio switch: send no
	// generate_audio then.
	GenerateAudio *bool `json:"generate_audio"`
	Seed          *bool `json:"seed"`
	// PricingSKUs maps a SKU ("duration_seconds_720p",
	// "cents_per_video_output_second_1080p", ...) to its price as sent; the
	// unit is in the key and differs per provider.
	PricingSKUs map[string]string `json:"pricing_skus"`
}

// VideoModels lists the video-generation models with their supported values.
// They are not part of GET /models.
func (c *Client) VideoModels(ctx context.Context) ([]VideoModel, error) {
	var out struct {
		Data []VideoModel `json:"data"`
	}
	if err := c.getJSON(ctx, "/videos/models", &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}
