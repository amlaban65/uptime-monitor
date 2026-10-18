package checker

import (
	"context"
	"net"
	"time"

	"github.com/amlaban65/uptime-monitor/internal/monitor"
)

// TCP checks that a TCP port accepts connections.
type TCP struct {
	dialer *net.Dialer
}

func NewTCP() *TCP { return &TCP{dialer: &net.Dialer{}} }

func (c *TCP) Check(ctx context.Context, m monitor.Monitor) monitor.Result {
	res := monitor.Result{MonitorID: m.ID, CheckedAt: time.Now()}
	start := time.Now()
	conn, err := c.dialer.DialContext(ctx, "tcp", m.Target)
	res.Latency = time.Since(start)
	if err != nil {
		res.Error = describe(ctx, err)
		return res
	}
	_ = conn.Close()
	res.OK = true
	return res
}
