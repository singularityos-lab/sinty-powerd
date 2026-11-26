//go:build !linux

package thermal

import (
	"context"
	"errors"
)

type Trip struct{}

func ArmTrip(string, int64) (*Trip, error) { return nil, errors.New("no thermal events here") }

func (t *Trip) Restore() {}

func (t *Trip) Run(context.Context, func()) error { return nil }
