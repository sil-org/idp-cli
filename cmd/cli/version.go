/*
Copyright © 2023 SIL Global
*/
package main

import (
	"fmt"
	"runtime/debug"
	"strings"

	"github.com/spf13/cobra"
)

func SetupVersionCmd(parentCommand *cobra.Command) {
	parentCommand.AddCommand(versionCmd)
}

// version is set at build time
var version = ""

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Show idp-cli version",
	Run: func(cmd *cobra.Command, args []string) {
		if version != "" {
			fmt.Println("Version:", version)
			return
		}

		buildInfo, ok := debug.ReadBuildInfo()
		if ok {
			fmt.Println("Version:", strings.TrimLeft(buildInfo.Main.Version, "v"))
			return
		}

		fmt.Println("Version: unknown")
	},
}
