//go:build !unix && !windows

package main

type identityLock struct{}

func acquireIdentityLock(string) (*identityLock, int, error) { return &identityLock{}, 0, nil }
func (*identityLock) release()                               {}
