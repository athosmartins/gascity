package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/doctor"
)

type coordStoreBackendCheck struct {
	cityPath string
	cfg      *config.City
}

func newCoordStoreBackendCheck(cityPath string, cfg *config.City) *coordStoreBackendCheck {
	return &coordStoreBackendCheck{cityPath: cityPath, cfg: cfg}
}

func (c *coordStoreBackendCheck) Name() string { return "coord-store-backend" }

func (c *coordStoreBackendCheck) CanFix() bool { return false }

func (c *coordStoreBackendCheck) WarmupEligible() bool { return false }

func (c *coordStoreBackendCheck) Fix(_ *doctor.CheckContext) error { return nil }

func (c *coordStoreBackendCheck) Run(ctx *doctor.CheckContext) *doctor.CheckResult {
	r := &doctor.CheckResult{Name: c.Name()}
	rawBackend := ""
	if c.cfg != nil {
		rawBackend = c.cfg.Beads.Backend
	} else {
		rawBackend = peekBeadsBackend(filepath.Join(c.cityPath, "city.toml"))
	}
	provider := rawBeadsProvider(c.cityPath)
	bboltPath := bboltCityStorePath(c.cityPath)
	if ctx != nil && ctx.Verbose {
		r.Details = append(r.Details,
			fmt.Sprintf("raw backend: %s", doctorDisplayEmpty(rawBackend)),
			fmt.Sprintf("provider: %s", doctorDisplayEmpty(provider)),
			fmt.Sprintf("bbolt path: %s", bboltPath),
		)
	}

	if !providerUsesBdStoreContract(provider) {
		r.Status = doctor.StatusOK
		r.Message = fmt.Sprintf("backend selection inactive for provider %q", provider)
		return r
	}
	backend, err := normalizeBeadsBackend(rawBackend)
	if err != nil {
		r.Status = doctor.StatusError
		r.Message = firstDiagnosticLine(err.Error())
		r.FixHint = `set [beads].backend to "" (dolt, default), "dolt", or "bbolt", then rerun gc doctor`
		if ctx != nil && ctx.Verbose {
			r.Details = append(r.Details, remainingDiagnosticLines(err.Error())...)
		}
		return r
	}

	if backend == beadsBackendDolt {
		r.Status = doctor.StatusOK
		r.Message = "using managed Dolt coord-store backend"
		return r
	}

	info, err := os.Stat(bboltPath)
	if err == nil {
		if info.IsDir() {
			r.Status = doctor.StatusError
			r.Message = fmt.Sprintf("bbolt store path is a directory: %s", bboltPath)
			r.FixHint = "move the directory aside so gc start can create the bbolt store file"
			return r
		}
		r.Status = doctor.StatusOK
		r.Message = fmt.Sprintf("using bbolt coord-store backend at %s", bboltPath)
		return r
	}
	if os.IsNotExist(err) {
		r.Status = doctor.StatusOK
		r.Message = "using bbolt coord-store backend; store will be created on gc start"
		return r
	}
	r.Status = doctor.StatusError
	r.Message = fmt.Sprintf("stat bbolt store path %s: %v", bboltPath, err)
	r.FixHint = "fix filesystem permissions for the bbolt store path, then rerun gc doctor"
	return r
}

func doctorDisplayEmpty(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "(empty)"
	}
	return value
}

func firstDiagnosticLine(value string) string {
	if i := strings.IndexByte(value, '\n'); i >= 0 {
		return value[:i]
	}
	return value
}

func remainingDiagnosticLines(value string) []string {
	lines := strings.Split(value, "\n")
	if len(lines) <= 1 {
		return nil
	}
	return lines[1:]
}
