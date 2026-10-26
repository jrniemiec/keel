package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/jrniemiec/keel/config"
	"github.com/jrniemiec/keel/models"
)

// modelsFlags holds keel models' flags.
type modelsFlags struct {
	config, source string
	diff, help     bool
}

// newModelsFlags defines keel models' flags. They are described in helpText.
func newModelsFlags() (*flag.FlagSet, *modelsFlags) {
	fs := flag.NewFlagSet("models", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // errors are reported by main, help by helpText
	f := &modelsFlags{}
	for _, n := range []string{"c", "config"} {
		fs.StringVar(&f.config, n, "", "")
	}
	fs.StringVar(&f.source, "source", models.LiteLLMURL, "")
	fs.BoolVar(&f.diff, "diff", false, "")
	for _, n := range []string{"h", "help"} {
		fs.BoolVar(&f.help, n, false, "")
	}
	return fs, f
}

// modelsCmd is "keel models": it prints a models file made from LiteLLM's
// model list and the models installed in Ollama, or with --diff how that
// differs from the saved one. An Ollama that can't be reached is noted on
// errW and left out. now dates the file's header.
func modelsCmd(args []string, w, errW io.Writer, now time.Time) error {
	flags, f := newModelsFlags()
	patterns, err := parseArgs(flags, args)
	if err != nil {
		return err
	}
	if f.help {
		fmt.Fprint(w, helpText)
		return nil
	}

	kc, err := config.LoadKeelConfig(f.config)
	if err != nil {
		return err
	}
	ctx := context.Background()
	data, err := models.Fetch(ctx, f.source)
	if err != nil {
		return err
	}
	all, pricingPages, err := models.FromLiteLLM(data)
	if err != nil {
		return err
	}
	ollamaHost := kc.Providers.Ollama.Host
	local, err := models.FromOllama(ctx, ollamaHost)
	if err != nil {
		fmt.Fprintf(errW, "keel: no Ollama models: %v\n", err)
	}
	maps.Copy(all, local)
	selected, unmatchedPatterns, err := models.Select(all, patterns)
	if err != nil {
		return usageError{err.Error()}
	}
	if len(unmatchedPatterns) > 0 {
		return fmt.Errorf("no model matches %s", strings.Join(unmatchedPatterns, ", "))
	}

	if !f.diff {
		header := []string{
			fmt.Sprintf("keel models file: made by keel models on %s from", now.Format(time.DateOnly)),
			"  " + f.source,
			"Prices in USD per 1M tokens; K = 1,000 tokens, M = 1,000,000.",
			"Check it before saving it; keel models --diff shows what changed.",
		}
		var pages []string
		hasOllama := false
		for name := range selected {
			provider, _, _ := strings.Cut(name, ":")
			if page := pricingPages[provider]; page != "" && !slices.Contains(pages, page) {
				pages = append(pages, page)
			}
			hasOllama = hasOllama || provider == "ollama"
		}
		slices.Sort(pages)
		if len(pages) > 0 {
			header = append(header, "Providers' prices:")
			for _, p := range pages {
				header = append(header, "  "+p)
			}
		}
		if hasOllama {
			header = append(header, "Ollama models: the ones installed at "+ollamaHost+"; free.")
		}
		return models.Write(w, selected, header)
	}

	saved, err := models.Load(config.ModelsPath())
	if err != nil {
		return err
	}
	if len(patterns) > 0 { // compare like with like
		saved, _, _ = models.Select(saved, patterns)
	}
	lines := models.Diff(saved, selected)
	if len(lines) == 0 {
		fmt.Fprintln(w, "no changes")
		return nil
	}
	_, err = fmt.Fprintln(w, strings.Join(lines, "\n"))
	return err
}
