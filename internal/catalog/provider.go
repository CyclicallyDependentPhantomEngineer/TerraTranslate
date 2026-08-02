package catalog

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var terraformIdentifier = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]*$`)

func fetchProviderSchemas(binary string, specs []ProviderSpec, timeout time.Duration) ([]byte, string, error) {
	if binary == "" {
		binary = "terraform"
	}
	if timeout <= 0 {
		timeout = 20 * time.Minute
	}
	workspace, err := os.MkdirTemp("", "terra-translate-catalog-")
	if err != nil {
		return nil, "", fmt.Errorf("create provider refresh workspace: %w", err)
	}
	defer os.RemoveAll(workspace)

	configuration, err := providerConfiguration(specs)
	if err != nil {
		return nil, "", err
	}
	if err := os.WriteFile(filepath.Join(workspace, "providers.tf"), []byte(configuration), 0o600); err != nil {
		return nil, "", fmt.Errorf("write provider refresh configuration: %w", err)
	}

	environment := append(os.Environ(),
		"TF_IN_AUTOMATION=1",
		"TF_INPUT=0",
		"CHECKPOINT_DISABLE=1",
		"TF_DATA_DIR="+filepath.Join(workspace, ".terraform-data"),
	)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if _, err := runTerraformCommandRetry(ctx, environment, 3, binary, "-chdir="+workspace, "init", "-backend=false", "-input=false", "-no-color"); err != nil {
		return nil, "", fmt.Errorf("initialize provider workspace: %w", err)
	}
	schema, err := runTerraformCommand(ctx, environment, binary, "-chdir="+workspace, "providers", "schema", "-json")
	if err != nil {
		return nil, "", fmt.Errorf("read provider schemas: %w", err)
	}
	versionOutput, err := runTerraformCommand(ctx, environment, binary, "version")
	if err != nil {
		return nil, "", fmt.Errorf("read Terraform version: %w", err)
	}
	version := strings.TrimSpace(strings.SplitN(string(versionOutput), "\n", 2)[0])
	return schema, version, nil
}

func runTerraformCommandRetry(ctx context.Context, environment []string, attempts int, binary string, args ...string) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		output, err := runTerraformCommand(ctx, environment, binary, args...)
		if err == nil {
			return output, nil
		}
		lastErr = err
		if attempt < attempts-1 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(2<<attempt) * time.Second):
			}
		}
	}
	return nil, lastErr
}

func providerConfiguration(specs []ProviderSpec) (string, error) {
	if len(specs) == 0 {
		return "", fmt.Errorf("at least one provider is required")
	}
	var builder strings.Builder
	builder.WriteString("terraform {\n  required_providers {\n")
	for _, spec := range specs {
		if !terraformIdentifier.MatchString(spec.Name) {
			return "", fmt.Errorf("invalid provider name %q", spec.Name)
		}
		if len(strings.Split(spec.Source, "/")) != 2 {
			return "", fmt.Errorf("provider source %q must be namespace/name", spec.Source)
		}
		if spec.Version == "" || spec.Version == "latest" {
			return "", fmt.Errorf("provider %s does not have a resolved version", spec.Name)
		}
		fmt.Fprintf(&builder, "    %s = {\n      source = %q\n      version = %q\n    }\n", spec.Name, spec.Source, "="+spec.Version)
	}
	builder.WriteString("  }\n}\n")
	return builder.String(), nil
}

func runTerraformCommand(ctx context.Context, environment []string, binary string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, binary, args...)
	command.Env = environment
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = strings.TrimSpace(stdout.String())
		}
		return nil, fmt.Errorf("%s %s: %w: %s", binary, strings.Join(args, " "), err, message)
	}
	return stdout.Bytes(), nil
}
