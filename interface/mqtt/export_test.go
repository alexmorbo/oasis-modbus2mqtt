package mqtt

import "context"

// CommandQueueSize exposes commandQueueSize to the external test package.
const CommandQueueSize = commandQueueSize

// FlushForTest blocks until every job queued before the call has run. The
// worker is a single FIFO, so a sentinel job running proves that.
func (c *CommandSubscriber) FlushForTest() {
	ran := make(chan struct{})
	c.jobs <- commandJob{topic: "flush", run: func(context.Context) { close(ran) }}
	<-ran
}
