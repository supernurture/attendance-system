package util

// Ternary returns trueVal if cond is true, otherwise falseVal.
func Ternary[T any](cond bool, trueVal T, falseVal T) T {
	if cond {
		return trueVal
	}
	return falseVal
}
