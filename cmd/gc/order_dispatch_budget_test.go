package main

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/orders"
)

func TestOrderDispatchBudget(t *testing.T) {
	// The default is documented as 4 on config.OrdersConfig.MaxDispatchesPerTick
	// and in docs/reference/config.md. Changing it means changing those too.
	if defaultMaxOrderDispatchesPerTick != 4 {
		t.Fatalf("defaultMaxOrderDispatchesPerTick = %d; update the default documented on OrdersConfig.MaxDispatchesPerTick and rerun go run ./cmd/genschema", defaultMaxOrderDispatchesPerTick)
	}
	withBudget := func(n int) *config.City {
		cfg := &config.City{}
		cfg.Orders.MaxDispatchesPerTick = n
		return cfg
	}
	cases := []struct {
		name string
		cfg  *config.City
		want int
	}{
		{"nil config", nil, defaultMaxOrderDispatchesPerTick},
		{"unset", &config.City{}, defaultMaxOrderDispatchesPerTick},
		{"zero is the default, not unlimited", withBudget(0), defaultMaxOrderDispatchesPerTick},
		{"negative is the default, not unlimited", withBudget(-3), defaultMaxOrderDispatchesPerTick},
		{"one", withBudget(1), 1},
		{"above the default", withBudget(12), 12},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := orderDispatchBudget(tc.cfg); got != tc.want {
				t.Errorf("orderDispatchBudget() = %d, want %d", got, tc.want)
			}
		})
	}
}

// The production builder is the only place the budget comes from config.
func TestBuildOrderDispatcherUsesConfiguredBudget(t *testing.T) {
	aa := []orders.Order{{Name: "budget-probe", Trigger: "cooldown", Interval: "1m", Exec: "true"}}
	for _, tc := range []struct {
		name string
		set  int
		want int
	}{
		{"unset keeps the default", 0, defaultMaxOrderDispatchesPerTick},
		{"configured value reaches the dispatcher", 9, 9},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.City{}
			cfg.Orders.MaxDispatchesPerTick = tc.set
			ad := buildOrderDispatcherFromOrderSet(t.TempDir(), cfg, aa, events.Discard, &bytes.Buffer{})
			m, ok := ad.(*memoryOrderDispatcher)
			if !ok {
				t.Fatalf("dispatcher = %T, want *memoryOrderDispatcher", ad)
			}
			t.Cleanup(m.cancel)
			if m.maxDispatchesPerTick != tc.want {
				t.Errorf("maxDispatchesPerTick = %d, want %d", m.maxDispatchesPerTick, tc.want)
			}
		})
	}
}

// With more orders due than the cap, one tick starts exactly cap of them; a
// higher configured cap starts more in the same tick.
func TestOrderDispatchConfiguredBudgetChangesOrdersPerTick(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  int
		want int
	}{
		{"default cap", 0, defaultMaxOrderDispatchesPerTick},
		{"raised cap", 6, 6},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := beads.NewMemStore()
			var aa []orders.Order
			for i := 0; i < 8; i++ {
				aa = append(aa, orders.Order{
					Name:     fmt.Sprintf("order-%d", i),
					Trigger:  "cooldown",
					Interval: "1m",
					Exec:     "true",
				})
			}
			ad := buildOrderDispatcherFromListExec(aa, store, nil, func(context.Context, string, string, []string) ([]byte, error) {
				return []byte("ok\n"), nil
			}, nil)
			if ad == nil {
				t.Fatal("expected non-nil dispatcher")
			}
			cfg := &config.City{}
			cfg.Orders.MaxDispatchesPerTick = tc.set
			ad.(*memoryOrderDispatcher).maxDispatchesPerTick = orderDispatchBudget(cfg)

			ad.dispatch(context.Background(), t.TempDir(), time.Date(2026, 5, 19, 2, 30, 0, 0, time.UTC))
			ad.drain(context.Background())

			if got := countOrderTrackingRuns(t, store); got != tc.want {
				t.Fatalf("orders started in one tick = %d, want %d", got, tc.want)
			}
		})
	}
}
