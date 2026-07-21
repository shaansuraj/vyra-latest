package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type relayInstanceGuard struct {
	lockFile *os.File
	lockPath string
	pidPath  string
}

type relayPIDMetadata struct {
	PID         int    `json:"pid"`
	StartedAt   string `json:"started_at"`
	Command     string `json:"command"`
	LauncherTag string `json:"launcher_tag"`
}

func acquireRelayInstanceGuard(appDir string, argv []string, launcherTag string) (*relayInstanceGuard, error) {
	runDir := filepath.Join(appDir, "run")
	if err := os.MkdirAll(runDir, 0755); err != nil {
		return nil, fmt.Errorf("create run dir: %w", err)
	}

	lockPath := filepath.Join(runDir, "relay.lock")
	pidPath := filepath.Join(runDir, "relay.pid")
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}

	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lockFile.Close()
		meta, _ := readRelayPIDMetadata(pidPath)
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, errors.New(alreadyRunningMessage(meta))
		}
		return nil, fmt.Errorf("acquire relay lock: %w", err)
	}

	guard := &relayInstanceGuard{
		lockFile: lockFile,
		lockPath: lockPath,
		pidPath:  pidPath,
	}

	meta := relayPIDMetadata{
		PID:         os.Getpid(),
		StartedAt:   time.Now().UTC().Format(time.RFC3339),
		Command:     strings.Join(argv, " "),
		LauncherTag: strings.TrimSpace(launcherTag),
	}
	if meta.LauncherTag == "" {
		meta.LauncherTag = "direct"
	}
	if err := writeRelayPIDMetadata(pidPath, meta); err != nil {
		_ = guard.Release()
		return nil, fmt.Errorf("write pid metadata: %w", err)
	}

	return guard, nil
}

func (g *relayInstanceGuard) Release() error {
	if g == nil || g.lockFile == nil {
		return nil
	}
	_ = os.Remove(g.pidPath)
	_ = syscall.Flock(int(g.lockFile.Fd()), syscall.LOCK_UN)
	closeErr := g.lockFile.Close()
	_ = os.Remove(g.lockPath)
	g.lockFile = nil
	if closeErr != nil {
		return fmt.Errorf("close lock file: %w", closeErr)
	}
	return nil
}

func writeRelayPIDMetadata(path string, meta relayPIDMetadata) error {
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0644)
}

func readRelayPIDMetadata(path string) (relayPIDMetadata, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return relayPIDMetadata{}, err
	}
	var meta relayPIDMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return relayPIDMetadata{}, err
	}
	return meta, nil
}

func alreadyRunningMessage(meta relayPIDMetadata) string {
	if meta.PID > 0 {
		started := meta.StartedAt
		if strings.TrimSpace(started) == "" {
			started = "unknown"
		}
		tag := strings.TrimSpace(meta.LauncherTag)
		if tag == "" {
			tag = "unknown"
		}
		return fmt.Sprintf(
			"relay is already running (pid=%d, started_at=%s, launcher=%s). Stop it with `scripts/vyractl.sh stop` or `kill %d`.",
			meta.PID,
			started,
			tag,
			meta.PID,
		)
	}
	return "relay is already running (lock is held). Stop it with `scripts/vyractl.sh stop`."
}
