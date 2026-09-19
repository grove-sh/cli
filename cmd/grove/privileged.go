package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/grove-sh/cli/internal/platform"
	"github.com/grove-sh/cli/internal/shell"
)

// A terminal is asked, a pipe is told, and --yes is the answer given in
// advance. Nothing privileged runs without one of the last two, because editing
// a machine's firewall or sysctls is not a thing to do behind someone's back.
type elevation struct {
	yes bool
	in  io.Reader
	out io.Writer
	err io.Writer

	// Builds the process for one step, or nil when this machine has no way to
	// elevate, in which case the steps are printed and left to the reader.
	elevate func(shell.Step) *exec.Cmd
}

func elevationFor(yes bool, in io.Reader, out, err io.Writer) elevation {
	return elevation{yes: yes, in: in, out: out, err: err, elevate: viaSudo()}
}

// Root runs the steps as written; anyone else goes through sudo, which is what
// the trust store install already did a moment earlier, so its cached
// credentials usually carry these without a second prompt.
func viaSudo() func(shell.Step) *exec.Cmd {
	if os.Geteuid() == 0 {
		return func(step shell.Step) *exec.Cmd { return exec.Command(step[0], step[1:]...) }
	}
	if _, err := exec.LookPath("sudo"); err != nil {
		return nil
	}
	return func(step shell.Step) *exec.Cmd {
		return exec.Command("sudo", append([]string{"--"}, step...)...)
	}
}

// The two-space indent and the sudo prefix are load-bearing: CI greps them
// out of install's output and runs the lines, so they are what a person
// pastes as well as what grove runs.
func (e elevation) prefix() string {
	if os.Geteuid() == 0 {
		return "  "
	}
	return "  sudo "
}

// Offer prints the plan and then either runs it or says whose job it is.
// Applied is true only when every step ran.
func (e elevation) offer(plan platform.Plan) (applied bool, err error) {
	if plan.Empty() {
		return false, nil
	}
	paint := styles(e.out)
	fmt.Fprintf(e.out, "\n%s\n\n", plan.Intro)
	for _, step := range plan.Steps {
		fmt.Fprintln(e.out, paint.cmd(e.prefix()+step.String()))
	}
	if plan.Outro != "" {
		fmt.Fprintf(e.out, "\n%s\n", plan.Outro)
	}

	switch {
	case e.elevate == nil:
		fmt.Fprintf(e.out, "\n%s\n", paint.dim("sudo is not on PATH, so these are yours to run."))
		return false, nil
	case e.yes:
	case !e.interactive():
		fmt.Fprintf(e.out, "\n%s\n", paint.dim("Not a terminal, so grove printed them rather than asking. Pass --yes and it runs them."))
		return false, nil
	case !e.consent(paint):
		fmt.Fprintf(e.out, "%s\n", paint.dim("Left for you to run."))
		return false, nil
	}
	fmt.Fprintln(e.out)
	return true, e.apply(plan, paint)
}

// Both ends, since a prompt nobody can see waits forever and a prompt nobody
// can answer is worse than none.
func (e elevation) interactive() bool {
	return isTerminal(e.out) && isTerminal(e.in)
}

func (e elevation) consent(paint palette) bool {
	fmt.Fprintf(e.out, "\n%s sudo may ask for your password. [y/N] ", paint.bold("Run them now?"))
	answer, _ := bufio.NewReader(e.in).ReadString('\n')
	// Enter echoed a line break; an EOF did not, and what follows should not
	// land on the question.
	if !strings.HasSuffix(answer, "\n") {
		fmt.Fprintln(e.out)
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return true
	}
	return false
}

// Stops at the first failure and prints what is left, because the steps are
// ordered so that every prefix leaves the machine loadable, and the reader
// finishing by hand needs to know where grove got to.
func (e elevation) apply(plan platform.Plan, paint palette) error {
	for i, step := range plan.Steps {
		fmt.Fprintln(e.out, paint.dim(e.prefix()+step.String()))
		cmd := e.elevate(step)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, e.out, e.err
		if err := cmd.Run(); err != nil {
			said := fmt.Sprintf("%s failed: %v", strings.TrimSpace(e.prefix()+step.String()), err)
			if rest := plan.Steps[i+1:]; len(rest) > 0 {
				lines := make([]string, len(rest))
				for j, step := range rest {
					lines[j] = e.prefix() + step.String()
				}
				said += "\n\nStill to run, in this order:\n\n" + strings.Join(lines, "\n")
			}
			return bareError{said}
		}
	}
	return nil
}
