package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/SadNoo/sshappy-tune/internal/automation"
	"github.com/SadNoo/sshappy-tune/internal/host"
	"github.com/SadNoo/sshappy-tune/internal/manage"
	"github.com/SadNoo/sshappy-tune/internal/runx"
	"github.com/SadNoo/sshappy-tune/internal/tune"
	"github.com/SadNoo/sshappy-tune/internal/version"
)

type App struct {
	Runner   runx.Runner
	Detector host.Detector
	Manager  manage.Manager
	Services automation.Installer
	Stdout   io.Writer
	Stderr   io.Writer
}

func New(stdout, stderr io.Writer) App {
	runner := runx.ExecRunner{}
	return App{
		Runner: runner, Detector: host.NewDetector(runner), Manager: manage.NewManager(runner), Services: automation.NewInstaller(runner),
		Stdout: stdout, Stderr: stderr,
	}
}

func (a App) Run(ctx context.Context, args []string) int {
	if len(args) == 0 {
		a.usage()
		return 2
	}
	var err error
	switch args[0] {
	case "help", "-h", "--help":
		a.usage()
		return 0
	case "version":
		fmt.Fprintf(a.Stdout, "sshappy-tune %s commit=%s buildTime=%s\n", version.Version, version.Commit, version.BuildTime)
		return 0
	case "detect":
		err = a.detect(ctx, args[1:])
	case "inspect-container":
		err = a.inspectContainer(ctx, args[1:])
	case "recommend":
		err = a.recommend(ctx, args[1:])
	case "apply":
		err = a.apply(ctx, args[1:])
	case "verify":
		err = a.verify(ctx, args[1:])
	case "rollback":
		err = a.rollback(ctx, args[1:])
	case "reconcile":
		err = a.reconcile(ctx, args[1:])
	case "service":
		err = a.service(ctx, args[1:])
	default:
		err = fmt.Errorf("unknown command %q", args[0])
	}
	if err != nil {
		fmt.Fprintf(a.Stderr, "error: %v\n", err)
		return 1
	}
	return 0
}

type ReconcileResult struct {
	Changed      bool                `json:"changed"`
	Drift        manage.Drift        `json:"drift"`
	SnapshotID   string              `json:"snapshotId,omitempty"`
	Verification manage.Verification `json:"verification"`
}

func (a App) detect(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("detect", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	jsonOutput := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("detect accepts no positional arguments")
	}
	profile, err := a.Detector.Detect(ctx)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return writeJSON(a.Stdout, profile)
	}
	printProfile(a.Stdout, profile)
	return nil
}

func (a App) inspectContainer(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("inspect-container", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	pid := fs.Int("pid", 0, "container process PID")
	jsonOutput := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *pid < 1 {
		return fmt.Errorf("usage: sshappy-tune inspect-container --pid <PID>")
	}
	values, err := a.Detector.InspectNetworkNamespace(ctx, *pid)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return writeJSON(a.Stdout, values)
	}
	for _, key := range sortedMapKeys(values) {
		fmt.Fprintf(a.Stdout, "%s = %s\n", key, values[key])
	}
	return nil
}

func (a App) recommend(ctx context.Context, args []string) error {
	input, jsonOutput, err := parseTuneFlags("recommend", args, a.Stderr)
	if err != nil {
		return err
	}
	profile, recommendation, err := a.buildRecommendation(ctx, input)
	if err != nil {
		return err
	}
	if jsonOutput {
		return writeJSON(a.Stdout, recommendation)
	}
	printRecommendation(a.Stdout, profile, recommendation)
	return nil
}

func (a App) apply(ctx context.Context, args []string) error {
	input, jsonOutput, dryRun, confirm, err := parseApplyFlags(args, a.Stderr)
	if err != nil {
		return err
	}
	profile, recommendation, err := a.buildRecommendation(ctx, input)
	if err != nil {
		return err
	}
	plan := manage.BuildPlan(profile.Sysctls, recommendation)
	if dryRun {
		if jsonOutput {
			return writeJSON(a.Stdout, plan)
		}
		printPlan(a.Stdout, plan)
		return nil
	}
	if !confirm {
		return fmt.Errorf("refusing to modify the host without --confirm; run apply --dry-run first")
	}
	result, err := a.Manager.Apply(ctx, plan)
	if jsonOutput {
		if writeErr := writeJSON(a.Stdout, result); writeErr != nil && err == nil {
			err = writeErr
		}
	} else {
		if result.SnapshotID != "" {
			fmt.Fprintf(a.Stdout, "snapshot: %s\n", result.SnapshotID)
		}
		if len(result.Verification.Checks) > 0 || len(result.Verification.Warnings) > 0 {
			printVerification(a.Stdout, result.Verification)
		}
	}
	return err
}

