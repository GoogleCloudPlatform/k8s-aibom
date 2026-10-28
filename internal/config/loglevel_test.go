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

package config

import (
	"context"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	aibomv1beta1 "github.com/GoogleCloudPlatform/k8s-aibom/api/v1beta1"
)

func TestLogLevelSubscriber_SetsSpecLevel(t *testing.T) {
	cases := []struct {
		spec string
		want zapcore.Level
	}{
		{"debug", zapcore.DebugLevel},
		{"info", zapcore.InfoLevel},
		{"warn", zapcore.WarnLevel},
		{"error", zapcore.ErrorLevel},
		{"", zapcore.InfoLevel},
	}
	for _, tc := range cases {
		t.Run(tc.spec, func(t *testing.T) {
			level := zap.NewAtomicLevelAt(zapcore.FatalLevel)
			LogLevelSubscriber(level)(nil, &Snapshot{LogLevel: tc.spec})
			if got := level.Level(); got != tc.want {
				t.Errorf("level after spec.logging.level=%q = %v, want %v", tc.spec, got, tc.want)
			}
		})
	}
}

func TestLogLevelSubscriber_IgnoresUnknownLevel(t *testing.T) {
	level := zap.NewAtomicLevelAt(zapcore.WarnLevel)
	LogLevelSubscriber(level)(nil, &Snapshot{LogLevel: "verbose"})
	if got := level.Level(); got != zapcore.WarnLevel {
		t.Errorf("level after an unknown value = %v, want it unchanged (warn)", got)
	}
}

// TestLogLevel_HotReloadsThroughStore follows the level through
// snapshot swaps: a CR asking for debug enables V(1) logs, and going
// back to compiled defaults (CR deleted) returns to info.
func TestLogLevel_HotReloadsThroughStore(t *testing.T) {
	level := zap.NewAtomicLevelAt(zapcore.InfoLevel)
	store := NewStore(DefaultSnapshot())
	store.Subscribe(LogLevelSubscriber(level))

	store.Store(&Snapshot{LogLevel: "debug"})
	if !level.Enabled(zapcore.DebugLevel) {
		t.Fatalf("debug not enabled after storing spec.logging.level=debug (level %v)", level.Level())
	}

	store.Store(&Snapshot{LogLevel: "error"})
	if level.Enabled(zapcore.InfoLevel) {
		t.Fatalf("info still enabled after storing spec.logging.level=error (level %v)", level.Level())
	}

	store.Store(DefaultSnapshot())
	if got := level.Level(); got != zapcore.InfoLevel {
		t.Fatalf("level after compiled defaults = %v, want info", got)
	}
}

func TestLoad_ValidCR_CarriesLogLevel(t *testing.T) {
	cr := &aibomv1beta1.AIBOMControllerConfig{
		ObjectMeta: metav1.ObjectMeta{Name: DefaultConfigName},
		Spec: aibomv1beta1.AIBOMControllerConfigSpec{
			Logging: aibomv1beta1.LoggingConfig{Level: "debug", Format: "json"},
		},
	}
	result, err := newLoader(t, nil, cr).Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if result.HasErrors() {
		t.Fatalf("unexpected errors: %s", result.AggregateMessage())
	}
	if got := result.Snapshot.LogLevel; got != "debug" {
		t.Errorf("Snapshot.LogLevel = %q, want %q", got, "debug")
	}
}
