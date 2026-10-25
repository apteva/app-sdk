package sdk

// SetResourcePolicyForTest enables the same resolver/governor/socket policy in a
// testkit context. Call before starting goroutines and restore after stopping
// them; production uses the authenticated runtime controller instead.
func (c *AppCtx) SetResourcePolicyForTest(p ResourcePolicy) (func(), error) {
	rt, e := newResourceRuntime(c.manifest.Runtime.ResourcePolicy, "")
	if e != nil {
		return nil, e
	}
	resolved, e := ResolveResources(c.manifest.Runtime.ResourcePolicy, &p)
	if e != nil {
		return nil, e
	}
	if e = rt.governor.Configure(resolved); e != nil {
		return nil, e
	}
	rt.envelope.Policy = resolved
	rt.envelope.Revision = 1
	previous := c.resources
	c.resources = rt
	return func() { rt.governor.Close(); c.resources = previous }, nil
}
