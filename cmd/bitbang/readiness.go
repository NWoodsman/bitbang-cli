package main

import "time"

// unreadyPeerTimeout allows time for an interactive PIN prompt while bounding
// peers that request a connection and never answer.
const unreadyPeerTimeout = 2 * time.Minute
