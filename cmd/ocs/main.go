// Command ocs is one picker for every OpenCode, Claude Code and Codex session
// on the machine.
package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"

	"github.com/sergio-gimenez/opencode-sessions/internal/ocs"
)

func resolveTarget(config ocs.Config, requested string) (*ocs.Account, error) {
	if requested == "" {
		return nil, nil
	}
	if requested == "opencode" {
		requested = "oc"
	}

	targets := config.Targets()
	names := make([]string, len(targets))
	for i, target := range targets {
		if target.Name == requested {
			return &targets[i], nil
		}
		names[i] = target.Name
	}
	return nil, fmt.Errorf("Unknown target %q. Configured: %s.", requested, strings.Join(names, ", "))
}

func run() (int, error) {
	args := ocs.ParseArgs(os.Args[1:])
	if args.Help {
		ocs.PrintHelp(os.Stdout)
		return 0, nil
	}

	config := ocs.LoadConfig()
	initialTarget, err := resolveTarget(config, args.Target)
	if err != nil {
		return 1, err
	}

	cache := ocs.OpenCache(ocs.CachePath(), args.Rescan)
	sessions := cache.AllSessions(config, args.Search)

	// Writing the cache overlaps with the picker; it only has to be done
	// before this process is replaced by the tool it opens.
	saved := make(chan struct{})
	save := func() {
		go func() {
			defer close(saved)
			_ = cache.Save()
		}()
	}

	if args.Print {
		save()
		out := bufio.NewWriter(os.Stdout)
		limit := min(25, len(sessions))
		ocs.PrintSessions(out, sessions[:limit])
		err := out.Flush()
		<-saved
		// `ocs --print | head` closes the pipe early; that is the caller
		// getting what they asked for, not a failure.
		if errors.Is(err, syscall.EPIPE) {
			return 0, nil
		}
		return 0, err
	}

	skipFor := func(target ocs.Account) bool {
		return config.DefaultSkipPermissions(args.SkipPermissions, target)
	}

	picked, err := ocs.PickSession(sessions, args.Query, ocs.PickOptions{
		Targets:         config.Targets(),
		InitialTarget:   initialTarget,
		SkipPermissions: skipFor,
		AfterFirstDraw:  save,
		Layout:          ocs.LoadLayout(ocs.LayoutPath()),
		SaveLayout: func(layout ocs.Layout) {
			_ = ocs.SaveLayout(ocs.LayoutPath(), layout)
		},
	})
	if err != nil {
		if errors.Is(err, ocs.ErrCancelled) {
			<-saved
		}
		return 1, err
	}

	<-saved
	skip := skipFor(picked.Target)
	if picked.SkipPermissions != nil {
		skip = *picked.SkipPermissions
	}
	return ocs.OpenWith(picked.Session, picked.Target, ocs.OpenOptions{
		SkipPermissions: skip,
		Fork:            picked.Mode == ocs.ModeFork,
	})
}

func main() {
	code, err := run()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}
