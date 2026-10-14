package ocs

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// Herdr owns right-clicks by default even when an app enables terminal mouse
// reporting. Request routing for this pane explicitly, never the focused pane
// (which may have changed since agb started).
func enableHerdrRightClick() error {
	if os.Getenv("HERDR_ENV") != "1" || os.Getenv("HERDR_PANE_ID") == "" {
		return nil
	}
	binary := os.Getenv("HERDR_BIN_PATH")
	if binary == "" {
		binary = "herdr"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, binary, "pane", "input", "--pane", os.Getenv("HERDR_PANE_ID"), "--right-click", "pane").CombinedOutput()
	if err != nil {
		return fmt.Errorf("Herdr could not forward right-clicks to agb: %w (%s). Ctrl+E opens the actions menu", err, out)
	}
	return nil
}
