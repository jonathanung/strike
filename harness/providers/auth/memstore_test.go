package auth

import "sync"

type memStore struct {
	mu    sync.Mutex
	creds map[string]Credential
}

func newMemStore() *memStore {
	return &memStore{creds: map[string]Credential{}}
}

func (m *memStore) Get(provider string) (Credential, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.creds[canonicalProvider(provider)]
	return c, ok
}

func (m *memStore) Set(provider string, c Credential) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.creds[canonicalProvider(provider)] = c
	return nil
}
