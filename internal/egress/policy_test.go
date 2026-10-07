package egress_test

import (
	"context"
	"strings"
	"testing"

	"mind-runner/internal/config"
	"mind-runner/internal/egress"
	"mind-runner/internal/egress/egressfake"
)

// TestPolicyRoutingIsolation: mỗi policy đi đúng endpoint của mình; local
// KHÔNG BAO GIỜ rơi sang cloud — kể cả endpoint chết, thiếu cấu hình hay
// policy viết sai (spec: không fallback ngầm).
func TestPolicyRoutingIsolation(t *testing.T) {
	ctx := context.Background()

	t.Run("local-chi-cham-local", func(t *testing.T) {
		cloud := egressfake.New(t, egressfake.Options{})
		local := egressfake.NewLocal(t, egressfake.Options{})
		eg := egress.New(egressfake.Config(cloud, local), nil)

		if _, err := eg.Embed(ctx, egress.PolicyLocal, []string{"a"}); err != nil {
			t.Fatal(err)
		}
		if _, err := eg.Rerank(ctx, egress.PolicyLocal, "q", []string{"d"}); err != nil {
			t.Fatal(err)
		}
		if _, err := eg.Chat(ctx, egress.PolicyLocal, "s", "u"); err != nil {
			t.Fatal(err)
		}
		if local.EmbedCalls != 1 || local.RerankCalls != 1 || local.ChatCalls != 1 {
			t.Fatalf("local=%d/%d/%d, muốn 1/1/1", local.EmbedCalls, local.RerankCalls, local.ChatCalls)
		}
		if cloud.EmbedCalls != 0 || cloud.RerankCalls != 0 || cloud.ChatCalls != 0 {
			t.Fatalf("cloud=%d/%d/%d, muốn 0", cloud.EmbedCalls, cloud.RerankCalls, cloud.ChatCalls)
		}
	})

	t.Run("local-thieu-base-url", func(t *testing.T) {
		cloud := egressfake.New(t, egressfake.Options{})
		eg := egress.New(egressfake.Config(cloud, nil), nil)

		_, err := eg.Embed(ctx, egress.PolicyLocal, []string{"a"})
		if err == nil || !strings.Contains(err.Error(), "base_url") {
			t.Fatalf("err=%v, muốn nêu [spaces.local].base_url", err)
		}
		if cloud.EmbedCalls != 0 {
			t.Fatalf("cloud.EmbedCalls=%d, muốn 0", cloud.EmbedCalls)
		}
	})

	t.Run("local-chet-khong-roi-cloud", func(t *testing.T) {
		cloud := egressfake.New(t, egressfake.Options{})
		local := egressfake.NewLocal(t, egressfake.Options{})
		eg := egress.New(egressfake.Config(cloud, local), nil)
		local.Close()

		if _, err := eg.Embed(ctx, egress.PolicyLocal, []string{"a"}); err == nil {
			t.Fatal("endpoint chết phải lỗi")
		}
		if cloud.EmbedCalls != 0 {
			t.Fatalf("cloud.EmbedCalls=%d, muốn 0", cloud.EmbedCalls)
		}
	})

	t.Run("cloud-khong-cham-local", func(t *testing.T) {
		cloud := egressfake.New(t, egressfake.Options{})
		local := egressfake.NewLocal(t, egressfake.Options{})
		eg := egress.New(egressfake.Config(cloud, local), nil)

		if _, err := eg.Embed(ctx, egress.PolicyCloud, []string{"a"}); err != nil {
			t.Fatal(err)
		}
		if cloud.EmbedCalls != 1 || local.EmbedCalls != 0 {
			t.Fatalf("cloud=%d local=%d, muốn 1/0", cloud.EmbedCalls, local.EmbedCalls)
		}
	})

	t.Run("thieu-model-local", func(t *testing.T) {
		cloud := egressfake.New(t, egressfake.Options{})
		local := egressfake.NewLocal(t, egressfake.Options{})
		cfg := egressfake.Config(cloud, local)
		cfg.Spaces.Local.Models = config.Models{}
		eg := egress.New(cfg, nil)

		if _, err := eg.Embed(ctx, egress.PolicyLocal, []string{"a"}); err == nil || !strings.Contains(err.Error(), "embed") {
			t.Fatalf("Embed err=%v, muốn nêu model embed", err)
		}
		if _, err := eg.Chat(ctx, egress.PolicyLocal, "s", "u"); err == nil || !strings.Contains(err.Error(), "extract") {
			t.Fatalf("Chat err=%v, muốn nêu model extract", err)
		}
		if _, err := eg.Rerank(ctx, egress.PolicyLocal, "q", []string{"d"}); err == nil || !strings.Contains(err.Error(), "rerank") {
			t.Fatalf("Rerank err=%v, muốn nêu model rerank", err)
		}
		if cloud.EmbedCalls != 0 || cloud.ChatCalls != 0 || cloud.RerankCalls != 0 {
			t.Fatalf("cloud=%d/%d/%d, muốn 0", cloud.EmbedCalls, cloud.ChatCalls, cloud.RerankCalls)
		}
	})

	t.Run("policy-la-khong-roi-cloud", func(t *testing.T) {
		cloud := egressfake.New(t, egressfake.Options{})
		eg := egress.New(egressfake.Config(cloud, nil), nil)

		if _, err := eg.Embed(ctx, egress.Policy("locl"), []string{"a"}); err == nil {
			t.Fatal("policy không hợp lệ phải lỗi, không được rơi về cloud")
		}
		if cloud.EmbedCalls != 0 {
			t.Fatalf("cloud.EmbedCalls=%d, muốn 0", cloud.EmbedCalls)
		}
	})

	t.Run("embed-model-theo-policy", func(t *testing.T) {
		cloud := egressfake.New(t, egressfake.Options{})
		local := egressfake.NewLocal(t, egressfake.Options{})
		eg := egress.New(egressfake.Config(cloud, local), nil)

		if got := eg.EmbedModel(egress.PolicyCloud); got != "text-embedding-v4" {
			t.Fatalf("cloud model=%q", got)
		}
		if got := eg.EmbedModel(egress.PolicyLocal); got == "" {
			t.Fatal("local model rỗng")
		}
	})
}
