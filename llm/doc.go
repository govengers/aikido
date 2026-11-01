// Package llm defines the provider-agnostic types and the Client interface
// every LLM provider satisfies.
//
// The shapes here are deliberately the lowest common denominator across
// providers. Provider-specific knobs live on each provider's Options struct.
//
// Optional capabilities (ImageGenerator, SpeechGenerator, VideoGenerator,
// Decider) are separate interfaces; Collect, CollectWithRetry and Transcribe
// work on any Client. Guide: docs/wiki/Models.md in the repository.
package llm
