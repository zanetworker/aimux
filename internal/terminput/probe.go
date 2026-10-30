package terminput

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const probeTimeout = 2 * time.Second

// SystemProbe is the real System, backed by ps and ioreg.
type SystemProbe struct{}

func run(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output() // #nosec G204 -- fixed commands, integer or ps-reported args
	return string(out), err
}

// TTY returns the pid's controlling tty such as "/dev/ttys001", or "" if none.
func (SystemProbe) TTY(pid int) (string, error) {
	out, err := run("ps", "-o", "tty=", "-p", strconv.Itoa(pid))
	if err != nil {
		return "", err
	}
	return parseTTY(out)
}

// TTYs returns the controlling ttys of pids that have one, from one ps.
func (SystemProbe) TTYs(pids []int) (map[int]string, error) { return ttysFrom(run, pids) }

// ttysFrom runs one `ps -o pid=,tty= -p p1,p2,...` through run. ps exits 1
// when some pid is gone but still lists the others.
func ttysFrom(run func(string, ...string) (string, error), pids []int) (map[int]string, error) {
	out := map[int]string{}
	if len(pids) == 0 {
		return out, nil
	}
	list := make([]string, len(pids))
	for i, p := range pids {
		list[i] = strconv.Itoa(p)
	}
	text, err := run("ps", "-o", "pid=,tty=", "-p", strings.Join(list, ","))
	if err != nil && strings.TrimSpace(text) == "" {
		return nil, err
	}
	for _, line := range strings.Split(text, "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		pid, perr := strconv.Atoi(f[0])
		tty, _ := parseTTY(f[1])
		if perr == nil && tty != "" {
			out[pid] = tty
		}
	}
	return out, nil
}

// foregroundPGID is the tty's foreground process group id.
func foregroundPGID(tty string) (int, error) {
	out, err := run("ps", "-o", "tpgid=", "-t", strings.TrimPrefix(tty, "/dev/"))
	if err != nil {
		return 0, err
	}
	return parseInt(firstLine(out))
}

// ForegroundIsPID reports whether the tty's foreground process group is pid's.
func (SystemProbe) ForegroundIsPID(tty string, pid int) (bool, error) {
	tpgid, err := foregroundPGID(tty)
	if err != nil {
		return false, err
	}
	out, err := run("ps", "-o", "pgid=", "-p", strconv.Itoa(pid))
	if err != nil {
		return false, err
	}
	pgid, err := parseInt(out)
	if err != nil {
		return false, err
	}
	return tpgid == pgid, nil
}

// ForegroundCommand is the command of the tty's foreground process group
// leader, as ps reports it (a name or a path).
func (SystemProbe) ForegroundCommand(tty string) (string, error) {
	tpgid, err := foregroundPGID(tty)
	if err != nil {
		return "", err
	}
	out, err := run("ps", "-o", "comm=", "-p", strconv.Itoa(tpgid))
	if err != nil {
		return "", err
	}
	return parseComm(out)
}

func parseComm(out string) (string, error) {
	c := strings.TrimSpace(firstLine(out))
	if c == "" {
		return "", fmt.Errorf("no command")
	}
	return c, nil
}

// IdleSeconds is the time since the last keyboard or mouse input.
func (SystemProbe) IdleSeconds() (float64, error) {
	out, err := run("ioreg", "-c", "IOHIDSystem")
	if err != nil {
		return 0, err
	}
	return parseHIDIdle(out)
}

func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			return l
		}
	}
	return ""
}

func parseTTY(out string) (string, error) {
	t := strings.TrimSpace(out)
	if t == "" || t == "??" || t == "-" {
		return "", nil
	}
	return "/dev/" + t, nil
}

func parseInt(out string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, fmt.Errorf("parse %q: %w", strings.TrimSpace(out), err)
	}
	return n, nil
}

var hidIdle = regexp.MustCompile(`"HIDIdleTime"\s*=\s*(\d+)`)

func parseHIDIdle(out string) (float64, error) {
	m := hidIdle.FindStringSubmatch(out)
	if m == nil {
		return 0, fmt.Errorf("HIDIdleTime not found")
	}
	ns, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, err
	}
	return ns / 1e9, nil
}
