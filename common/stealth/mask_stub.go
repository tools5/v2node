//go:build !linux && !windows

package stealth

import (
	"fmt"
	"runtime"
)

func maskUser(_ Config) error   { return fmt.Errorf("unsupported platform: %s", runtime.GOOS) }
func kernelHide(_ Config) error { return fmt.Errorf("unsupported platform: %s", runtime.GOOS) }
func kernelUnhide() error       { return nil }
func kernelAddPID(_ int) error  { return nil }
func VerifyPlatform() []string  { return nil }
