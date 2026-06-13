package cmd

import "os"

// defaultServerURL returns CODASTRE_SERVER env var, falling back to localhost.
func defaultServerURL() string {
	if v := os.Getenv("CODASTRE_SERVER"); v != "" {
		return v
	}
	return "http://localhost:8000"
}
