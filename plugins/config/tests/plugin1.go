package tests

import (
	"errors"
	"time"

	"github.com/temporalio/roadrunner-temporal/config"
)

// ReloadConfig is a Reload configuration point.
type ReloadConfig struct {
	Interval time.Duration
	Patterns []string
	Services map[string]ServiceConfig
}

type ServiceConfig struct {
	Enabled bool
	Recursive bool
	Patterns []string
	Dirs []string
	Ignore []string
}

type Foo struct {
	configProvider config.Provider
}


// Depends on S2 and DB (S3 in the current case)
func (f *Foo) Init(p config.Provider) error {
	f.configProvider = p
	return nil
}

func (f *Foo) Serve() chan error {
	errCh := make(chan error, 1)

	r := &ReloadConfig{}
	err := f.configProvider.UnmarshalKey("reload", r)
	if err != nil {
		errCh <- err
	}

	if len(r.Patterns) == 0 {
		errCh <- errors.New("should be at least one pattern, but got 0")
	}

	return errCh
}

func (f *Foo) Stop() error {
	return nil
}
