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
	"flag"
	"fmt"
	"os"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
		allNS      = fs.Bool("A", false, "all namespaces (summary only)")
		raw        = fs.Bool("raw", false, "view: emit canonical bytes instead of pretty-printed JSON")
		kubeconfig = fs.String("kubeconfig", "", "path to the kubeconfig file")
		kubectx    = fs.String("context", "", "kubeconfig context to use")
	)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), `Usage:
  kubectl aibom summary [-n namespace | -A]
  kubectl aibom view <aibom-name> [-n namespace] [--raw]
  kubectl aibom verify <aibom-name> [-n namespace]

Flags:
`)
		fs.PrintDefaults()
	}

	if len(args) == 0 {
		fs.Usage()
		return fmt.Errorf("a subcommand is required")
	}
	sub := args[0]
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
		return err
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

	case "view", "verify":
		if len(positional) != 1 {
			return fmt.Errorf("%s requires exactly one AIBOM name (try: kubectl aibom summary)", sub)
		}
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

	case "help", "-h", "--help":
		fs.Usage()
		return nil
	default:
		fs.Usage()
		return fmt.Errorf("unknown subcommand %q", sub)
	}
}
