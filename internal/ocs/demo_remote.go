package ocs

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// dialRemote is the other machine agb push and pull talk to: an ssh host, or,
// for the recorded demos, a directory named in AGB_DEMO_REMOTES
// ("desk=/path/to/fake/home,..."). Output of copies and hooks goes to out.
func dialRemote(host string, out io.Writer) remote {
	for _, entry := range strings.Split(os.Getenv("AGB_DEMO_REMOTES"), ",") {
		if name, root, ok := strings.Cut(entry, "="); ok && name == host && root != "" {
			return demoRemote{home: homeDir(), root: filepath.Clean(root), out: out}
		}
	}
	return sshRemote{host: host, out: out}
}

// demoRemote stands a directory in for another machine's home, so the demos
// push and pull for real without a second machine. Paths under this home map
// to the same paths under root; scripts run there with HOME=root and report
// this home back, as a real machine of the same user would.
type demoRemote struct {
	home, root string
	out        io.Writer
}

func (d demoRemote) there(path string) string {
	if rel, ok := strings.CutPrefix(path, d.home); ok {
		return d.root + rel
	}
	return path
}

func (d demoRemote) shell(script string, stdout io.Writer) error {
	cmd := exec.Command("bash", "-s")
	cmd.Dir = d.root
	cmd.Stdin = strings.NewReader(strings.ReplaceAll(script, d.home, d.root))
	cmd.Env = append(os.Environ(), "HOME="+d.root, "PATH="+filepath.Join(d.root, ".local", "bin")+":"+os.Getenv("PATH"))
	var raw bytes.Buffer
	cmd.Stdout, cmd.Stderr = &raw, io.Discard
	err := cmd.Run()
	_, _ = io.WriteString(stdout, strings.ReplaceAll(raw.String(), d.root, d.home))
	return err
}

func (d demoRemote) Probe(script string) (string, error) {
	var out bytes.Buffer
	err := d.shell(script, &out)
	return out.String(), err
}

func (d demoRemote) Run(command string) error { return d.shell(command, d.out) }

func (d demoRemote) rsync(src, dst string, extra []string) error {
	if isDir(src) {
		src, dst = strings.TrimSuffix(src, "/")+"/", strings.TrimSuffix(dst, "/")+"/"
	}
	args := append([]string{"-a", "--mkpath"}, extra...)
	cmd := exec.Command("rsync", append(args, "--", src, dst)...)
	cmd.Stdout, cmd.Stderr = d.out, d.out
	return cmd.Run()
}

func (d demoRemote) Copy(paths []string, extra ...string) error {
	for _, path := range paths {
		if err := d.rsync(path, d.there(path), extra); err != nil {
			return err
		}
	}
	return nil
}

func (d demoRemote) CopyTo(src, dst string, extra ...string) error {
	return d.rsync(src, d.there(dst), extra)
}

func (d demoRemote) Fetch(src, dst string, extra ...string) error {
	return d.rsync(d.there(src), dst, extra)
}