func (a App) verify(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	jsonOutput := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("verify accepts no positional arguments")
	}
	verification, err := a.Manager.Verify(ctx, a.Detector)
	if err != nil {
		return err
	}
	if *jsonOutput {
		if err := writeJSON(a.Stdout, verification); err != nil {
			return err
		}
	} else {
		printVerification(a.Stdout, verification)
	}
	if !verification.OK {
		return fmt.Errorf("one or more critical checks failed")
	}
	return nil
}

func (a App) rollback(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("rollback", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	snapshotID := fs.String("snapshot", "", "snapshot ID; defaults to latest")
	confirm := fs.Bool("confirm", false, "confirm host modification")
	jsonOutput := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("rollback accepts no positional arguments")
	}
	if !*confirm {
		return fmt.Errorf("refusing to modify the host without --confirm")
	}
	snapshot, err := a.Manager.Rollback(ctx, *snapshotID)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return writeJSON(a.Stdout, snapshot)
	}
	fmt.Fprintf(a.Stdout, "restored snapshot %s created at %s\n", snapshot.ID, snapshot.CreatedAt.Format("2006-01-02T15:04:05Z"))
	return nil
}

func (a App) reconcile(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("reconcile", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	confirm := fs.Bool("confirm", false, "confirm host modification when drift is found")
	jsonOutput := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("reconcile accepts no positional arguments")
	}
	if !*confirm {
		return fmt.Errorf("refusing to reconcile without --confirm")
	}
	profile, err := automation.LoadProfile(a.Services.Paths.ProfileFile)
	if err != nil {
		return err
	}
	result, err := a.reconcileInput(ctx, profile.Input)
	if *jsonOutput {
		if writeErr := writeJSON(a.Stdout, result); writeErr != nil && err == nil {
			err = writeErr
		}
	} else {
		printReconcile(a.Stdout, result)
	}
	return err
}

func (a App) reconcileInput(ctx context.Context, input tune.Input) (ReconcileResult, error) {
	hostProfile, recommendation, err := a.buildRecommendation(ctx, input)
	if err != nil {
		return ReconcileResult{}, err
	}
	plan := manage.BuildPlan(hostProfile.Sysctls, recommendation)
	drift, err := a.Manager.NeedsApply(plan)
	if err != nil {
		return ReconcileResult{}, err
	}
	result := ReconcileResult{Drift: drift}
	if !drift.Needed {
		result.Verification, err = a.Manager.Verify(ctx, a.Detector)
		return result, err
	}
	applyResult, err := a.Manager.Apply(ctx, plan)
	result.Changed = err == nil
	result.SnapshotID = applyResult.SnapshotID
	result.Verification = applyResult.Verification
	return result, err
}

func (a App) service(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: sshappy-tune service <install|status|uninstall>")
	}
	switch args[0] {
	case "install":
		return a.serviceInstall(ctx, args[1:])
	case "status":
		return a.serviceStatus(ctx, args[1:])
	case "uninstall":
		return a.serviceUninstall(ctx, args[1:])
	default:
		return fmt.Errorf("unknown service command %q", args[0])
	}
}

func (a App) serviceInstall(ctx context.Context, args []string) error {
	input, jsonOutput, confirm, err := parseServiceInstallFlags(args, a.Stderr)
	if err != nil {
		return err
	}
	if !confirm {
		return fmt.Errorf("refusing to install automation without --confirm")
	}
	profile := automation.NewProfile(input)
	if err := a.Services.PreflightInstall(profile); err != nil {
		return err
	}
	result, err := a.reconcileInput(ctx, profile.Input)
	if err != nil {
		return err
	}
	if err := a.Services.Install(ctx, profile); err != nil {
		var rollbackErr error
		if result.Changed && result.SnapshotID != "" {
			_, rollbackErr = a.Manager.Rollback(ctx, result.SnapshotID)
		}
		return errors.Join(err, rollbackErr)
	}
	if jsonOutput {
		return writeJSON(a.Stdout, result)
	}
	printReconcile(a.Stdout, result)
	fmt.Fprintln(a.Stdout, "automationInstalled=true verifyInterval=6h")
	return nil
}

func (a App) serviceStatus(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("service status", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	jsonOutput := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("service status accepts no positional arguments")
	}
	status, err := a.Services.Status(ctx)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return writeJSON(a.Stdout, status)
	}
	fmt.Fprintf(a.Stdout, "installed=%t applyUnit=%s timerUnit=%s timerActive=%s\n",
		status.Installed, emptyText(status.ApplyUnitState), emptyText(status.TimerUnitState), emptyText(status.TimerActiveState))
	if status.Installed {
		fmt.Fprintf(a.Stdout, "profile role=%s bandwidth=%dMbps rtt=%dms\n",
			status.Profile.Input.Role, status.Profile.Input.BandwidthMbps, status.Profile.Input.RTTMillis)
	}
	return nil
}

