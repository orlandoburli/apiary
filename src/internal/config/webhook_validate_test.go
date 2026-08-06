package config

import (
	"strings"
	"testing"
)

// pushConfig is a minimal valid config with one webhook-type source.
func pushConfig(listen string) *Config {
	return &Config{
		Version: "1",
		Sources: []SourceConfig{{ID: "hooks", Type: "webhook", Config: map[string]any{"secret": "s"}}},
		Settings: Settings{
			Webhook: WebhookSettings{Listen: listen},
		},
	}
}

func TestPushSourceRequiresWebhookListen(t *testing.T) {
	prev := SourcePushCapable
	defer func() { SourcePushCapable = prev }()
	SourcePushCapable = func(sourceType string) bool { return sourceType == "webhook" }

	errs := pushConfig("").Validate()
	found := false
	for _, err := range errs {
		if strings.Contains(err.Error(), "settings.webhook.listen") {
			found = true
		}
	}
	if !found {
		t.Errorf("webhook source without settings.webhook.listen should error, got: %v", errs)
	}

	for _, err := range pushConfig("127.0.0.1:8090").Validate() {
		if strings.Contains(err.Error(), "settings.webhook.listen") {
			t.Errorf("listen set — unexpected error: %v", err)
		}
	}

	// Hook not injected (isolated tests): check skipped.
	SourcePushCapable = nil
	for _, err := range pushConfig("").Validate() {
		if strings.Contains(err.Error(), "settings.webhook.listen") {
			t.Errorf("nil hook — check must be skipped, got: %v", err)
		}
	}
}
