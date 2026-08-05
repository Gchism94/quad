// SPDX-License-Identifier: AGPL-3.0-or-later

// Package config holds runtime configuration, populated from the environment.
package config

import "os"

// Config is the control plane's runtime configuration.
type Config struct {
	ListenAddr  string // e.g. ":8080"
	DatabaseURL string // Postgres DSN
}

// Load reads configuration from environment variables, applying defaults.
func Load() Config {
	c := Config{
		ListenAddr:  os.Getenv("CAIRN_LISTEN_ADDR"),
		DatabaseURL: os.Getenv("CAIRN_DATABASE_URL"),
	}
	if c.ListenAddr == "" {
		c.ListenAddr = ":8080"
	}
	return c
}
