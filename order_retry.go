package main

type nonRetriableOrderError struct{ msg string }

func (e *nonRetriableOrderError) Error() string { return e.msg }

func isNonRetriableOrderError(err error) bool {
	_, ok := err.(*nonRetriableOrderError)
	return ok
}
