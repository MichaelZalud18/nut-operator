//go:build hadron

/*
Copyright 2026 Michael Zalud.

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

package hadron

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spectrocloud/peg/pkg/machine/types"
)

type unverifiedMachine struct {
	types.Machine
	root string
}

func (m unverifiedMachine) Config() types.MachineConfig {
	return types.MachineConfig{StateDir: m.root}
}
func (m unverifiedMachine) Stop() error  { panic("unchecked Stop called") }
func (m unverifiedMachine) Clean() error { panic("unverified state removed") }

func TestCleanupRequiresVerifiedStartup(t *testing.T) {
	for name, cleanup := range map[string]func(types.Machine, time.Duration) error{
		"stop": SafeStop, "teardown": SafeTeardown,
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "pid"), []byte("12345"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := cleanup(unverifiedMachine{root: root}, time.Second); err == nil {
				t.Fatal("cleanup accepted an unverified machine")
			}
		})
	}
}

func TestNeverStartedMachineCanBeCleaned(t *testing.T) {
	m, _, err := NewSafeMachine(Config{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(m.Config().StateDir) })
	if err := SafeStop(m, time.Second); err == nil {
		t.Fatal("never-started machine accepted as a verified process")
	}
	if err := m.Clean(); err != nil {
		t.Fatal(err)
	}
}
