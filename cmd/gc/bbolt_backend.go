package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	berrors "go.etcd.io/bbolt/errors"
)

const (
	beadsBackendDolt  = "dolt"
	beadsBackendBbolt = "bbolt"
)

func normalizeBeadsBackend(raw string) (string, error) {
	switch strings.TrimSpace(raw) {
	case "", beadsBackendDolt:
		return beadsBackendDolt, nil
	case beadsBackendBbolt:
		return beadsBackendBbolt, nil
	default:
		return "", fmt.Errorf("unrecognized backend value %q\nhint: valid values for [beads] backend are: \"\" (dolt, default), \"dolt\", or \"bbolt\"\nhint: run `gc doctor` to see the currently active backend", raw)
	}
}

func configuredBeadsBackendForCity(cityPath string, cfg *config.City) (string, error) {
	backend := ""
	if cfg == nil {
		backend = peekBeadsBackend(filepath.Join(cityPath, "city.toml"))
	} else {
		backend = cfg.Beads.Backend
	}
	if !providerUsesBdStoreContract(rawBeadsProvider(cityPath)) {
		return "", nil
	}
	return normalizeBeadsBackend(backend)
}

func cityUsesBboltBackend(cityPath string, cfg *config.City) (bool, error) {
	backend, err := configuredBeadsBackendForCity(cityPath, cfg)
	if err != nil {
		return false, err
	}
	return backend == beadsBackendBbolt, nil
}

func bboltCityStorePath(cityPath string) string {
	return filepath.Join(cityPath, ".gc", "state", "bbolt", "beads.bolt")
}

func openBboltCityStore(cityPath, prefix string) (*beads.BboltStore, error) {
	path := bboltCityStorePath(cityPath)
	store, err := beads.OpenBboltStore(path, beads.WithBboltStoreIDPrefix(prefix))
	if err != nil {
		return nil, formatBboltOpenError(path, err)
	}
	return store, nil
}

func formatBboltOpenError(path string, err error) error {
	if errors.Is(err, berrors.ErrTimeout) {
		return fmt.Errorf("bbolt bead store: open %s: timeout (5s): file is already locked: %w\nhint: another gc controller may already be running - run `gc status` to check\nhint: if no other controller is running, remove %s to clear a stale lock", path, err, path+".lock")
	}
	return fmt.Errorf("bbolt bead store: %w", err)
}
