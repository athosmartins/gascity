package config

import (
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/gastownhall/gascity/internal/fsys"
)

func TestOrdersMaxDispatchesPerTickParses(t *testing.T) {
	cases := []struct {
		name string
		body string
		want int
	}{
		{"unset", "", 0},
		{"explicit", "\n[orders]\nmax_dispatches_per_tick = 12\n", 12},
		{"zero", "\n[orders]\nmax_dispatches_per_tick = 0\n", 0},
		{"negative is kept as written (the dispatcher side maps it to the default)", "\n[orders]\nmax_dispatches_per_tick = -3\n", -3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Parse([]byte("[workspace]\nname = \"test\"\n" + tc.body))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if got := cfg.Orders.MaxDispatchesPerTick; got != tc.want {
				t.Errorf("Orders.MaxDispatchesPerTick = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestOrdersMaxDispatchesPerTickIsAKnownKey(t *testing.T) {
	var cfg City
	md, err := toml.Decode(`
[workspace]
name = "test"

[orders]
max_dispatches_per_tick = 12
`, &cfg)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if warnings := CheckUndecodedKeys(md, "city.toml"); len(warnings) != 0 {
		t.Errorf("max_dispatches_per_tick must not be reported as an unknown key, got: %v", warnings)
	}
}

func TestLoadWithIncludes_OrdersMaxDispatchesPerTickOverride(t *testing.T) {
	fs := fsys.NewFake()
	fs.Files["/city/city.toml"] = []byte(`
include = ["infra.toml"]

[workspace]
name = "test"

[orders]
max_dispatches_per_tick = 8
`)
	fs.Files["/city/infra.toml"] = []byte(`
[orders]
max_dispatches_per_tick = 12
`)
	cfg, _, err := LoadWithIncludes(fs, "/city/city.toml")
	if err != nil {
		t.Fatalf("LoadWithIncludes: %v", err)
	}
	if got := cfg.Orders.MaxDispatchesPerTick; got != 12 {
		t.Errorf("Orders.MaxDispatchesPerTick = %d, want 12 (the fragment's [orders] wins)", got)
	}
}
