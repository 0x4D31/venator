package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/0x4D31/venator/connector"
	"github.com/0x4D31/venator/internal/config"
	"github.com/0x4D31/venator/internal/engine"
	"github.com/0x4D31/venator/internal/llm"
	"github.com/0x4D31/venator/internal/llm/provider"
	"github.com/0x4D31/venator/internal/model"
	"github.com/sirupsen/logrus"
)

var version = "0.2.0"

var logger = logrus.StandardLogger()

func main() { os.Exit(realMain(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }

func realMain(arguments []string, stdin io.Reader, stdout, stderr io.Writer) int {
	logger.SetOutput(stderr)
	if len(arguments) == 0 {
		printUsage(stderr)
		return 2
	}
	command := arguments[0]
	if command == "--version" || command == "-v" {
		if rejectUnexpectedArguments(arguments[1:], stderr) {
			return 2
		}
		fmt.Fprintf(stdout, "venator %s\n", version)
		return 0
	}
	if command == "--help" || command == "-h" {
		if rejectUnexpectedArguments(arguments[1:], stderr) {
			return 2
		}
		printUsage(stdout)
		return 0
	}
	if strings.HasPrefix(command, "-") {
		command = "run" // v0.1 compatibility
	} else {
		arguments = arguments[1:]
	}
	switch command {
	case "run":
		return runCommand(arguments, stdin, stdout, stderr)
	case "validate":
		return validateCommand(arguments, stderr)
	case "version":
		if rejectUnexpectedArguments(arguments, stderr) {
			return 2
		}
		fmt.Fprintf(stdout, "venator %s\n", version)
		return 0
	case "help":
		if rejectUnexpectedArguments(arguments, stderr) {
			return 2
		}
		printUsage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command %q\n", command)
		printUsage(stderr)
		return 2
	}
}

func rejectUnexpectedArguments(arguments []string, stderr io.Writer) bool {
	if len(arguments) == 0 {
		return false
	}
	fmt.Fprintf(stderr, "unexpected arguments: %s\n", strings.Join(arguments, " "))
	return true
}

type commandOptions struct {
	rulePath   string
	globalPath string
	logLevel   string
	reportFile string
	force      bool
}

func parseOptions(arguments []string, stderr io.Writer, includeRunOptions bool) (*commandOptions, error) {
	fs := flag.NewFlagSet("venator", flag.ContinueOnError)
	fs.SetOutput(stderr)
	opts := &commandOptions{}
	fs.StringVar(&opts.rulePath, "rule-config", "", "path to the rule YAML")
	fs.StringVar(&opts.rulePath, "r", "", "path to the rule YAML")
	fs.StringVar(&opts.globalPath, "global-config", "config/files/global_config.yaml", "path to the global YAML")
	fs.StringVar(&opts.globalPath, "c", "config/files/global_config.yaml", "path to the global YAML")
	if includeRunOptions {
		fs.StringVar(&opts.logLevel, "log-level", "info", "trace, debug, info, warn, error")
		fs.StringVar(&opts.logLevel, "l", "info", "trace, debug, info, warn, error")
		fs.StringVar(&opts.reportFile, "report-file", "", "write the JSON run report atomically to this path")
		fs.BoolVar(&opts.force, "force", false, "run a disabled rule")
	}
	if err := fs.Parse(arguments); err != nil {
		return nil, err
	}
	if fs.NArg() != 0 {
		return nil, fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	if opts.rulePath == "" {
		return nil, fmt.Errorf("--rule-config is required")
	}
	return opts, nil
}

func runCommand(arguments []string, stdin io.Reader, stdout, stderr io.Writer) int {
	opts, err := parseOptions(arguments, stderr, true)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintf(stderr, "invalid arguments: %v\n", err)
		return 2
	}
	if err := setLogLevel(opts.logLevel); err != nil {
		fmt.Fprintf(stderr, "invalid log level: %v\n", err)
		return 2
	}
	rule, global, err := loadConfigs(opts.rulePath, opts.globalPath)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 2
	}

	baseCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(baseCtx, global.Runtime.Timeout.Value())
	defer cancel()
	registry := connector.NewRegistry(ctx, global, stdin, stdout)
	registryClosed := false
	defer func() {
		if registryClosed {
			return
		}
		if err := registry.Close(); err != nil {
			logger.Errorf("close connectors: %v", err)
		}
	}()
	if rule.Enabled || opts.force {
		if err := registry.PreflightRule(rule); err != nil {
			fmt.Fprintf(stderr, "invalid connector configuration: %v\n", err)
			return 2
		}
		if err := engine.ValidateRuleFiles(opts.rulePath, rule); err != nil {
			fmt.Fprintf(stderr, "invalid rule file reference: %v\n", err)
			return 2
		}
	}

	var review engine.ReviewFunc
	if (rule.Enabled || opts.force) && rule.LLM != nil && rule.LLM.Enabled {
		llmSettings := global.LLM
		resolveErr := config.ResolveEnv(&llmSettings)
		var client provider.Client
		var initErr error
		if resolveErr != nil {
			initErr = resolveErr
		} else {
			client, initErr = llm.New(provider.Config{
				Provider: provider.Provider(llmSettings.Provider), APIKey: llmSettings.APIKey,
				Model: llmSettings.Model, ServerURL: llmSettings.ServerURL, Temperature: llmSettings.Temperature,
			})
		}
		reviewerName := llmSettings.Provider + "/" + llmSettings.Model
		review = func(ctx context.Context, findings []model.Finding, cfg *config.RuleConfig) (map[string]model.Review, error) {
			if initErr != nil {
				return nil, fmt.Errorf("initialize LLM reviewer: %w", initErr)
			}
			return llm.Review(ctx, client, findings, cfg, reviewerName)
		}
	}

	report, runErr := engine.Run(ctx, registry, rule, engine.Options{
		RulePath: opts.rulePath, Force: opts.force, Review: review,
		MaxRecords: global.Runtime.MaxRecords, MaxBytes: global.Runtime.MaxBytes,
		ReviewTimeout: global.LLM.Timeout.Value(),
	})
	closeErr := registry.Close()
	registryClosed = true
	if closeErr != nil {
		runErr = errors.Join(runErr, fmt.Errorf("close connectors: %w", closeErr))
		report.Status = "failed"
		report.Error = runErr.Error()
	}
	if opts.reportFile != "" {
		if err := writeReport(opts.reportFile, report); err != nil {
			runErr = errors.Join(runErr, err)
		}
	}
	if report.ReviewError != "" {
		logger.WithFields(logrus.Fields{"run_id": report.RunID, "rule_id": report.RuleID}).Warnf("LLM review unavailable: %s", report.ReviewError)
	}
	for _, receipt := range report.Sinks {
		if receipt.Error != "" && !receipt.Required {
			logger.WithFields(logrus.Fields{"run_id": report.RunID, "sink": receipt.Name}).Warnf("best-effort publisher failed: %s", receipt.Error)
		}
	}
	if runErr != nil {
		logger.Errorf("run %s failed: %v", report.RunID, runErr)
		return 1
	}
	logger.WithFields(logrus.Fields{
		"run_id": report.RunID, "status": report.Status, "queried": report.Queried,
		"excluded": report.Excluded, "findings": report.Findings,
	}).Info("rule run completed")
	return 0
}

func validateCommand(arguments []string, stderr io.Writer) int {
	opts, err := parseOptions(arguments, stderr, false)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintf(stderr, "invalid arguments: %v\n", err)
		return 2
	}
	rule, global, err := loadConfigs(opts.rulePath, opts.globalPath)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 2
	}
	registry := connector.NewRegistry(context.Background(), global, strings.NewReader(""), io.Discard)
	defer func() {
		if err := registry.Close(); err != nil {
			logger.Errorf("close connectors after validation: %v", err)
		}
	}()
	if err := registry.ValidateRule(rule); err != nil {
		fmt.Fprintf(stderr, "invalid connector configuration: %v\n", err)
		return 2
	}
	if err := validateReviewer(rule, global); err != nil {
		fmt.Fprintf(stderr, "invalid LLM reviewer configuration: %v\n", err)
		return 2
	}
	if err := engine.ValidateRuleFiles(opts.rulePath, rule); err != nil {
		fmt.Fprintf(stderr, "invalid rule file reference: %v\n", err)
		return 2
	}
	fmt.Fprintln(stderr, "configuration is valid")
	return 0
}

