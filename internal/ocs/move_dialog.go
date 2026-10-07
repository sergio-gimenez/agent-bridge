package ocs

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// The picker's Ctrl+O ends here: a few plain prompts on the normal terminal,
// then the same checks and copy as `agb push`.

// sshConfigHosts lists the concrete Host aliases of an ssh config file and the
// files it Includes, in file order, without patterns (*, ?, !).
func sshConfigHosts(path string) []string {
	seen := map[string]bool{}
	var hosts []string
	var read func(path string, depth int)
	read = func(path string, depth int) {
		raw, err := os.ReadFile(path)
		if err != nil || depth > 4 {
			return
		}
		for _, line := range strings.Split(string(raw), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			switch strings.ToLower(fields[0]) {
			case "host":
				for _, name := range fields[1:] {
					if !strings.ContainsAny(name, "*?!") && !seen[name] {
						seen[name] = true
						hosts = append(hosts, name)
					}
				}
			case "include":
				for _, pattern := range fields[1:] {
					pattern = expandHome(pattern)
					if !filepath.IsAbs(pattern) {
						pattern = filepath.Join(filepath.Dir(path), pattern)
					}
					matches, _ := filepath.Glob(pattern)
					for _, match := range matches {
						read(match, depth+1)
					}
				}
			}
		}
	}
	read(path, 0)
	return hosts
}

// remoteDirCandidates asks the other machine where the session's project is:
// the same path if it exists ("same"), else checkouts of the same git origin
// up to four levels below home ("repo").
func remoteDirCandidates(link remote, session Session) (same string, repos []string, err error) {
	origin, _ := gitOutput(session.Directory, "remote", "get-url", "origin")
	script := fmt.Sprintf(`d=%s; url=%s
if [ -d "$d" ]; then echo "same=$d"; fi
if [ -n "$url" ]; then
  find "$HOME" -maxdepth 4 -name .git -prune 2>/dev/null | while read -r g; do
    p=${g%%/.git}
    [ "$(git -C "$p" remote get-url origin 2>/dev/null)" = "$url" ] && echo "repo=$p"
  done | head -5
fi
`, shellQuote(session.Directory), shellQuote(origin))
	out, err := link.Probe(script)
	if err != nil {
		return "", nil, err
	}
	for _, line := range strings.Split(out, "\n") {
		if value, ok := strings.CutPrefix(line, "same="); ok {
			same = value
		} else if value, ok := strings.CutPrefix(line, "repo="); ok && value != session.Directory {
			repos = append(repos, value)
		}
	}
	return same, repos, nil
}

type moveDialog struct {
	in  *bufio.Reader
	out io.Writer
}

func (d moveDialog) ask(prompt string) (string, error) {
	fmt.Fprint(d.out, prompt)
	line, err := d.in.ReadString('\n')
	if err != nil && line == "" {
		return "", ErrCancelled
	}
	return strings.TrimSpace(line), nil
}

// choose reads a number into options, or any other text as typed. An empty
// answer takes def.
func (d moveDialog) choose(prompt string, options []string, def string) (string, error) {
	answer, err := d.ask(prompt)
	if err != nil {
		return "", err
	}
	if answer == "" {
		return def, nil
	}
	if n, err := strconv.Atoi(answer); err == nil && n >= 1 && n <= len(options) {
		return options[n-1], nil
	}
	return answer, nil
}

func (d moveDialog) pickHost(configured, sshHosts []string) (string, error) {
	fmt.Fprintln(d.out, bold("Push to which machine?"))
	for i, host := range configured {
		fmt.Fprintf(d.out, "  %d) %s\n", i+1, host)
	}
	if len(sshHosts) > 0 {
		fmt.Fprintf(d.out, "  s) pick from ~/.ssh/config (%d hosts)\n", len(sshHosts))
	}
	fmt.Fprintln(d.out, "  or type any ssh host")
	def, prompt := "", "Host: "
	if len(configured) > 0 {
		def, prompt = configured[0], fmt.Sprintf("Host [%s]: ", configured[0])
	}
	for {
		host, err := d.choose(prompt, configured, def)
		if err != nil {
			return "", err
		}
		if host == "" {
			continue
		}
		if host != "s" || len(sshHosts) == 0 {
			return host, nil
		}
		filter, err := d.ask("Filter (part of the name, Enter for all): ")
		if err != nil {
			return "", err
		}
		var matches []string
		for _, h := range sshHosts {
			if strings.Contains(strings.ToLower(h), strings.ToLower(filter)) {
				matches = append(matches, h)
			}
		}
		if len(matches) == 0 {
			fmt.Fprintln(d.out, "  no host matches")
			continue
		}
		if len(matches) > 40 {
			fmt.Fprintf(d.out, "  %d match; showing 40, filter more to narrow\n", len(matches))
			matches = matches[:40]
		}
		for i, h := range matches {
			fmt.Fprintf(d.out, "  %2d) %s\n", i+1, h)
		}
		host, err = d.choose("Host (number or name): ", matches, "")
		if err != nil {
			return "", err
		}
		if host != "" {
			return host, nil
		}
	}
}

