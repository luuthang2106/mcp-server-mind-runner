package cli

import (
	"testing"

	"mind-runner/internal/config"
)

func TestCanRunJobs(t *testing.T) {
	var c config.Config
	if canRunJobs(c) {
		t.Fatal("không key, không local → không chạy job")
	}
	c.Spaces.Policy = map[string]string{"personal": "cloud", "secret": "local"}
	if !canRunJobs(c) {
		t.Fatal("có space local → chạy job")
	}
	c = config.Config{}
	c.Gateway.APIKey = "k"
	if !canRunJobs(c) {
		t.Fatal("có key → chạy job")
	}
}
