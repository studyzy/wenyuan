package app

import (
	"context"
	"strings"

	"github.com/studyzy/wenyuan/internal/provider"
)

// OutputLanguageDirective is the instruction appended to a request when a
// deployment has configured an output language.
//
// It is shared by every model-generated artifact — extracted facts, consolidated
// observations, reflected answers, mental-model bodies — because the setting is
// a property of the deployment, not of one pipeline. An empty language returns
// an empty string, so an unconfigured deployment sends exactly what it sent
// before.
func OutputLanguageDirective(language string) string {
	return provider.OutputLanguageDirective(language)
}

// languageProvider applies the configured output language to every completion,
// so no pipeline has to remember it.
type languageProvider struct {
	provider.Provider
	language string
}

// WithOutputLanguage wraps a provider so its completions carry the output
// language directive. With no language configured, or no provider, it returns
// the provider unchanged.
func WithOutputLanguage(p provider.Provider, language string) provider.Provider {
	if p == nil || strings.TrimSpace(language) == "" {
		return p
	}
	return &languageProvider{Provider: p, language: strings.TrimSpace(language)}
}

// RerankCapability lets ProviderReranker see through the wrapper.
func (p *languageProvider) RerankCapability() provider.Reranker {
	return provider.ProviderReranker(p.Provider)
}

// Complete adds the directive to the request, then runs it.
func (p *languageProvider) Complete(ctx context.Context, req provider.CompletionRequest) (provider.CompletionResult, error) {
	return p.Provider.Complete(ctx, applyOutputLanguage(req, p.language))
}

// applyOutputLanguage appends the directive to the request's system message,
// inserting one when the request carries none.
//
// It goes on the system message rather than beside the question so that it is
// not out-ranked by a user turn that follows it, which is what happens when the
// rule is phrased as part of the question.
func applyOutputLanguage(req provider.CompletionRequest, language string) provider.CompletionRequest {
	directive := OutputLanguageDirective(language)
	if directive == "" {
		return req
	}
	messages := make([]provider.Message, len(req.Messages))
	copy(messages, req.Messages)
	for i := range messages {
		if strings.EqualFold(string(messages[i].Role), "system") {
			messages[i].Content = messages[i].Content + directive
			req.Messages = messages
			return req
		}
	}
	req.Messages = append([]provider.Message{{Role: provider.RoleSystem, Content: strings.TrimPrefix(directive, "\n\n")}}, messages...)
	return req
}
