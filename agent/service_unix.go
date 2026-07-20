//go:build !windows

package main

import "github.com/breakerbox/breakerbox/agent/internal/appconfig"

// maybeRunAsService is a no-op on unix: systemd/launchd run the agent as a
// plain foreground process.
func maybeRunAsService(*appconfig.Store) (bool, error) { return false, nil }
