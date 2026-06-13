package cmd

import "github.com/spf13/cobra"

var rootCmd = &cobra.Command{
	Use:   "codastre",
	Short: "Topology-aware hybrid retrieval and knowledge graph for codebases, runbooks, and docs",
}

// Execute is the entry point called from main.
func Execute() error {
	return rootCmd.Execute()
}
