// image-generation demonstrates calling an OpenRouter image-capable model and
// writing the returned PNG/JPEG bytes to disk.
//
// Set OPENROUTER_API_KEY to run against the real provider:
//
//	go run ./examples/image-generation
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/llm/openrouter"
)

const outputDirectory = "out"

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	apiKey := os.Getenv("OPENROUTER_API_KEY")
	if apiKey == "" {
		return errors.New("OPENROUTER_API_KEY not set")
	}

	client, err := openrouter.NewClient(&openrouter.Options{
		APIKey:      apiKey,
		HTTPReferer: "https://github.com/govengers/aikido",
		XTitle:      "aikido image-generation example",
	})
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	response, err := llm.Collect(ctx, client, llm.Request{
		Model: "google/gemini-2.5-flash-image-preview",
		Messages: []llm.Message{
			{Role: llm.RoleUser, Content: "Generate a small pixel-art icon of a fox sitting in tall grass."},
		},
		MaximumTokens: 1024,
	})
	if err != nil {
		return fmt.Errorf("collect: %w", err)
	}
	if len(response.Images) == 0 {
		return errors.New("provider returned no images")
	}
	if err := writeImages(response.Images); err != nil {
		return err
	}
	if usage := response.Usage; usage != nil {
		fmt.Printf("tokens: prompt=%d completion=%d cost=$%.6f\n",
			usage.PromptTokens, usage.CompletionTokens, usage.CostUSD)
	}
	return nil
}

// writeImages saves the inline images into outputDirectory and prints the URL
// of any image the provider only links to.
func writeImages(images []llm.ImagePart) error {
	if err := os.MkdirAll(outputDirectory, 0o750); err != nil {
		return err
	}
	for index, image := range images {
		switch {
		case len(image.Data) > 0:
			path := filepath.Join(outputDirectory, fmt.Sprintf("image-%02d%s", index, imageExtension(image.ContentType)))
			if err := os.WriteFile(path, image.Data, 0o600); err != nil {
				return err
			}
			fmt.Printf("wrote %s (%d bytes, %s)\n", path, len(image.Data), image.ContentType)
		case image.URL != "":
			fmt.Printf("image %d available at: %s\n", index, image.URL)
		}
	}
	return nil
}

func imageExtension(contentType string) string {
	switch strings.ToLower(contentType) {
	case "image/png":
		return ".png"
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	default:
		return ".bin"
	}
}
