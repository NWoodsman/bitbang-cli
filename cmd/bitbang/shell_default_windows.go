//go:build windows

package main

func defaultShellLabel() string { return "%COMSPEC% or cmd.exe" }
