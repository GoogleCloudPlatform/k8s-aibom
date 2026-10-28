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
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// logLevels maps spec.logging.level to zap levels. logr's V(1) logs
// are zap's debug level, so "debug" is what turns them on.
var logLevels = map[string]zapcore.Level{
	"debug": zapcore.DebugLevel,
	"info":  zapcore.InfoLevel,
	"warn":  zapcore.WarnLevel,
	"error": zapcore.ErrorLevel,
}

// LogLevelSubscriber returns a Store subscriber that sets level from
// each stored snapshot's LogLevel, so spec.logging.level hot-reloads
// like the rest of the config. A snapshot without a level (compiled
// defaults, a deleted CR) sets info, the default. A value outside the
// CRD enum leaves the level unchanged.
func LogLevelSubscriber(level zap.AtomicLevel) func(prev, next *Snapshot) {
	return func(_, next *Snapshot) {
		name := next.LogLevel
		if name == "" {
			name = "info"
		}
		if l, ok := logLevels[name]; ok {
			level.SetLevel(l)
		}
	}
}
