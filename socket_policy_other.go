//go:build !linux && !darwin

package sdk

import "net"

func inspectUDPSocket(_ *net.UDPConn, _ SocketPolicy, r *SocketPolicyResult) {
	r.BufferAccounting = "unavailable"
	r.Errors = append(r.Errors, "socket readback and DSCP unsupported on this platform")
}
