/*
Copyright 2026 Google LLC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// kubectl-aibom is a kubectl plugin for reading, summarizing, and
// hash-verifying AIBOM resources (issue #58). It turns the documented
// jq/base64/shasum walkthrough into single commands:
//
//	kubectl aibom summary [-n ns | -A]
//	kubectl aibom view <aibom-name> [-n ns] [--raw]
//	kubectl aibom verify <aibom-name> [-n ns]
//
// verify exits non-zero when the inline document does not match the
// published sha256, so it composes into scripts and CI checks. A future
// mode gains signature verification when Design 002 ships.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"
)

var aibomGVR = schema.GroupVersionResource{
	Group:    "aibom.k8saibom.dev",
	Version:  "v1beta1",
	Resource: "aiboms",
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "kubectl-aibom: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("kubectl-aibom", flag.ContinueOnError)
	var (
		namespace  = fs.String("n", "", "namespace (defaults to the current kubeconfig namespace)")
		allNS      = fs.Bool("A", false, "all namespaces (summary and find)")
		raw        = fs.Bool("raw", false, "view: emit canonical bytes instead of pretty-printed JSON")
		fRuntime   = fs.String("runtime", "", "find: exact match on the attributed runtime name (e.g. vllm)")
		fModel     = fs.String("model", "", "find: case-sensitive substring of a model identity")
		fImage     = fs.String("image", "", "find: substring of a container image reference")
		fDigest    = fs.String("digest", "", "find: image sha256 (bare hex or sha256:-prefixed; a prefix matches)")
		fSigned    = fs.String("signed", "", "find: model signature state: unsigned | claimed | verified")
		kubeconfig = fs.String("kubeconfig", "", "path to the kubeconfig file")
		kubectx    = fs.String("context", "", "kubeconfig context to use")
	)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), `Usage:
  kubectl aibom summary [-n namespace | -A]
  kubectl aibom find [--runtime R] [--model M] [--image I] [--digest D] [--signed S] [-n namespace | -A]
  kubectl aibom view <aibom-name> [-n namespace] [--raw]
  kubectl aibom verify <aibom-name> [-n namespace]

find answers "where is this running?" — filters AND together; zero
matches prints "0 matches" and exits 0. Zero matches is not proof of
absence: namespaces that are not opted in, images not on the detection
allowlist, and unresolved digests are invisible here by design.

Flags:
`)
		fs.PrintDefaults()
	}

	if len(args) == 0 {
		fs.Usage()
		return fmt.Errorf("a subcommand is required")
	}
	sub := args[0]
	// Settle the subcommand before anything touches the kubeconfig, so
	// help and a mistyped subcommand work without a reachable cluster.
	switch sub {
	case "help", "-h", "--help":
		fs.Usage()
		return nil
	case "summary", "find", "view", "verify":
	default:
		fs.Usage()
		return fmt.Errorf("unknown subcommand %q", sub)
	}
	// Separate the single positional argument (the AIBOM name, for
	// view/verify) from flags before parsing, so both
	// `view -n ns NAME` and `view NAME -n ns` work — the stdlib flag
	// package stops at the first non-flag token otherwise.
	var positional []string
	var flagArgs []string
	for i := 1; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			positional = append(positional, a)
			continue
		}
		flagArgs = append(flagArgs, a)
		// A flag that takes a value consumes the next token unless it
		// used the -flag=value form. -A and --raw are booleans.
		if a != "-A" && a != "--raw" && a != "-raw" && !strings.Contains(a, "=") && i+1 < len(args) {
			i++
			flagArgs = append(flagArgs, args[i])
		}
	}
	if err := fs.Parse(flagArgs); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			// -h or --help after the subcommand: fs.Parse has already
			// printed the usage.
			return nil
		}
		return err
	}

	// Argument errors are reported before the kubeconfig is loaded too.
	// An argument the subcommand does not use is an error rather than
	// dropped: `find vllm` would list every AIBOM unfiltered, and
	// `view NAME -A` would send a cluster-scoped GET that 404s.
	used := map[string]bool{"n": true, "kubeconfig": true, "context": true}
	switch sub {
	case "summary":
		used["A"] = true
	case "find":
		for _, name := range []string{"A", "runtime", "model", "image", "digest", "signed"} {
			used[name] = true
		}
	case "view":
		used["raw"] = true
	}
	var unused []string
	nsSet := false
	fs.Visit(func(fl *flag.Flag) {
		if fl.Name == "n" {
			nsSet = true
		}
		if !used[fl.Name] {
			unused = append(unused, flagName(fl.Name))
		}
	})
	if len(unused) > 0 {
		return fmt.Errorf("%s does not apply to %s", strings.Join(unused, ", "), sub)
	}
	// -n and -A pick the scope one way or the other (`[-n namespace |
	// -A]`). With both, -A used to win and -n was dropped, so
	// `find -A -n prod-jobs` listed every namespace.
	if *allNS && nsSet {
		one := *namespace
		if one == "" {
			one = "NAMESPACE"
		}
		return fmt.Errorf("-n and -A cannot be used together (-n %s for one namespace, -A for all)", one)
	}
	if (sub == "summary" || sub == "find") && len(positional) > 0 {
		hint := fmt.Sprintf("for a namespace, use -n %s", positional[0])
		if sub == "find" {
			hint = fmt.Sprintf("filters are flags, e.g. --runtime %s", positional[0])
		}
		return fmt.Errorf("%s takes no positional arguments, got %q (%s)", sub, positional[0], hint)
	}
	f := findFilter{runtime: *fRuntime, model: *fModel, image: *fImage, digest: *fDigest, signed: *fSigned}
	switch sub {
	case "find":
		if f.signed != "" && f.signed != "unsigned" && f.signed != "claimed" && f.signed != "verified" {
			return fmt.Errorf("--signed must be one of unsigned, claimed, verified")
		}
		if f.digest != "" {
			if err := checkDigestFilter(f.digest); err != nil {
				return err
			}
		}
	case "view", "verify":
		if len(positional) != 1 {
			return fmt.Errorf("%s requires exactly one AIBOM name (try: kubectl aibom summary)", sub)
		}
	}

	loading := clientcmd.NewDefaultClientConfigLoadingRules()
	if *kubeconfig != "" {
		loading.ExplicitPath = *kubeconfig
	}
	cfgOverrides := &clientcmd.ConfigOverrides{CurrentContext: *kubectx}
	clientCfg := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loading, cfgOverrides)
	ns := *namespace
	if ns == "" && !*allNS {
		defNS, _, err := clientCfg.Namespace()
		if err != nil {
			return fmt.Errorf("resolving namespace: %w", err)
		}
		ns = defNS
	}
	restCfg, err := clientCfg.ClientConfig()
	if err != nil {
		return fmt.Errorf("loading kubeconfig: %w", err)
	}
	dyn, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		return err
	}
	ctx := context.Background()

	switch sub {
	case "summary":
		if *allNS {
			l, err := dyn.Resource(aibomGVR).List(ctx, metav1.ListOptions{})
			if err != nil {
				return err
			}
			return runSummary(l.Items, os.Stdout)
		}
		l, err := dyn.Resource(aibomGVR).Namespace(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return err
		}
		return runSummary(l.Items, os.Stdout)

	case "find":
		var items []unstructured.Unstructured
		if *allNS {
			l, err := dyn.Resource(aibomGVR).List(ctx, metav1.ListOptions{})
			if err != nil {
				return err
			}
			items = l.Items
		} else {
			l, err := dyn.Resource(aibomGVR).Namespace(ns).List(ctx, metav1.ListOptions{})
			if err != nil {
				return err
			}
			items = l.Items
		}
		return runFind(items, f, os.Stdout, os.Stderr)

	case "view", "verify":
		u, err := dyn.Resource(aibomGVR).Namespace(ns).Get(ctx, positional[0], metav1.GetOptions{})
		if err != nil {
			return err
		}
		if sub == "view" {
			return runView(u, *raw, os.Stdout)
		}
		ok, err := runVerify(u, os.Stdout)
		if err != nil {
			return err
		}
		if !ok {
			os.Exit(2)
		}
		return nil
	}
	return nil
}

// flagName spells a flag the way the usage text does: -n and -A,
// --runtime and the other long names.
func flagName(name string) string {
	if len(name) == 1 {
		return "-" + name
	}
	return "--" + name
}
