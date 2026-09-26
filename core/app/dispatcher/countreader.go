package dispatcher

import (
	"sync/atomic"
	"time"

	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
)

var _ buf.TimeoutReader = (*CounterReader)(nil)

type CounterReader struct {
	Reader            buf.TimeoutReader
	Counter           *atomic.Int64
	ConnectionCounter *atomic.Int64
}

func (c *CounterReader) ReadMultiBufferTimeout(timeout time.Duration) (buf.MultiBuffer, error) {
	mb, err := c.Reader.ReadMultiBufferTimeout(timeout)
	c.count(mb)
	return mb, err
}

func (c *CounterReader) ReadMultiBuffer() (buf.MultiBuffer, error) {
	mb, err := c.Reader.ReadMultiBuffer()
	c.count(mb)
	return mb, err
}

func (c *CounterReader) count(mb buf.MultiBuffer) {
	if mb.Len() > 0 {
		c.Counter.Add(int64(mb.Len()))
		if c.ConnectionCounter != nil {
			c.ConnectionCounter.Add(int64(mb.Len()))
		}
	}
}

func (c *CounterReader) Interrupt() {
	if reader, ok := c.Reader.(*buf.TimeoutWrapperReader); ok {
		common.Interrupt(reader.Reader)
		return
	}
	common.Interrupt(c.Reader)
}