func validateReviewer(rule *config.RuleConfig, global *config.GlobalConfig) error {
	if rule == nil || rule.LLM == nil || !rule.LLM.Enabled {
		return nil
	}
	settings := global.LLM
	if err := config.ResolveEnv(&settings); err != nil {
		return err
	}
	_, err := llm.New(provider.Config{
		Provider: provider.Provider(settings.Provider), APIKey: settings.APIKey,
		Model: settings.Model, ServerURL: settings.ServerURL, Temperature: settings.Temperature,
	})
	return err
}

func loadConfigs(rulePath, globalPath string) (*config.RuleConfig, *config.GlobalConfig, error) {
	rule, err := config.ParseRuleConfig(rulePath)
	if err != nil {
		return nil, nil, fmt.Errorf("read rule config: %w", err)
	}
	global, err := config.ParseGlobalConfig(globalPath)
	if err != nil {
		return nil, nil, fmt.Errorf("read global config: %w", err)
	}
	return rule, global, nil
}

func setLogLevel(level string) error {
	lvl, err := logrus.ParseLevel(level)
	if err != nil {
		return err
	}
	logger.SetLevel(lvl)
	return nil
}

func writeReport(path string, report model.RunReport) error {
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("encode run report: %w", err)
	}
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, ".venator-report-*")
	if err != nil {
		return fmt.Errorf("create temporary run report: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return fmt.Errorf("secure run report: %w", err)
	}
	if _, err := temp.Write(append(encoded, '\n')); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write run report: %w", err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return fmt.Errorf("sync run report: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close run report: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("replace run report: %w", err)
	}
	return nil
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "usage:")
	fmt.Fprintln(w, "  venator run --rule-config RULE.yaml [--global-config GLOBAL.yaml]")
	fmt.Fprintln(w, "  venator validate --rule-config RULE.yaml [--global-config GLOBAL.yaml]")
	fmt.Fprintln(w, "  venator version")
	fmt.Fprintln(w, "\nLegacy v0.1 flags without the 'run' command remain supported.")
}