func (a App) serviceUninstall(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("service uninstall", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	confirm := fs.Bool("confirm", false, "confirm service removal")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("service uninstall accepts no positional arguments")
	}
	if !*confirm {
		return fmt.Errorf("refusing to uninstall automation without --confirm")
	}
	if err := a.Services.Uninstall(ctx); err != nil {
		return err
	}
	fmt.Fprintln(a.Stdout, "automationInstalled=false; managed sysctl settings were left unchanged")
	return nil
}

func (a App) buildRecommendation(ctx context.Context, input tune.Input) (host.Profile, tune.Recommendation, error) {
	profile, err := a.Detector.Detect(ctx)
	if err != nil {
		return host.Profile{}, tune.Recommendation{}, err
	}
	recommendation, err := tune.Recommend(profile, input)
	return profile, recommendation, err
}

func parseTuneFlags(name string, args []string, stderr io.Writer) (tune.Input, bool, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	bandwidth := fs.Int64("bandwidth", 0, "expected node bandwidth in Mbps")
	rtt := fs.Int64("rtt", 0, "representative RTT in milliseconds")
	role := fs.String("role", "proxy", "workload role (proxy)")
	jsonOutput := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return tune.Input{}, false, err
	}
	if fs.NArg() != 0 {
		return tune.Input{}, false, fmt.Errorf("unexpected positional arguments")
	}
	return tune.Input{BandwidthMbps: *bandwidth, RTTMillis: *rtt, Role: *role}, *jsonOutput, nil
}

func parseApplyFlags(args []string, stderr io.Writer) (tune.Input, bool, bool, bool, error) {
	fs := flag.NewFlagSet("apply", flag.ContinueOnError)
	fs.SetOutput(stderr)
	bandwidth := fs.Int64("bandwidth", 0, "expected node bandwidth in Mbps")
	rtt := fs.Int64("rtt", 0, "representative RTT in milliseconds")
	role := fs.String("role", "proxy", "workload role (proxy)")
	jsonOutput := fs.Bool("json", false, "print JSON")
	dryRun := fs.Bool("dry-run", false, "show the complete plan without modifying the host")
	confirm := fs.Bool("confirm", false, "confirm host modification")
	if err := fs.Parse(args); err != nil {
		return tune.Input{}, false, false, false, err
	}
	if fs.NArg() != 0 {
		return tune.Input{}, false, false, false, fmt.Errorf("unexpected positional arguments")
	}
	return tune.Input{BandwidthMbps: *bandwidth, RTTMillis: *rtt, Role: *role}, *jsonOutput, *dryRun, *confirm, nil
}

func parseServiceInstallFlags(args []string, stderr io.Writer) (tune.Input, bool, bool, error) {
	fs := flag.NewFlagSet("service install", flag.ContinueOnError)
	fs.SetOutput(stderr)
	bandwidth := fs.Int64("bandwidth", 0, "expected node bandwidth in Mbps")
	rtt := fs.Int64("rtt", 0, "representative RTT in milliseconds")
	role := fs.String("role", "proxy", "workload role (proxy)")
	jsonOutput := fs.Bool("json", false, "print JSON")
	confirm := fs.Bool("confirm", false, "confirm service installation and initial reconciliation")
	if err := fs.Parse(args); err != nil {
		return tune.Input{}, false, false, err
	}
	if fs.NArg() != 0 {
		return tune.Input{}, false, false, fmt.Errorf("unexpected positional arguments")
	}
	return tune.Input{BandwidthMbps: *bandwidth, RTTMillis: *rtt, Role: *role}, *jsonOutput, *confirm, nil
}

func writeJSON(w io.Writer, value any) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func printProfile(w io.Writer, profile host.Profile) {
	fmt.Fprintf(w, "Kernel:             %s (%s)\n", profile.Kernel, profile.Architecture)
	fmt.Fprintf(w, "CPU / memory:       %d cores / %d MB\n", profile.CPUCores, profile.MemoryMB)
	fmt.Fprintf(w, "Default interface:  %s (%d TX queues)\n", profile.DefaultInterface, profile.TXQueues)
	if profile.InterfaceSpeedMbps > 0 {
		fmt.Fprintf(w, "Interface speed:    %d Mbps\n", profile.InterfaceSpeedMbps)
	}
	fmt.Fprintf(w, "Congestion control: %s\n", profile.CongestionControl)
	fmt.Fprintf(w, "Qdisc:              root=%s fqReady=%t leaves=%s\n", profile.Qdisc.RootKind, profile.Qdisc.FQReady, strings.Join(profile.Qdisc.Leaves, ","))
	for _, warning := range profile.Warnings {
		fmt.Fprintf(w, "WARNING: %s\n", warning)
	}
}

