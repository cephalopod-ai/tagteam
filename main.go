package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/cephalopod-ai/tagteam/internal/cli"
	"github.com/cephalopod-ai/tagteam/internal/tagteam"
)

func main() {
	root := cli.NewRootCommand()
	root.SetArgs(normalizeArgs(os.Args[1:]))
	if err := root.Execute(); err != nil {
		if strings.TrimSpace(err.Error()) != "" {
			fmt.Fprintln(os.Stderr, err.Error())
		}
		os.Exit(tagteam.ExitCode(err))
	}
}

func normalizeArgs(args []string) []string {
	normalized := make([]string, 0, len(args))
	for _, arg := range args {
		switch {
		case arg == "-mc":
			normalized = append(normalized, "--mc")
		case arg == "-ma":
			normalized = append(normalized, "--ma")
		case strings.HasPrefix(arg, "-mc="):
			normalized = append(normalized, "--mc="+strings.TrimPrefix(arg, "-mc="))
		case strings.HasPrefix(arg, "-ma="):
			normalized = append(normalized, "--ma="+strings.TrimPrefix(arg, "-ma="))
		default:
			normalized = append(normalized, arg)
		}
	}
	return normalized
}
