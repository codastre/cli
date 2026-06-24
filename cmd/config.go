package cmd

import "os"

// defaultServerURL returns CODASTRE_SERVER env var, falling back to the hosted API.
func defaultServerURL() string {
	if v := os.Getenv("CODASTRE_SERVER"); v != "" {
		return v
	}
	return "https://api.codastre.com"
}
