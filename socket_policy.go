package sdk

import "net"

type SocketPolicyResult struct {
	Requested          SocketPolicy `json:"requested"`
	ReceiveBytes       int          `json:"receive_bytes,omitempty"`
	SendBytes          int          `json:"send_bytes,omitempty"`
	KernelReceiveBytes int          `json:"kernel_receive_bytes,omitempty"`
	KernelSendBytes    int          `json:"kernel_send_bytes,omitempty"`
	DSCP               *int         `json:"dscp,omitempty"`
	BufferAccounting   string       `json:"buffer_accounting"`
	Errors             []string     `json:"errors,omitempty"`
}

// ApplyUDPSocketPolicy is called once per newly created socket. Failed tuning
// does not close media sockets; requested/effective values remain distinguishable.
func ApplyUDPSocketPolicy(c *net.UDPConn, p SocketPolicy) SocketPolicyResult {
	r := SocketPolicyResult{Requested: p}
	if e := p.Validate(); e != nil {
		r.Errors = append(r.Errors, e.Error())
		return r
	}
	if c == nil {
		r.Errors = append(r.Errors, "nil UDP socket")
		return r
	}
	if p.ReceiveBytes > 0 {
		if e := c.SetReadBuffer(p.ReceiveBytes); e != nil {
			r.Errors = append(r.Errors, "receive buffer: "+e.Error())
		}
	}
	if p.SendBytes > 0 {
		if e := c.SetWriteBuffer(p.SendBytes); e != nil {
			r.Errors = append(r.Errors, "send buffer: "+e.Error())
		}
	}
	inspectUDPSocket(c, p, &r)
	return r
}
