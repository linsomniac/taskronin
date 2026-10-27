package backup

// setCrashHook simulates process death at a snapshot boundary, preserving artifacts.
func setCrashHook(m *Manager, fn func(step string) error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.crashHook = fn
}
