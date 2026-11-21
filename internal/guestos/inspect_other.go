//go:build !darwin && !linux && !windows

package guestos

import "context"

func Inspect(_ context.Context) Inspection {
	return Inspection{WorkbenchSupport: "unsupported-host", Detail: "此平台没有 Super Cage 本机工作台。"}
}
