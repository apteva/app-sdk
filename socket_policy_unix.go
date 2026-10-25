//go:build linux || darwin

package sdk

import (
	"golang.org/x/sys/unix"
	"net"
	"runtime"
)

func inspectUDPSocket(c *net.UDPConn, p SocketPolicy, r *SocketPolicyResult) {
	raw, e := c.SyscallConn()
	if e != nil {
		r.Errors = append(r.Errors, e.Error())
		return
	}
	e = raw.Control(func(fd uintptr) {
		get := func(level, key int) int {
			v, e := unix.GetsockoptInt(int(fd), level, key)
			if e != nil {
				r.Errors = append(r.Errors, e.Error())
			}
			return v
		}
		r.KernelReceiveBytes = get(unix.SOL_SOCKET, unix.SO_RCVBUF)
		r.KernelSendBytes = get(unix.SOL_SOCKET, unix.SO_SNDBUF)
		divisor := 1
		r.BufferAccounting = "kernel"
		if runtime.GOOS == "linux" {
			divisor = 2
			r.BufferAccounting = "linux_doubled"
		}
		r.ReceiveBytes = r.KernelReceiveBytes / divisor
		r.SendBytes = r.KernelSendBytes / divisor
		level, key := unix.IPPROTO_IP, unix.IP_TOS
		if a, ok := c.LocalAddr().(*net.UDPAddr); ok && a.IP.To4() == nil {
			level, key = unix.IPPROTO_IPV6, unix.IPV6_TCLASS
		}
		old, err := unix.GetsockoptInt(int(fd), level, key)
		if err != nil {
			r.Errors = append(r.Errors, "traffic class read: "+err.Error())
			return
		}
		if p.DSCP != nil {
			if err = unix.SetsockoptInt(int(fd), level, key, (*p.DSCP<<2)|(old&3)); err != nil {
				r.Errors = append(r.Errors, "traffic class write: "+err.Error())
			}
		}
		v, err := unix.GetsockoptInt(int(fd), level, key)
		if err != nil {
			r.Errors = append(r.Errors, "traffic class readback: "+err.Error())
			return
		}
		d := (v >> 2) & 63
		r.DSCP = &d
		if p.DSCP != nil && *p.DSCP != d {
			r.Errors = append(r.Errors, "DSCP readback differs from request")
		}
	})
	if e != nil {
		r.Errors = append(r.Errors, e.Error())
	}
	if p.ReceiveBytes > r.ReceiveBytes {
		r.Errors = append(r.Errors, "receive buffer capped below request")
	}
	if p.SendBytes > r.SendBytes {
		r.Errors = append(r.Errors, "send buffer capped below request")
	}
}
