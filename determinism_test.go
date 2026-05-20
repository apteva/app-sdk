package sdk

import (
	"os"
	"testing"
	"time"
)

func TestNowFakeTime(t *testing.T) {
	c := &AppCtx{}
	t.Setenv("APTEVA_FAKE_TIME", "2020-01-02T03:04:05Z")
	want, _ := time.Parse(time.RFC3339, "2020-01-02T03:04:05Z")
	if got := c.Now(); !got.Equal(want) {
		t.Fatalf("Now() got %v want %v", got, want)
	}
	// Unix seconds form.
	t.Setenv("APTEVA_FAKE_TIME", "1000000000")
	if got := c.Now(); got.Unix() != 1000000000 {
		t.Fatalf("Now() unix got %d", got.Unix())
	}
}

func TestNewIDDeterministic(t *testing.T) {
	c := &AppCtx{}
	os.Unsetenv("APTEVA_SEED")
	if c.NewID() == c.NewID() {
		t.Fatalf("unseeded ids should differ")
	}
	t.Setenv("APTEVA_SEED", "42")
	a, b := c.NewID(), c.NewID()
	if a == b {
		t.Fatalf("seeded ids should be unique (monotonic): %s == %s", a, b)
	}
	if a[:3] != "id-" {
		t.Fatalf("seeded id format wrong: %s", a)
	}
}

func TestRandRepeatable(t *testing.T) {
	c := &AppCtx{}
	t.Setenv("APTEVA_SEED", "7")
	rndCounter.Store(0)
	r1 := c.Rand().Int63()
	rndCounter.Store(0)
	r2 := c.Rand().Int63()
	if r1 != r2 {
		t.Fatalf("seeded Rand not repeatable: %d vs %d", r1, r2)
	}
}