func (d moveDialog) pickDir(host string, session Session, same string, repos []string) (string, error) {
	switch {
	case same != "":
		answer, err := d.ask(fmt.Sprintf("Directory on %s [%s]: ", host, ShortenHome(same)))
		if err != nil || answer == "" {
			return same, err
		}
		return expandHome(answer), nil
	case len(repos) > 0:
		fmt.Fprintf(d.out, "%s is not on %s. Checkouts of the same repository there:\n", ShortenHome(session.Directory), host)
		shown := make([]string, len(repos))
		for i, repo := range repos {
			shown[i] = ShortenHome(repo)
			fmt.Fprintf(d.out, "  %d) %s\n", i+1, shown[i])
		}
		answer, err := d.choose(fmt.Sprintf("Directory on %s [%s]: ", host, shown[0]), repos, repos[0])
		return expandHome(answer), err
	}
	for {
		answer, err := d.ask(fmt.Sprintf("%s is not on %s. Directory there: ", ShortenHome(session.Directory), host))
		if err != nil {
			return "", err
		}
		if answer != "" {
			return expandHome(answer), nil
		}
	}
}

func (d moveDialog) yes(prompt string, def bool) (bool, error) {
	answer, err := d.ask(prompt)
	if err != nil {
		return false, err
	}
	if answer == "" {
		return def, nil
	}
	return strings.HasPrefix(strings.ToLower(answer), "y"), nil
}

// RunMoveDialog asks where to, shows the checks and the copy list, and pushes
// the session after a confirmation.
func RunMoveDialog(session Session, config Config, in io.Reader, out io.Writer) (int, error) {
	return runMoveDialog(session, config, filepath.Join(homeDir(), ".ssh", "config"), in, out,
		func(host string) remote { return dialRemote(host, nil) })
}

func runMoveDialog(session Session, config Config, sshConfig string, in io.Reader, out io.Writer, dial func(string) remote) (int, error) {
	d := moveDialog{in: bufio.NewReader(in), out: out}
	fmt.Fprintf(out, "%s %s\n  %s\n  in %s\n\n", AccountLabel(session.Source, session.Account), session.Title, session.ID, ShortenHome(session.Directory))
	if _, _, _, err := accountFiles(session); err != nil {
		return 1, err
	}

	// One config serves every machine, so leave out the one this runs on.
	self, _ := os.Hostname()
	var hosts []string
	for _, host := range config.MoveHosts {
		if !strings.EqualFold(host, self) {
			hosts = append(hosts, host)
		}
	}
	host, err := d.pickHost(hosts, sshConfigHosts(sshConfig))
	if err != nil {
		return 1, err
	}
	link := dial(host)
	fmt.Fprintf(out, "Looking for the project on %s...\n", host)
	same, repos, err := remoteDirCandidates(link, session)
	if err != nil {
		return 1, err
	}
	dir, err := d.pickDir(host, session, same, repos)
	if err != nil {
		return 1, err
	}

	opts := MoveOptions{Host: host, RemoteDir: dir, DryRun: true}
	fmt.Fprintln(out)
	if code, err := moveSession(session, config, opts, link, out); code != 0 || err != nil {
		return code, err
	}
	if ok, err := d.yes("\nPush it now? [Y/n] ", true); err != nil || !ok {
		return 1, ErrCancelled
	}
	opts.DryRun = dryRunEnabled()
	return moveSession(session, config, opts, link, out)
}
