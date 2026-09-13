package main

import (
	"errors"
	"flag"
	"os"

	"github.com/openclaw/crawlkit/output"
	"github.com/openclaw/photoscrawl/internal/archive"
)

type commandFlags struct {
	*flag.FlagSet
	paths    *archive.Paths
	database string
	json     bool
	format   string
}

func newCommandFlags(name string, paths *archive.Paths) *commandFlags {
	flags := &commandFlags{FlagSet: flag.NewFlagSet(name, flag.ContinueOnError), paths: paths}
	flags.SetOutput(os.Stderr)
	flags.BoolVar(&flags.json, "json", false, "write JSON")
	flags.StringVar(&flags.format, "format", "", "output format")
	if paths != nil {
		flags.StringVar(&flags.database, "db", "", "photos.sqlite path")
	}
	return flags
}

func (flags *commandFlags) parse(args []string, flagsOnly bool) (output.Format, error) {
	if err := flags.Parse(args); err != nil {
		return "", output.UsageError{Err: err}
	}
	if flagsOnly && flags.NArg() != 0 {
		return "", output.UsageError{Err: errors.New(flags.Name() + " takes flags only")}
	}
	if flags.paths != nil && flags.database != "" {
		flags.paths.Database = flags.database
	}
	return output.Resolve(flags.format, flags.json)
}
