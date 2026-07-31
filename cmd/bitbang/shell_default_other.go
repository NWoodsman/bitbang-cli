//go:build !unix && !windows

package main

func defaultShellLabel() string { return "the platform shell" }
