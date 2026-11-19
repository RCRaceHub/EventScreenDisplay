package browser

import (
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

// IdleArg is the hidden subcommand the dummy browser runs: the agent's own
// binary, waiting to be killed.
const IdleArg = "__idle"

// idleCommand prefers "sleep infinity" (so tests, whose binary is the test
// runner, never re-run themselves) and falls back to the agent's own binary
// where there is no sleep, as in the scratch Docker image.
func idleCommand() ([]string, error) {
	if sleep, err := exec.LookPath("sleep"); err == nil {
		return []string{sleep, "infinity"}, nil
	}
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return []string{self, IdleArg}, nil
}

// Idle blocks until the process is told to stop. main calls it for IdleArg.
func Idle() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM, os.Interrupt)
	<-ch
}
