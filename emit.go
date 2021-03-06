package ecsevent

// Emitter is a common interface for all ECSEvent adapters.
type Emitter interface {
	// Emit takes a flat map of ECS fields and values, converts it to a nested
	// map, and emits the event on the underlying logger implementation.
	Emit(event map[string]interface{})
}
