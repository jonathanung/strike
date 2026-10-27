package main

import (
	"context"
	"flag"
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "run":
		os.Exit(runMain(os.Args[2:]))
	case "run-fallback":
		os.Exit(runFallbackMain(os.Args[2:]))
	case "inspect":
		os.Exit(inspectMain(os.Args[2:]))
	case "kvm-api":
		os.Exit(kvmMain())
	case "-h", "--help", "help":
		usage()
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `throwaway phase0 microvm driver

  run --runner DIR          start the TCG guest, prove isolation, run one echo turn
  run-fallback --strike BIN bubblewrap stand-in when the microVM cannot boot
  inspect --runner DIR      reject a runner that enables KVM, a NIC, or extra shares
  kvm-api                   ioctl KVM_GET_API_VERSION only (never CREATE_VCPU)

TCG only. This program does not pass -enable-kvm.
The fallback is not a virtual machine.
`)
}

func runMain(args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	runner := fs.String("runner", "", "microvm runner directory (nix build --out-link)")
	state := fs.String("state", defaultStateDir, "baked state directory")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *runner == "" {
		fmt.Fprintln(os.Stderr, "run: --runner is required")
		return 2
	}
	rep, err := execute(context.Background(), *runner, *state)
	out := *state + "/report.json"
	if rep != nil {
		if werr := writeReport(out, rep); werr != nil {
			fmt.Fprintln(os.Stderr, "write report:", werr)
		} else {
			fmt.Fprintln(os.Stderr, "wrote", out)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "phase0:", err)
		return 1
	}
	if rep == nil || !rep.Pass {
		fmt.Fprintln(os.Stderr, "phase0: proofs failed")
		return 1
	}
	fmt.Fprintln(os.Stderr, "phase0: pass")
	return 0
}

func runFallbackMain(args []string) int {
	fs := flag.NewFlagSet("run-fallback", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	strike := fs.String("strike", "", "host-built strike binary (CGO_ENABLED=0)")
	state := fs.String("state", defaultStateDir, "state directory")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *strike == "" {
		fmt.Fprintln(os.Stderr, "run-fallback: --strike is required")
		return 2
	}
	rep, err := executeFallback(context.Background(), *strike, *state)
	out := *state + "/report.json"
	if rep != nil {
		if werr := writeReport(out, rep); werr != nil {
			fmt.Fprintln(os.Stderr, "write report:", werr)
		} else {
			fmt.Fprintln(os.Stderr, "wrote", out)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "phase0:", err)
		return 1
	}
	if rep == nil || !rep.Pass {
		fmt.Fprintln(os.Stderr, "phase0: proofs failed")
		return 1
	}
	fmt.Fprintln(os.Stderr, "phase0: pass")
	return 0
}

func inspectMain(args []string) int {
	fs := flag.NewFlagSet("inspect", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	runner := fs.String("runner", "", "microvm runner directory")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *runner == "" {
		fmt.Fprintln(os.Stderr, "inspect: --runner is required")
		return 2
	}
	script, err := readRunnerScript(*runner)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := inspectScript(script); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	fmt.Fprintln(os.Stderr, "runner policy: ok")
	return 0
}

func kvmMain() int {
	v, err := kvmAPIVersion()
	if err != nil {
		fmt.Fprintln(os.Stderr, "kvm_api_version_error:", err)
		return 1
	}
	fmt.Printf("kvm_api_version=%d\n", v)
	return 0
}
