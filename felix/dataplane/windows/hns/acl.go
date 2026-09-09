package hns

// ApplyACLPolicy keeps endpoint mutation behind the same API boundary as
// endpoint discovery. Windows uses hcsshim; tests supply a recording API.
func (API) ApplyACLPolicy(id string, rules ...*ACLPolicy) error {
	endpoint := &HNSEndpoint{Id: id}
	return endpoint.ApplyACLPolicy(rules...)
}
