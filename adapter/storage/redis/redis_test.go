package redis_test

import (
	"testing"

	goredis "github.com/redis/go-redis/v9"

	"github.com/rankegraph/ranke-go"
	"github.com/rankegraph/ranke-go/adapter/storage/redis"
)

// TestTier checks the write tier: lazy by default, overridable via WithTier.
// Capabilities is static, so no live redis is needed (the client never dials).
func TestTier(t *testing.T) {
	client := goredis.NewClient(&goredis.Options{})
	def, err := redis.New(client)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := def.Capabilities().Tier; got != ranke.StorageTierLazy {
		t.Fatalf("default tier = %q, want lazy", got)
	}
	eager, err := redis.New(client, redis.WithTier(ranke.StorageTierEager))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := eager.Capabilities().Tier; got != ranke.StorageTierEager {
		t.Fatalf("WithTier(eager) = %q, want eager", got)
	}
}
