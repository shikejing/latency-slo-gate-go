package slogate

import "errors"

// Library-level sentinel errors.
var (
	ErrInvalidConfig    = errors.New("slogate: invalid histogram config")
	ErrConfigMismatch   = errors.New("slogate: histogram configs do not match")
	ErrCorruptState     = errors.New("slogate: persisted state is corrupt")
	ErrUnsupportedVer   = errors.New("slogate: unsupported state format version")
	ErrSourceRegression = errors.New("slogate: same-source counts moved backwards")
	ErrEmptyHistogram   = errors.New("slogate: histogram has no samples")
	ErrInvalidArgument  = errors.New("slogate: invalid argument")
)
