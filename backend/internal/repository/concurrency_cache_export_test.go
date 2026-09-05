package repository

type concurrencyTestHooks struct {
	acquireScripts int
	reapCalls      int
}

func (h *concurrencyTestHooks) onAcquireScript() { h.acquireScripts++ }
func (h *concurrencyTestHooks) onReap()          { h.reapCalls++ }

func (c *concurrencyCache) attachTestHooks() *concurrencyTestHooks {
	h := &concurrencyTestHooks{}
	c.testHooks = h
	return h
}