func printRecommendation(w io.Writer, profile host.Profile, recommendation tune.Recommendation) {
	printProfile(w, profile)
	fmt.Fprintln(w)
	fmt.Fprintf(w, "Role / path:       %s / %d Mbps / %d ms\n", recommendation.Input.Role, recommendation.Input.BandwidthMbps, recommendation.Input.RTTMillis)
	fmt.Fprintf(w, "BDP:               %s\n", bytesText(recommendation.BDPBytes))
	fmt.Fprintf(w, "Target buffer:     %s\n", bytesText(recommendation.TargetBufferBytes))
	fmt.Fprintf(w, "Memory cap:        %s\n", bytesText(recommendation.MemoryCapBytes))
	fmt.Fprintf(w, "Recommended max:  %s (%s)\n", bytesText(recommendation.BufferMaxBytes), recommendation.CapReason)
	for _, warning := range recommendation.Warnings {
		fmt.Fprintf(w, "WARNING: %s\n", warning)
	}
}

func printPlan(w io.Writer, plan manage.Plan) {
	recommendation := plan.Recommendation
	fmt.Fprintf(w, "Role / path:       %s / %d Mbps / %d ms\n", recommendation.Input.Role, recommendation.Input.BandwidthMbps, recommendation.Input.RTTMillis)
	fmt.Fprintf(w, "Host memory:       %d MB\n", recommendation.MemoryMB)
	fmt.Fprintf(w, "BDP:               %s\n", bytesText(recommendation.BDPBytes))
	fmt.Fprintf(w, "Recommended max:   %s (%s)\n", bytesText(recommendation.BufferMaxBytes), recommendation.CapReason)
	for _, warning := range plan.Warnings {
		fmt.Fprintf(w, "WARNING: %s\n", warning)
	}
	fmt.Fprintln(w, "\nActions:")
	for _, action := range plan.Actions {
		fmt.Fprintf(w, "  - %s\n", action)
	}
	fmt.Fprintln(w, "\nChanges:")
	keys := make([]string, 0, len(plan.Changes))
	for key := range plan.Changes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		change := plan.Changes[key]
		marker := "="
		if change.Changed {
			marker = "->"
		}
		fmt.Fprintf(w, "  %s: %s %s %s\n", key, emptyText(change.Current), marker, change.Recommended)
	}
	fmt.Fprintln(w, "\nGenerated sysctl file:")
	fmt.Fprint(w, plan.SysctlConfig)
}

func printVerification(w io.Writer, verification manage.Verification) {
	for _, check := range verification.Checks {
		status := "OK"
		if !check.OK {
			status = "FAIL"
		}
		fmt.Fprintf(w, "%s %s expected=%q actual=%q\n", status, check.Name, check.Expected, check.Actual)
	}
	for _, warning := range verification.Warnings {
		fmt.Fprintf(w, "WARNING: %s\n", warning)
	}
	fmt.Fprintf(w, "verificationOK=%t\n", verification.OK)
}

func printReconcile(w io.Writer, result ReconcileResult) {
	fmt.Fprintf(w, "changed=%t drift=%t\n", result.Changed, result.Drift.Needed)
	for _, reason := range result.Drift.Reasons {
		fmt.Fprintf(w, "DRIFT: %s\n", reason)
	}
	if result.SnapshotID != "" {
		fmt.Fprintf(w, "snapshot: %s\n", result.SnapshotID)
	}
	if len(result.Verification.Checks) > 0 || len(result.Verification.Warnings) > 0 {
		printVerification(w, result.Verification)
	}
}

func bytesText(value int64) string {
	return fmt.Sprintf("%.2f MiB (%s bytes)", float64(value)/(1024*1024), strconv.FormatInt(value, 10))
}

func emptyText(value string) string {
	if value == "" {
		return "<missing>"
	}
	return value
}

func sortedMapKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (a App) usage() {
	fmt.Fprintln(a.Stderr, `sshappy-tune safely audits and tunes a Linux host for a proxy workload.

Usage:
  sshappy-tune detect [--json]
  sshappy-tune inspect-container --pid <PID> [--json]
  sshappy-tune recommend --bandwidth <Mbps> --rtt <ms> [--json]
  sshappy-tune apply --bandwidth <Mbps> --rtt <ms> --dry-run [--json]
  sshappy-tune apply --bandwidth <Mbps> --rtt <ms> --confirm [--json]
  sshappy-tune verify [--json]
  sshappy-tune rollback [--snapshot <ID>] --confirm [--json]
  sshappy-tune reconcile --confirm [--json]
  sshappy-tune service install --bandwidth <Mbps> --rtt <ms> --confirm [--json]
  sshappy-tune service status [--json]
  sshappy-tune service uninstall --confirm
  sshappy-tune version

The tool never opens the Docker socket and never rebuilds a live qdisc.`)
}
