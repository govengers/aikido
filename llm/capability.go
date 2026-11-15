package llm

// Capability returns the first client in the chain client, client.Unwrap(), client.Unwrap().Unwrap(), ...
// that implements T, the way errors.As walks an error chain. A decorator such as ObservedClient
// implements only Client and offers the client it wraps through an Unwrap() Client method, so
// Capability[ImageGenerator](observed) reaches the provider's images endpoint behind it. A call made
// through the returned value skips the decorators above the client that implements T.
func Capability[T any](client Client) (T, bool) {
	for client != nil {
		if capable, ok := client.(T); ok {
			return capable, true
		}
		wrapper, ok := client.(interface{ Unwrap() Client })
		if !ok {
			break
		}
		client = wrapper.Unwrap()
	}
	var none T
	return none, false
}
