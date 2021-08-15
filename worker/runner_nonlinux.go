// +build !linux

package worker

import "fmt"

func newLimitedRunner(*JobLimitConfig) (runner, error) {
	return nil, fmt.Errorf("resource limited runner only supported on linux")
}

func ExecLimitedChild([]string) error {
	return fmt.Errorf("limited child execution only supported on linux")
}
