//go:build talos

package talos

import (
	"os"
	"testing"
	"time"

	"github.com/spectrocloud/peg/pkg/machine/types"
)

type unverifiedMachine struct{ types.Machine }

func (m unverifiedMachine) Stop() error  { panic("unchecked Stop called") }
func (m unverifiedMachine) Clean() error { panic("unverified state removed") }

func TestCleanupRequiresVerifiedStartup(t *testing.T) {
	for name, cleanup := range map[string]func(types.Machine, time.Duration) error{
		"stop": SafeStop, "teardown": SafeTeardown,
	} {
		t.Run(name, func(t *testing.T) {
			if err := cleanup(unverifiedMachine{}, time.Second); err == nil {
				t.Fatal("cleanup accepted an unverified machine")
			}
		})
	}
}

func TestNeverStartedMachineCanBeCleaned(t *testing.T) {
	m, err := NewSafeMachine(Config{})
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
