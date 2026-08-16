package upgrader

import (
	"context"
	"fmt"
)

// AutoUpgradeExecutor performs the local download, checksum verification, and
// upgrade after Maintain has assigned a P3 task. The executor must report its
// intermediate stages through the supplied callback and return the final P3
// status it actually reached.
type AutoUpgradeExecutor interface {
	ExecuteAutoUpgrade(ctx context.Context, task UpgradeTask, ticket DownloadTicket, checksumSHA256 string, reportProgress func(UpgradeProgress) error) (UpgradeResult, error)
}

// AutoUpgradeAgent coordinates P4.4 version hints with the P3 task protocol.
// It deliberately owns no process-restart or container-specific behavior; that
// responsibility remains with the local supervisor injected as Executor.
type AutoUpgradeAgent struct {
	Client   *Client
	Platform string
	Arch     string
	Executor AutoUpgradeExecutor
}

// RunOnce performs one periodic automatic-upgrade check. It returns true only
// when a task was claimed and handed to the local executor. A version-check
// result without an authenticated auto_upgrade hint never polls P3.
func (a *AutoUpgradeAgent) RunOnce(ctx context.Context, currentVersion, channel string) (bool, error) {
	if a.Client == nil {
		return false, fmt.Errorf("auto upgrade client is required")
	}

	platform, arch := a.Platform, a.Arch
	if platform == "" {
		platform = "linux"
	}
	if arch == "" {
		arch = "amd64"
	}
	check, err := a.Client.CheckDistribution(ctx, currentVersion, channel, platform, arch)
	if err != nil {
		return false, fmt.Errorf("check distribution: %w", err)
	}
	if !check.HasUpdate || !check.AutoUpgrade {
		return false, nil
	}
	if a.Executor == nil {
		return false, fmt.Errorf("auto upgrade executor is required")
	}
	if check.Release == nil || check.Release.SHA256 == "" {
		return false, fmt.Errorf("auto upgrade hint lacks a local artifact checksum")
	}

	task, err := a.Client.PollUpgradeTask(ctx)
	if err != nil {
		return false, fmt.Errorf("poll upgrade task: %w", err)
	}
	if task == nil {
		return false, nil
	}
	if task.ToVersion != check.Release.Version {
		err := fmt.Errorf("claimed task version %q does not match checked artifact version %q", task.ToVersion, check.Release.Version)
		if reportErr := a.Client.ReportUpgradeResult(ctx, task.TaskID, UpgradeResult{Status: "failed", Error: err.Error()}); reportErr != nil {
			return true, fmt.Errorf("%w; report task failure: %v", err, reportErr)
		}
		return true, err
	}

	ticket, err := a.Client.CreateDownloadTicket(ctx, task.ToVersion, platform, arch)
	if err != nil {
		reportErr := a.Client.ReportUpgradeResult(ctx, task.TaskID, UpgradeResult{Status: "failed", Error: fmt.Sprintf("request download ticket: %v", err)})
		if reportErr != nil {
			return true, fmt.Errorf("request download ticket: %w; report task failure: %v", err, reportErr)
		}
		return true, fmt.Errorf("request download ticket: %w", err)
	}

	result, execErr := a.Executor.ExecuteAutoUpgrade(ctx, *task, *ticket, check.Release.SHA256, func(progress UpgradeProgress) error {
		return a.Client.ReportUpgradeProgress(ctx, task.TaskID, progress)
	})
	if execErr != nil && result.Status == "" {
		result = UpgradeResult{Status: "failed", Error: execErr.Error()}
	}
	if result.Status == "" {
		result = UpgradeResult{Status: "failed", Error: "executor returned no terminal status"}
	}
	if err := a.Client.ReportUpgradeResult(ctx, task.TaskID, result); err != nil {
		if execErr != nil {
			return true, fmt.Errorf("auto upgrade execution failed: %w; report task result: %v", execErr, err)
		}
		return true, fmt.Errorf("report task result: %w", err)
	}
	if execErr != nil {
		return true, fmt.Errorf("auto upgrade execution failed: %w", execErr)
	}
	return true, nil
}
