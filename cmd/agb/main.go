// Command agb is AgentBridge: one picker and shared setup for OpenCode,
// Claude Code and Codex on the machine.
package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/sergio-gimenez/opencode-sessions/internal/ocs"
)

// Set by the release build (-ldflags "-X main.version=v2.0.0"). A plain
// `go build` leaves it empty and falls back to what the toolchain recorded.
var version string

func versionString() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}

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
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "herdr":
			return ocs.RunHerdrCommand(os.Args[2:], os.Stdin, os.Stdout)
		case "help":
			ocs.PrintGuide(os.Stdout)
			return 0, nil
		case "version", "--version", "-V":
			fmt.Println("agb", versionString())
			return 0, nil
		case "experiment":
			return ocs.RunExperimentCommand(os.Args[2:], os.Stdout)
		case "limits":
			return ocs.RunUsageCommand(os.Args[2:], os.Stdout)
		case "setup", "plan", "sync":
			return ocs.RunSetupCommand(os.Args[1], os.Args[2:], os.Stdout)
		case "push":
			return ocs.RunPushCommand(os.Args[2:], os.Stdout)
		case "new":
			return runNew(os.Args[2:])
		case "pull":
			return ocs.RunPullCommand(os.Args[2:], os.Stdout)
		case "open":
			return runOpen(os.Args[2:])
		}
	}
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
		if args.JSON {
			err = ocs.WriteListing(out, sessions[:min(200, len(sessions))], ocs.ListingOpenPID())
		} else {
			ocs.PrintSessions(out, sessions[:min(25, len(sessions))])
		}
		if flushErr := out.Flush(); err == nil {
			err = flushErr
		}
		<-saved
		// `agb --print | head` closes the pipe early; that is the caller
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
		Usage:           ocs.CachedUsage(config),
		RefreshUsage: func() ocs.UsageReport {
			return ocs.CurrentUsage(config, false)
		},
		Layout: ocs.LoadLayout(ocs.LayoutPath()),
		Config: config,
		Hosts:  config.OtherHosts(),
		Self:   ocs.MachineName(),
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
	if picked.PulledFrom != "" {
		fmt.Printf("Pulled %s %s from %s.\n", ocs.AccountLabel(picked.Session.Source, picked.Session.Account), picked.Session.Title, picked.PulledFrom)
	}
	skip := skipFor(picked.Target)
	if picked.SkipPermissions != nil {
		skip = *picked.SkipPermissions
	}
	if picked.Secret != "" {
		return openSecret(config, picked.Session.Directory, picked.Secret, skip)
	}
	if picked.Mode == ocs.ModeNew {
		return ocs.OpenNew(picked.Session.Directory, picked.Target, ocs.OpenOptions{SkipPermissions: skip})
	}
	return ocs.OpenWith(picked.Session, picked.Target, ocs.OpenOptions{
		SkipPermissions: skip,
		Fork:            picked.Mode == ocs.ModeFork,
	})
}

func runNew(argv []string) (int, error) {
	directory, args, err := ocs.ParseNewArgs(argv)
	if err != nil {
		return 2, err
	}
	if args.Help {
		ocs.PrintHelp(os.Stdout)
		return 0, nil
	}
	if args.Secret {
		config := ocs.LoadConfig()
		return runNewSecret(config, directory, args, func(target ocs.Account) bool {
			return config.DefaultSkipPermissions(args.SkipPermissions, target)
		})
	}
	if directory == "" {
		directory, err = os.Getwd()
		if err != nil {
			return 1, err
		}
	}
	config := ocs.LoadConfig()
	target, err := resolveTarget(config, args.Target)
	if err != nil {
		return 1, err
	}
	skipFor := func(target ocs.Account) bool { return config.DefaultSkipPermissions(args.SkipPermissions, target) }
	var picked ocs.PickResult
	if target == nil {
		picked, err = ocs.PickSession(nil, "", ocs.PickOptions{Targets: config.Targets(), StartNew: true, WorkDirectory: directory, SkipPermissions: skipFor})
		if err != nil {
			return 1, err
		}
	} else {
		picked = ocs.PickResult{Session: ocs.Session{Directory: directory}, Target: *target, Mode: ocs.ModeNew}
	}
	skip := skipFor(picked.Target)
	if picked.SkipPermissions != nil {
		skip = *picked.SkipPermissions
	}
	return ocs.OpenNew(picked.Session.Directory, picked.Target, ocs.OpenOptions{SkipPermissions: skip})
}

// runNewSecret starts a secret conversation on --model, or opens the chooser
// with Ctrl+S on to pick one. Without --dir it starts in a scratch directory,
// not the current one: a secret conversation is usually not about a project.
func runNewSecret(config ocs.Config, directory string, args ocs.Options, skipFor func(ocs.Account) bool) (int, error) {
	skip := skipFor(config.Opencode)
	if args.SecretModel != "" {
		return openSecret(config, directory, args.SecretModel, skip)
	}
	picked, err := ocs.PickSession(nil, "", ocs.PickOptions{Targets: config.Targets(), StartNew: true, StartSecret: true,
		WorkDirectory: directory, SkipPermissions: skipFor, Config: config})
	if err != nil {
		return 1, err
	}
	if picked.SkipPermissions != nil {
		skip = *picked.SkipPermissions
	}
	return openSecret(config, picked.Session.Directory, picked.Secret, skip)
}

// openSecret checks the model again, whoever chose it, before starting.
func openSecret(config ocs.Config, directory, id string, skip bool) (int, error) {
	model, err := ocs.FindSecretModel(ocs.SecretModels(config, time.Now()), id)
	if err != nil {
		return 1, err
	}
	return ocs.OpenSecret(directory, model, ocs.OpenOptions{SkipPermissions: skip})
}

// runOpen is Enter in the picker without the picker, so another program (a
// launcher button, a script) can resume a session it found with --print.
func runOpen(argv []string) (int, error) {
	if ocs.ParseArgs(argv).Help {
		ocs.PrintHelp(os.Stdout)
		return 0, nil
	}
	id, fork, args, err := ocs.ParseOpenArgs(argv)
	if err != nil {
		return 2, err
	}
	config := ocs.LoadConfig()
	target, err := resolveTarget(config, args.Target)
	if err != nil {
		return 1, err
	}

	cache := ocs.OpenCache(ocs.CachePath(), args.Rescan)
	sessions := cache.AllSessions(config, args.Search)
	_ = cache.Save()
	session, err := ocs.FindSession(sessions, id)
	if err != nil {
		return 1, err
	}

	mode := ocs.ModeResume
	if fork {
		mode = ocs.ModeFork
	}
	picked := ocs.EnterDestination(session, target, mode)
	return ocs.OpenWith(picked.Session, picked.Target, ocs.OpenOptions{
		SkipPermissions: config.DefaultSkipPermissions(args.SkipPermissions, picked.Target),
		Fork:            fork,
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
